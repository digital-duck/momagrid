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

var failoverPrompts = []string{
	"Explain the concept of fault tolerance.",
	"What is load balancing in distributed systems?",
	"Describe the CAP theorem.",
	"What is a circuit breaker pattern?",
	"Explain eventual consistency.",
	"What is a heartbeat in distributed systems?",
	"Describe leader election.",
	"What is consensus in distributed computing?",
	"Explain the two-phase commit protocol.",
	"What is a distributed hash table?",
}

type failoverResult struct {
	TaskID       string  `json:"task_id"`
	State        string  `json:"state"`
	AgentID      string  `json:"agent_id"`
	LatencyS     float64 `json:"latency_s"`
	OutputTokens float64 `json:"output_tokens"`
	Error        string  `json:"error"`
}

var failoverClient = &http.Client{Timeout: 30 * time.Second}

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
	numTasks := flag.Int("n", 20, "Number of tasks to submit")
	model := flag.String("model", "llama3", "Model name")
	maxTokens := flag.Int("max-tokens", 64, "Max output tokens")
	timeoutS := flag.Int("timeout", 300, "Timeout in seconds")
	submitDelay := flag.Float64("submit-delay", 1.0, "Seconds between submissions")
	flag.Parse()

	fmt.Printf("\n  Agent Failover Test\n")
	fmt.Printf("    Hub:    %s\n", *hubURL)
	fmt.Printf("    Tasks:  %d\n", *numTasks)
	fmt.Printf("    Model:  %s\n\n", *model)

	// Show initial agents
	showFailoverAgents(*hubURL)

	fmt.Println("  Submitting tasks... Kill an agent mid-run to test failover.")
	fmt.Println("  (Use `moma down` on the agent machine, or stop the agent process)")
	fmt.Println()

	n := *numTasks
	taskIDs := make([]string, 0, n)
	prompts := make([]string, n)
	for i := range prompts {
		prompts[i] = failoverPrompts[i%len(failoverPrompts)]
	}

	// Submit tasks with delay
	for i, prompt := range prompts {
		taskID, err := submitFailoverTask(*hubURL, prompt, *model, *maxTokens)
		if err != nil {
			fmt.Printf("  Submit failed at %d: %v\n", i+1, err)
			continue
		}
		taskIDs = append(taskIDs, taskID)
		fmt.Printf("  Submitted [%3d/%d] %s\n", i+1, n, taskID)
		if *submitDelay > 0 && i < n-1 {
			time.Sleep(time.Duration(*submitDelay * float64(time.Second)))
		}
	}

	fmt.Printf("\n  All %d tasks submitted. Waiting for completion...\n\n", len(taskIDs))

	// Poll all tasks concurrently
	var wg sync.WaitGroup
	results := make([]failoverResult, len(taskIDs))
	for i, tid := range taskIDs {
		wg.Add(1)
		go func(idx int, taskID string) {
			defer wg.Done()
			results[idx] = pollFailoverTask(*hubURL, taskID, *timeoutS)
		}(i, tid)
	}
	wg.Wait()

	// Summary
	var completed, failed, timedOut []failoverResult
	agentDist := make(map[string]int)
	for _, r := range results {
		switch r.State {
		case "COMPLETE":
			completed = append(completed, r)
			agentDist[r.AgentID]++
		case "FAILED":
			failed = append(failed, r)
		default:
			timedOut = append(timedOut, r)
		}
	}

	fmt.Printf("  %s\n", strings.Repeat("=", 60))
	fmt.Printf("  Completed: %d/%d\n", len(completed), len(taskIDs))
	fmt.Printf("  Failed:    %d\n", len(failed))
	fmt.Printf("  Timed out: %d\n", len(timedOut))
	fmt.Printf("\n  Agent distribution (post-failover):\n")
	for aid, cnt := range agentDist {
		fmt.Printf("    ..%-16s %3d tasks\n", lastN(aid, 14), cnt)
	}

	if len(completed) == len(taskIDs) {
		fmt.Printf("\n  PASS: All %d tasks completed despite any failover events.\n", len(taskIDs))
	} else {
		pct := float64(len(completed)) / float64(max(len(taskIDs), 1)) * 100
		if pct >= 95 {
			fmt.Printf("\n  PASS: %.0f%% tasks completed (>=95%%). Grid proved resilient.\n", pct)
		} else {
			fmt.Printf("\n  PARTIAL: %d/%d tasks completed.\n", len(completed), len(taskIDs))
		}
	}
	fmt.Println()
	saveResults("failover", map[string]interface{}{
		"timestamp": time.Now().Format(time.RFC3339),
		"hub":       *hubURL,
		"model":     *model,
		"num_tasks": *numTasks,
		"results":   results,
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

func showFailoverAgents(hub string) {
	resp, err := failoverClient.Get(fmt.Sprintf("%s/agents", hub))
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
	fmt.Printf("  Initial agents (%d online):\n", len(agents))
	for _, a := range agents {
		agent, ok := a.(map[string]interface{})
		if !ok {
			continue
		}
		fmt.Printf("    - %-20s %-10s %s\n",
			str(agent, "name"), str(agent, "tier"), str(agent, "status"))
	}
	fmt.Println()
}

func submitFailoverTask(hub, prompt, model string, maxTokens int) (string, error) {
	taskID := fmt.Sprintf("failover-%s", uuid.New().String()[:8])
	payload := map[string]interface{}{
		"task_id":    taskID,
		"model":      model,
		"prompt":     prompt,
		"max_tokens": maxTokens,
	}
	body, _ := json.Marshal(payload)
	resp, err := failoverClient.Post(fmt.Sprintf("%s/tasks", hub), "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return taskID, nil
}

func pollFailoverTask(hub, taskID string, timeoutS int) failoverResult {
	t0 := time.Now()
	deadline := time.Now().Add(time.Duration(timeoutS) * time.Second)
	interval := 1500 * time.Millisecond

	for time.Now().Before(deadline) {
		getResp, err := failoverClient.Get(fmt.Sprintf("%s/tasks/%s", hub, taskID))
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
			return failoverResult{
				TaskID:       taskID,
				State:        "COMPLETE",
				AgentID:      str(res, "agent_id"),
				LatencyS:     time.Since(t0).Seconds(),
				OutputTokens: num(res, "output_tokens"),
			}
		}
		if state == "FAILED" {
			res, _ := task["result"].(map[string]interface{})
			errMsg := ""
			if res != nil {
				errMsg = str(res, "error")
			}
			return failoverResult{TaskID: taskID, State: "FAILED", Error: errMsg}
		}

		time.Sleep(interval)
		if interval < 8*time.Second {
			interval = time.Duration(float64(interval) * 1.2)
		}
	}

	return failoverResult{TaskID: taskID, State: "TIMEOUT"}
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
