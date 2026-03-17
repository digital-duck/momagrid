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

var overnightPrompts = []string{
	"Summarise the key principles of thermodynamics.",
	"What is the difference between supervised and unsupervised learning?",
	"Explain how the internet works in three paragraphs.",
	"What are the main causes of climate change?",
	"Describe the human digestive system.",
	"What is blockchain technology and how does it work?",
	"Explain the concept of recursion with an example.",
	"What are the principles of object-oriented programming?",
	"Describe the water cycle and its importance.",
	"What is the difference between machine learning and deep learning?",
	"Explain how a neural network learns.",
	"What is quantum computing and what problems can it solve?",
	"Describe the structure of DNA.",
	"What is the significance of the Turing test?",
	"Explain the concept of entropy in information theory.",
	"What are the main differences between TCP and UDP?",
	"Describe how a compiler works.",
	"What is the difference between a process and a thread?",
	"Explain the CAP theorem in distributed systems.",
	"What is the Big Bang theory?",
}

type overnightResult struct {
	TaskID       string
	State        string
	AgentID      string
	LatencyS     float64
	OutputTokens float64
	TPS          float64
	Error        string
}

var overnightClient = &http.Client{Timeout: 30 * time.Second}

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
	numTasks := flag.Int("tasks", 100, "Total number of tasks to submit")
	model := flag.String("model", "llama3", "Model name")
	maxTokens := flag.Int("max-tokens", 256, "Max output tokens")
	concurrency := flag.Int("concurrency", 30, "Max parallel polls")
	timeoutS := flag.Int("timeout", 600, "Per-task timeout in seconds")
	flag.Parse()

	prompts := make([]string, *numTasks)
	for i := range prompts {
		prompts[i] = overnightPrompts[i%len(overnightPrompts)]
	}

	startTime := time.Now()
	fmt.Printf("\n  Overnight Batch\n")
	fmt.Printf("    Hub:         %s\n", *hubURL)
	fmt.Printf("    Tasks:       %d\n", *numTasks)
	fmt.Printf("    Model:       %s\n", *model)
	fmt.Printf("    Concurrency: %d\n", *concurrency)
	fmt.Printf("    Started:     %s\n", startTime.Format("2006-01-02 15:04:05"))
	showOvernightAgents(*hubURL)
	fmt.Println()

	// Submit all tasks
	fmt.Printf("  Submitting %d tasks...\n", len(prompts))
	taskIDs := make([]string, 0, len(prompts))
	for i, prompt := range prompts {
		taskID, err := submitOvernightTask(*hubURL, prompt, *model, *maxTokens)
		if err != nil {
			fmt.Printf("    Submit failed at %d: %v\n", i+1, err)
			continue
		}
		taskIDs = append(taskIDs, taskID)
		if (i+1)%20 == 0 {
			fmt.Printf("    Submitted %d/%d...\n", i+1, len(prompts))
		}
	}
	fmt.Printf("  All %d tasks submitted. Waiting for completion...\n", len(taskIDs))
	fmt.Print("  (Progress shown every 10 completions)\n\n")

	// Poll all in parallel with semaphore
	sem := make(chan struct{}, *concurrency)
	var wg sync.WaitGroup
	results := make([]overnightResult, len(taskIDs))
	var mu sync.Mutex
	completedCount := 0

	for i, tid := range taskIDs {
		wg.Add(1)
		go func(idx int, taskID string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			r := pollOvernightTask(*hubURL, taskID, *timeoutS)
			results[idx] = r

			mu.Lock()
			completedCount++
			cnt := completedCount
			mu.Unlock()

			if cnt%10 == 0 || r.State != "COMPLETE" {
				ts := time.Now().Format("15:04:05")
				fmt.Printf("  [%s] %4d/%d  %-10s  %5.1fs  %5.0f tok\n",
					ts, cnt, len(taskIDs), r.State, r.LatencyS, r.OutputTokens)
			}
		}(i, tid)
	}
	wg.Wait()

	wallTime := time.Since(startTime).Seconds()

	// Summary
	var completed, failed, timedOut []overnightResult
	totalTokens := 0.0
	agentDist := make(map[string]int)
	for _, r := range results {
		switch r.State {
		case "COMPLETE":
			completed = append(completed, r)
			totalTokens += r.OutputTokens
			agentDist[r.AgentID]++
		case "FAILED":
			failed = append(failed, r)
		default:
			timedOut = append(timedOut, r)
		}
	}
	throughput := 0.0
	if wallTime > 0 {
		throughput = totalTokens / wallTime
	}

	fmt.Printf("\n  %s\n", strings.Repeat("=", 60))
	fmt.Printf("  OVERNIGHT BATCH COMPLETE\n")
	fmt.Printf("  Started:    %s\n", startTime.Format("2006-01-02 15:04:05"))
	fmt.Printf("  Finished:   %s\n", time.Now().Format("2006-01-02 15:04:05"))
	fmt.Printf("  Wall time:  %.2fh (%.0fs)\n", wallTime/3600, wallTime)
	fmt.Printf("  Completed:  %d/%d\n", len(completed), len(taskIDs))
	fmt.Printf("  Failed:     %d\n", len(failed))
	fmt.Printf("  Timed out:  %d\n", len(timedOut))
	fmt.Printf("  Tokens:     %.0f\n", totalTokens)
	fmt.Printf("  Throughput: %.1f tokens/s\n", throughput)
	if len(completed) > 0 {
		avgLat := 0.0
		for _, r := range completed {
			avgLat += r.LatencyS
		}
		avgLat /= float64(len(completed))
		fmt.Printf("  Avg latency:%.1fs\n", avgLat)
	}

	fmt.Printf("\n  Agent distribution:\n")
	for aid, cnt := range agentDist {
		pct := float64(cnt) / float64(max(len(completed), 1)) * 100
		bar := strings.Repeat("█", int(pct/5))
		fmt.Printf("    ..%-16s %4d tasks (%.0f%%)  %s\n",
			lastN(aid, 14), cnt, pct, bar)
	}
	fmt.Println()
	saveResults("overnight_batch", map[string]interface{}{
		"timestamp":    time.Now().Format(time.RFC3339),
		"hub":          *hubURL,
		"model":        *model,
		"num_tasks":    *numTasks,
		"wall_time_s":  wallTime,
		"completed":    len(completed),
		"failed":       len(failed),
		"total_tokens": totalTokens,
		"throughput":   throughput,
		"results":      results,
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
	fmt.Printf("  Results saved: %s\n", path)
}

func showOvernightAgents(hub string) {
	resp, err := overnightClient.Get(fmt.Sprintf("%s/agents", hub))
	if err != nil {
		return
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(resp.Body)
	var data map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &data); err != nil {
		return
	}
	agents, _ := data["agents"].([]interface{})
	online := 0
	for _, a := range agents {
		agent, ok := a.(map[string]interface{})
		if ok && str(agent, "status") == "ONLINE" {
			online++
		}
	}
	fmt.Printf("    Agents:      %d online\n", online)
}

func submitOvernightTask(hub, prompt, model string, maxTokens int) (string, error) {
	taskID := fmt.Sprintf("overnight-%s", uuid.New().String()[:8])
	payload := map[string]interface{}{
		"task_id":    taskID,
		"model":      model,
		"prompt":     prompt,
		"max_tokens": maxTokens,
	}
	body, _ := json.Marshal(payload)
	resp, err := overnightClient.Post(fmt.Sprintf("%s/tasks", hub), "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return taskID, nil
}

func pollOvernightTask(hub, taskID string, timeoutS int) overnightResult {
	t0 := time.Now()
	deadline := time.Now().Add(time.Duration(timeoutS) * time.Second)
	interval := 2 * time.Second

	for time.Now().Before(deadline) {
		getResp, err := overnightClient.Get(fmt.Sprintf("%s/tasks/%s", hub, taskID))
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
			return overnightResult{
				TaskID:       taskID,
				State:        "COMPLETE",
				AgentID:      str(res, "agent_id"),
				LatencyS:     elapsed,
				OutputTokens: outTok,
				TPS:          tps,
			}
		}
		if state == "FAILED" {
			return overnightResult{TaskID: taskID, State: "FAILED"}
		}

		time.Sleep(interval)
		if interval < 15*time.Second {
			interval = time.Duration(float64(interval) * 1.3)
		}
	}

	return overnightResult{TaskID: taskID, State: "TIMEOUT"}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
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
