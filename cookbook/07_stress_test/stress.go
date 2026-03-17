package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

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

type stressResult struct {
	TaskID       string  `json:"task_id"`
	State        string  `json:"state"`
	Prompt       string  `json:"prompt"`
	AgentID      string  `json:"agent_id"`
	OutputTokens float64 `json:"output_tokens"`
	LatencyMs    float64 `json:"latency_ms"`
	TPS          float64 `json:"tps"`
	Error        string  `json:"error,omitempty"`
}

func main() {
	hubURL := flag.String("hub", defaultHubURL(), "Hub URL")
	numTasks := flag.Int("n", 20, "Number of tasks to run")
	concurrency := flag.Int("c", 5, "Max concurrent tasks")
	model := flag.String("model", "llama3", "Model name")
	maxTokens := flag.Int("max-tokens", 1024, "Max output tokens")
	timeoutS := flag.Int("timeout", 180, "Timeout in seconds")
	flag.Parse()

	prompts := []string{
		"Explain the concept of entropy in thermodynamics.",
		"Write a short story about a robot who discovers music.",
		"What are the primary differences between TCP and UDP?",
		"Summarize the plot of Hamlet in three sentences.",
		"How do neural networks learn from data?",
		"Explain the prisoner's dilemma in game theory.",
		"What is the significance of the Turing test?",
		"Describe the lifecycle of a star.",
		"How does photosynthesis work at a molecular level?",
		"What are the key features of the Go programming language?",
	}

	fmt.Printf("Starting stress test: %d tasks, concurrency=%d, model=%s\n", *numTasks, *concurrency, *model)

	var wg sync.WaitGroup
	resultsChan := make(chan stressResult, *numTasks)
	sem := make(chan struct{}, *concurrency)

	start := time.Now()

	for i := 0; i < *numTasks; i++ {
		wg.Add(1)
		sem <- struct{}{} // acquire
		prompt := prompts[i%len(prompts)]
		go func(p string, idx int) {
			defer wg.Done()
			defer func() { <-sem }() // release
			resultsChan <- submitAndPoll(*hubURL, p, *model, *maxTokens, *timeoutS)
		}(prompt, i)
	}

	go func() {
		wg.Wait()
		close(resultsChan)
	}()

	var results []stressResult
	completed := 0
	totalTokens := 0.0

	for r := range resultsChan {
		results = append(results, r)
		if r.State == "COMPLETE" {
			completed++
			totalTokens += r.OutputTokens
			fmt.Printf("  [%d/%d] COMPLETE  agent=..%s  %.1f tps\n",
				len(results), *numTasks, lastN(r.AgentID, 8), r.TPS)
		} else {
			fmt.Printf("  [%d/%d] %s: %s\n", len(results), *numTasks, r.State, r.Error)
		}
	}

	wallTime := time.Since(start).Seconds()
	fmt.Printf("\n--- Stress Test Summary ---\n")
	fmt.Printf("Tasks:       %d / %d completed\n", completed, *numTasks)
	fmt.Printf("Wall time:   %.2fs\n", wallTime)
	fmt.Printf("Total tokens: %.0f\n", totalTokens)
	if wallTime > 0 {
		fmt.Printf("Throughput:  %.1f tokens/sec (total grid capacity)\n", totalTokens/wallTime)
	}
	saveResults("stress", map[string]interface{}{
		"timestamp":    time.Now().Format(time.RFC3339),
		"hub":          *hubURL,
		"model":        *model,
		"num_tasks":    *numTasks,
		"concurrency":  *concurrency,
		"completed":    completed,
		"wall_time_s":  wallTime,
		"total_tokens": totalTokens,
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
	fmt.Printf("Results saved: %s\n", path)
}

func submitAndPoll(hub, prompt, model string, maxTokens, timeoutS int) stressResult {
	taskID := uuid.New().String()
	u := fmt.Sprintf("%s/tasks", hub)

	payload := map[string]interface{}{
		"task_id":    taskID,
		"model":      model,
		"prompt":     prompt,
		"max_tokens": maxTokens,
	}

	data, _ := json.Marshal(payload)
	_, err := http.Post(u, "application/json", bytes.NewReader(data))
	if err != nil {
		return stressResult{State: "SUBMIT_FAILED", Error: err.Error()}
	}

	deadline := time.Now().Add(time.Duration(timeoutS) * time.Second)
	interval := 2 * time.Second
	t0 := time.Now()

	for time.Now().Before(deadline) {
		resp, err := http.Get(fmt.Sprintf("%s/tasks/%s", hub, taskID))
		if err != nil {
			time.Sleep(interval)
			continue
		}
		defer resp.Body.Close()

		var task map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&task)

		state, _ := task["state"].(string)
		if state == "COMPLETE" {
			res, _ := task["result"].(map[string]interface{})
			if res == nil {
				res = task
			}
			elapsed := time.Since(t0).Seconds()
			tokens := num(res, "output_tokens")
			return stressResult{
				TaskID:       taskID,
				State:        "COMPLETE",
				Prompt:       prompt,
				AgentID:      str(res, "agent_id"),
				OutputTokens: tokens,
				LatencyMs:    num(res, "latency_ms"),
				TPS:          tokens / elapsed,
			}
		}
		if state == "FAILED" {
			return stressResult{State: "FAILED"}
		}
		time.Sleep(interval)
	}

	return stressResult{State: "TIMEOUT"}
}

func str(m map[string]interface{}, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
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
	case float32:
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
