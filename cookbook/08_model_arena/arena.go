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

const defaultArenaPrompt = "Explain the concept of information entropy (Shannon entropy) " +
	"and its relationship to thermodynamic entropy. " +
	"Include one concrete example. Keep it under 200 words."

var arenaClient = &http.Client{Timeout: 30 * time.Second}

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

type arenaResult struct {
	Model        string  `json:"model"`
	State        string  `json:"state"`
	Content      string  `json:"content"`
	OutputTokens float64 `json:"output_tokens"`
	LatencyS     float64 `json:"latency_s"`
	TPS          float64 `json:"tps"`
	AgentID      string  `json:"agent_id"`
	Error        string  `json:"error"`
}

func main() {
	hubURL := flag.String("hub", defaultHubURL(), "Hub URL")
	models := flag.String("models", "llama3,mistral,phi3", "Comma-separated model names")
	prompt := flag.String("prompt", defaultArenaPrompt, "Prompt to send to all models")
	maxTokens := flag.Int("max-tokens", 512, "Max output tokens")
	timeoutS := flag.Int("timeout", 300, "Timeout in seconds")
	flag.Parse()

	modelList := strings.Split(*models, ",")
	for i, m := range modelList {
		modelList[i] = strings.TrimSpace(m)
	}

	fmt.Printf("\n  Model Arena\n")
	fmt.Printf("    Hub:    %s\n", *hubURL)
	fmt.Printf("    Models: %v\n\n", modelList)

	var wg sync.WaitGroup
	ch := make(chan arenaResult, len(modelList))

	for _, m := range modelList {
		wg.Add(1)
		go func(model string) {
			defer wg.Done()
			fmt.Printf("    [%s] submitting...\n", model)
			r := runArena(*hubURL, model, *prompt, *maxTokens, *timeoutS)
			ch <- r
			if r.State == "COMPLETE" {
				fmt.Printf("    [%s] %.0f tok  %.1fs  %.1f tps\n",
					model, r.OutputTokens, r.LatencyS, r.TPS)
			} else {
				fmt.Printf("    [%s] %s: %s\n", model, r.State, r.Error)
			}
		}(m)
	}

	go func() {
		wg.Wait()
		close(ch)
	}()

	var results []arenaResult
	for r := range ch {
		results = append(results, r)
	}

	// Order by model list
	ordered := make([]arenaResult, 0, len(modelList))
	for _, m := range modelList {
		for _, r := range results {
			if r.Model == m {
				ordered = append(ordered, r)
				break
			}
		}
	}

	fmt.Printf("\n  %-15s %-10s %8s %10s %8s\n", "MODEL", "STATE", "TOKENS", "LATENCY", "TPS")
	fmt.Printf("  %s\n", strings.Repeat("-", 55))
	for _, r := range ordered {
		fmt.Printf("  %-15s %-10s %8.0f %9.1fs %7.1f\n",
			r.Model, r.State, r.OutputTokens, r.LatencyS, r.TPS)
	}
	fmt.Println()
	saveResults("arena", map[string]interface{}{
		"timestamp": time.Now().Format(time.RFC3339),
		"hub":       *hubURL,
		"models":    modelList,
		"prompt":    *prompt,
		"results":   ordered,
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

func runArena(hub, model, prompt string, maxTokens, timeoutS int) arenaResult {
	taskID := fmt.Sprintf("arena-%s-%s", model, uuid.New().String()[:6])
	t0 := time.Now()

	payload := map[string]interface{}{
		"task_id":    taskID,
		"model":      model,
		"prompt":     prompt,
		"max_tokens": maxTokens,
	}
	body, _ := json.Marshal(payload)
	resp, err := arenaClient.Post(fmt.Sprintf("%s/tasks", hub), "application/json", bytes.NewReader(body))
	if err != nil {
		return arenaResult{Model: model, State: "SUBMIT_FAILED", Error: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return arenaResult{Model: model, State: "SUBMIT_REJECTED", Error: fmt.Sprintf("HTTP %d", resp.StatusCode)}
	}

	deadline := time.Now().Add(time.Duration(timeoutS) * time.Second)
	interval := 2 * time.Second

	for time.Now().Before(deadline) {
		getResp, err := arenaClient.Get(fmt.Sprintf("%s/tasks/%s", hub, taskID))
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
			return arenaResult{
				Model:        model,
				State:        "COMPLETE",
				Content:      str(res, "content"),
				OutputTokens: outTok,
				LatencyS:     elapsed,
				TPS:          tps,
				AgentID:      str(res, "agent_id"),
			}
		}
		if state == "FAILED" {
			res, _ := task["result"].(map[string]interface{})
			errMsg := ""
			if res != nil {
				errMsg = str(res, "error")
			}
			return arenaResult{Model: model, State: "FAILED", Error: errMsg}
		}

		time.Sleep(interval)
		if interval < 10*time.Second {
			interval = time.Duration(float64(interval) * 1.3)
		}
	}

	return arenaResult{Model: model, State: "TIMEOUT"}
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
