package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

var tputPrompts = []string{
	"What is Newton's first law of motion?",
	"Explain the water cycle in two sentences.",
	"What is machine learning?",
	"Name three programming languages and their main use cases.",
	"What is the capital of Australia?",
	"Explain what an API is.",
	"What is the difference between RAM and storage?",
	"Describe the greenhouse effect briefly.",
	"What is recursion in computer science?",
	"Explain supply and demand in economics.",
}

type tputResult struct {
	State        string  `json:"state"`
	AgentID      string  `json:"agent_id"`
	LatencyS     float64 `json:"latency_s"`
	OutputTokens float64 `json:"output_tokens"`
	TPS          float64 `json:"tps"`
	Error        string  `json:"error"`
}

var tputClient = &http.Client{Timeout: 30 * time.Second}

func defaultHubURL() string {
	home, _ := os.UserHomeDir()
	data, err := os.ReadFile(filepath.Join(home, ".igrid", "config.yaml"))
	if err != nil {
		return "http://localhost:9000"
	}
	var cfg struct {
		Hub struct {
			URLs []string `yaml:"urls"`
			Port int      `yaml:"port"`
		} `yaml:"hub"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return "http://localhost:9000"
	}
	if len(cfg.Hub.URLs) > 0 {
		return strings.TrimRight(cfg.Hub.URLs[0], "/")
	}
	if cfg.Hub.Port != 0 {
		return fmt.Sprintf("http://localhost:%d", cfg.Hub.Port)
	}
	return "http://localhost:9000"
}

func main() {
	hubURL := flag.String("hub", defaultHubURL(), "Hub URL")
	numTasks := flag.Int("n", 30, "Number of tasks to fire")
	model := flag.String("model", "llama3", "Model name")
	maxTokens := flag.Int("max-tokens", 128, "Max output tokens")
	concurrency := flag.Int("concurrency", 20, "Max parallel submissions")
	timeoutS := flag.Int("timeout", 180, "Timeout in seconds")
	label := flag.String("label", "", "Run label e.g. '1-agent', '3-agents'")
	flag.Parse()

	runLabel := *label
	if runLabel == "" {
		runLabel = fmt.Sprintf("run-%s", time.Now().Format("150405"))
	}

	// Show active agents
	showTputAgents(*hubURL, *numTasks, *model, *concurrency, runLabel)

	prompts := make([]string, *numTasks)
	for i := range prompts {
		prompts[i] = tputPrompts[i%len(tputPrompts)]
	}

	sem := make(chan struct{}, *concurrency)
	var wg sync.WaitGroup
	results := make([]tputResult, *numTasks)

	wallStart := time.Now()

	for i, p := range prompts {
		wg.Add(1)
		go func(idx int, prompt string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[idx] = tputSubmitAndWait(*hubURL, prompt, *model, *maxTokens, *timeoutS)
		}(i, p)
	}

	wg.Wait()
	wallTime := time.Since(wallStart).Seconds()

	printTputSummary(results, wallTime, runLabel, *numTasks, *model, *hubURL)
	saveResults("throughput", map[string]interface{}{
		"timestamp":   time.Now().Format(time.RFC3339),
		"hub":         *hubURL,
		"model":       *model,
		"num_tasks":   *numTasks,
		"concurrency": *concurrency,
		"label":       runLabel,
		"wall_time_s": wallTime,
		"results":     results,
	})
}

func saveResults(name string, data interface{}) {
	if err := os.MkdirAll("out", 0755); err != nil {
		return
	}
	ts := time.Now().Format("20060102_150405")
	path := filepath.Join("out", fmt.Sprintf("%s_%s.json", name, ts))
	f, err := os.Create(path)
	if err != nil {
		return
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	enc.Encode(data)
	fmt.Printf("\n  Results saved: %s\n", path)
}

func showTputAgents(hub string, numTasks int, model string, concurrency int, label string) {
	resp, err := tputClient.Get(fmt.Sprintf("%s/agents", hub))
	if err != nil {
		fmt.Printf("\n  Multi-Agent Throughput\n")
		fmt.Printf("    Hub:         %s\n\n", hub)
		return
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(resp.Body)
	var data map[string]interface{}
	json.Unmarshal(bodyBytes, &data)
	agents, _ := data["agents"].([]interface{})

	fmt.Printf("\n  Multi-Agent Throughput\n")
	fmt.Printf("    Hub:         %s\n", hub)
	fmt.Printf("    Agents:      %d online\n", len(agents))
	fmt.Printf("    Tasks:       %d\n", numTasks)
	fmt.Printf("    Model:       %s\n", model)
	fmt.Printf("    Concurrency: %d\n", concurrency)
	fmt.Printf("    Label:       %s\n", label)
	for _, a := range agents {
		agent, ok := a.(map[string]interface{})
		if !ok {
			continue
		}
		fmt.Printf("      - %s (%s)\n", str(agent, "name"), str(agent, "tier"))
	}
	fmt.Println()
}

func tputSubmitAndWait(hub, prompt, model string, maxTokens, timeoutS int) tputResult {
	taskID := fmt.Sprintf("tput-%s", uuid.New().String()[:8])
	t0 := time.Now()

	payload := map[string]interface{}{
		"task_id":    taskID,
		"model":      model,
		"prompt":     prompt,
		"max_tokens": maxTokens,
	}
	body, _ := json.Marshal(payload)
	resp, err := tputClient.Post(fmt.Sprintf("%s/tasks", hub), "application/json", bytes.NewReader(body))
	if err != nil {
		return tputResult{State: "SUBMIT_FAILED", Error: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return tputResult{State: "SUBMIT_REJECTED", Error: fmt.Sprintf("HTTP %d", resp.StatusCode)}
	}

	deadline := time.Now().Add(time.Duration(timeoutS) * time.Second)
	interval := 1500 * time.Millisecond

	for time.Now().Before(deadline) {
		getResp, err := tputClient.Get(fmt.Sprintf("%s/tasks/%s", hub, taskID))
		if err != nil {
			time.Sleep(interval)
			continue
		}
		bodyBytes, _ := io.ReadAll(getResp.Body)
		getResp.Body.Close()

		var task map[string]interface{}
		if err := json.Unmarshal(bodyBytes, &task); err != nil {
			time.Sleep(interval)
			continue
		}

		state := str(task, "state")
		if state == "COMPLETE" {
			res, _ := task["result"].(map[string]interface{})
			if res == nil {
				res = task
			}
			elapsed := time.Since(t0).Seconds()
			outTok := num(res, "output_tokens")
			tps := 0.0
			if elapsed > 0 {
				tps = outTok / elapsed
			}
			return tputResult{
				State:        "COMPLETE",
				AgentID:      str(res, "agent_id"),
				LatencyS:     elapsed,
				OutputTokens: outTok,
				TPS:          tps,
			}
		}
		if state == "FAILED" {
			return tputResult{State: "FAILED"}
		}

		time.Sleep(interval)
		if interval < 8*time.Second {
			interval = time.Duration(float64(interval) * 1.2)
		}
	}

	return tputResult{State: "TIMEOUT"}
}

func printTputSummary(results []tputResult, wallTime float64, label string, n int, model, hub string) {
	var completed []tputResult
	for _, r := range results {
		if r.State == "COMPLETE" {
			completed = append(completed, r)
		}
	}

	totalTokens := 0.0
	for _, r := range completed {
		totalTokens += r.OutputTokens
	}
	throughput := 0.0
	if wallTime > 0 {
		throughput = totalTokens / wallTime
	}

	agentDist := make(map[string]int)
	for _, r := range completed {
		aid := lastN(r.AgentID, 16)
		agentDist[aid]++
	}

	fmt.Printf("\n  %s\n", strings.Repeat("=", 60))
	fmt.Printf("  Label:      %s\n", label)
	fmt.Printf("  Completed:  %d/%d\n", len(completed), n)
	fmt.Printf("  Wall time:  %.1fs\n", wallTime)
	fmt.Printf("  Tokens:     %.0f\n", totalTokens)
	fmt.Printf("  Throughput: %.1f tokens/s  <- KEY METRIC\n", throughput)

	if len(completed) > 0 {
		avgLat := 0.0
		avgTPS := 0.0
		for _, r := range completed {
			avgLat += r.LatencyS
			avgTPS += r.TPS
		}
		avgLat /= float64(len(completed))
		avgTPS /= float64(len(completed))
		fmt.Printf("  Avg latency:%.1fs  |  Avg TPS/agent: %.1f\n", avgLat, avgTPS)
	}

	fmt.Printf("\n  Agent distribution:\n")
	for aid, cnt := range agentDist {
		bar := strings.Repeat("█", cnt)
		fmt.Printf("    ..%-18s %3d tasks  %s\n", aid, cnt, bar)
	}
}

func str(m map[string]interface{}, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	s, ok := v.(string)
	if ok {
		return s
	}
	return fmt.Sprint(v)
}

func num(m map[string]interface{}, key string) float64 {
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	}
	return 0
}

func lastN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
