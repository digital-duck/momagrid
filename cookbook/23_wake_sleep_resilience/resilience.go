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

var resiliencePrompts = []string{
	"What is resilience in distributed systems?",
	"Name the planets in order from the sun.",
	"What is 17 * 23?",
	"Explain what a load balancer does.",
	"What is the speed of light?",
	"What is a hash function?",
	"Explain the concept of idempotency.",
	"What is latency in networking?",
}

type resResult struct {
	State        string  `json:"state"`
	AgentID      string  `json:"agent_id"`
	LatencyS     float64 `json:"latency_s"`
	OutputTokens float64 `json:"output_tokens"`
	Error        string  `json:"error"`
}

type agentEvent struct {
	TS    string `json:"ts"`
	Event string `json:"event"`
	Agent string `json:"agent"`
}

var resClient = &http.Client{Timeout: 30 * time.Second}

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
	durationS := flag.Int("duration", 300, "Test duration in seconds")
	model := flag.String("model", "llama3", "Model name")
	maxTokens := flag.Int("max-tokens", 64, "Max output tokens")
	intervalS := flag.Float64("interval", 3.0, "Seconds between task submissions")
	timeoutS := flag.Int("timeout", 120, "Per-task timeout in seconds")
	flag.Parse()

	fmt.Printf("\n  Wake/Sleep Resilience Test\n")
	fmt.Printf("    Hub:      %s\n", *hubURL)
	fmt.Printf("    Duration: %ds\n", *durationS)
	fmt.Printf("    Model:    %s\n", *model)
	fmt.Printf("    Interval: %.1fs between tasks\n\n", *intervalS)
	fmt.Println("  Continuously submitting tasks. Bring agents online/offline to test.")
	fmt.Println("  (`moma join` on another machine, or `moma down` to stop an agent)")
	fmt.Println()

	var mu sync.Mutex
	var agentLog []agentEvent
	var results []resResult
	taskCount := 0
	wallStart := time.Now()

	// Start agent watcher
	stopWatcher := make(chan struct{})
	prevAgents := make(map[string]string) // agent_id -> status
	go func() {
		for {
			select {
			case <-stopWatcher:
				return
			case <-time.After(5 * time.Second):
				watchAgents(*hubURL, prevAgents, &agentLog, &mu)
			}
		}
	}()

	// Submit tasks with interval
	deadline := time.Now().Add(time.Duration(*durationS) * time.Second)
	var pendingWg sync.WaitGroup
	var pendingMu sync.Mutex
	var pendingResults []resResult

	for time.Now().Before(deadline) {
		prompt := resiliencePrompts[taskCount%len(resiliencePrompts)]
		ts := time.Now().Format("15:04:05")
		remaining := int(deadline.Sub(time.Now()).Seconds())
		fmt.Printf("  [%s] Task %4d submitted  (%ds remaining)\n", ts, taskCount+1, remaining)
		taskCount++

		pendingWg.Add(1)
		go func(p string) {
			defer pendingWg.Done()
			r := resSubmitAndWait(*hubURL, p, *model, *maxTokens, *timeoutS)
			state := r.State
			agent := lastN(r.AgentID, 12)
			if agent == "" {
				agent = "?"
			}
			fmt.Printf("  -> %s  ..%s  %.1fs\n", state, agent, r.LatencyS)
			pendingMu.Lock()
			pendingResults = append(pendingResults, r)
			pendingMu.Unlock()
		}(prompt)

		time.Sleep(time.Duration(*intervalS * float64(time.Second)))
	}

	fmt.Printf("\n  Waiting for in-flight tasks...\n")
	pendingWg.Wait()
	close(stopWatcher)

	mu.Lock()
	results = append(results, pendingResults...)
	mu.Unlock()

	wallTime := time.Since(wallStart).Seconds()

	var completed, failed []resResult
	agentDist := make(map[string]int)
	for _, r := range results {
		if r.State == "COMPLETE" {
			completed = append(completed, r)
			agentDist[r.AgentID]++
		} else {
			failed = append(failed, r)
		}
	}

	fmt.Printf("\n  %s\n", strings.Repeat("=", 60))
	fmt.Printf("  Resilience Test Complete\n")
	fmt.Printf("  Duration:  %.0fs\n", wallTime)
	fmt.Printf("  Tasks:     %d submitted\n", len(results))
	fmt.Printf("  Completed: %d\n", len(completed))
	fmt.Printf("  Failed:    %d\n", len(failed))
	pct := float64(len(completed)) / float64(max(len(results), 1)) * 100
	fmt.Printf("  Success:   %.1f%%\n", pct)

	mu.Lock()
	if len(agentLog) > 0 {
		fmt.Printf("\n  Agent events during test:\n")
		for _, e := range agentLog {
			fmt.Printf("    [%s] %-10s %s\n", e.TS, e.Event, e.Agent)
		}
	}
	mu.Unlock()

	fmt.Printf("\n  Task distribution across agents:\n")
	for aid, cnt := range agentDist {
		bar := strings.Repeat("█", cnt)
		fmt.Printf("    ..%-16s %4d tasks  %s\n", lastN(aid, 14), cnt, bar)
	}

	mu.Lock()
	evts := len(agentLog)
	mu.Unlock()
	if len(completed) == len(results) {
		fmt.Printf("\n  PASS: 100%% tasks completed despite %d agent event(s).\n", evts)
	} else if pct >= 95 {
		fmt.Printf("\n  PASS: >=95%% tasks completed. Grid proved resilient.\n")
	} else {
		fmt.Printf("\n  PARTIAL: %d/%d tasks completed.\n", len(completed), len(results))
	}
	fmt.Println()

	mu.Lock()
	savedLog := agentLog
	mu.Unlock()
	saveResults("resilience", map[string]interface{}{
		"timestamp":   time.Now().Format(time.RFC3339),
		"hub":         *hubURL,
		"model":       *model,
		"duration_s":  *durationS,
		"wall_time_s": wallTime,
		"results":     results,
		"agent_events": savedLog,
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

func watchAgents(hub string, prev map[string]string, log *[]agentEvent, mu *sync.Mutex) {
	resp, err := resClient.Get(fmt.Sprintf("%s/agents", hub))
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

	mu.Lock()
	defer mu.Unlock()

	for _, a := range agents {
		agent, ok := a.(map[string]interface{})
		if !ok {
			continue
		}
		aid := str(agent, "agent_id")
		status := str(agent, "status")
		name := str(agent, "name")
		if name == "" {
			name = lastN(aid, 12)
		}

		prevStatus, seen := prev[aid]
		if !seen {
			ts := time.Now().Format("15:04:05")
			fmt.Printf("\n  +  [%s] NEW AGENT: %s (%s)\n", ts, name, status)
			*log = append(*log, agentEvent{ts, "JOINED", name})
			prev[aid] = status
		} else if prevStatus != status {
			ts := time.Now().Format("15:04:05")
			if status == "OFFLINE" {
				fmt.Printf("\n  !  [%s] AGENT OFFLINE: %s — tasks will re-queue\n", ts, name)
				*log = append(*log, agentEvent{ts, "OFFLINE", name})
			} else if status == "ONLINE" && prevStatus == "OFFLINE" {
				fmt.Printf("\n  OK [%s] AGENT ONLINE: %s\n", ts, name)
				*log = append(*log, agentEvent{ts, "ONLINE", name})
			}
			prev[aid] = status
		}
	}
}

func resSubmitAndWait(hub, prompt, model string, maxTokens, timeoutS int) resResult {
	taskID := fmt.Sprintf("resilience-%s", uuid.New().String()[:8])
	t0 := time.Now()

	payload := map[string]interface{}{
		"task_id":    taskID,
		"model":      model,
		"prompt":     prompt,
		"max_tokens": maxTokens,
	}
	body, _ := json.Marshal(payload)
	resp, err := resClient.Post(fmt.Sprintf("%s/tasks", hub), "application/json", bytes.NewReader(body))
	if err != nil {
		return resResult{State: "SUBMIT_FAILED", Error: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return resResult{State: "SUBMIT_REJECTED"}
	}

	deadline := time.Now().Add(time.Duration(timeoutS) * time.Second)
	interval := 1500 * time.Millisecond

	for time.Now().Before(deadline) {
		getResp, err := resClient.Get(fmt.Sprintf("%s/tasks/%s", hub, taskID))
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
			return resResult{
				State:        "COMPLETE",
				AgentID:      str(res, "agent_id"),
				LatencyS:     time.Since(t0).Seconds(),
				OutputTokens: num(res, "output_tokens"),
			}
		}
		if state == "FAILED" {
			return resResult{State: "FAILED", LatencyS: time.Since(t0).Seconds()}
		}

		time.Sleep(interval)
		if interval < 8*time.Second {
			interval = time.Duration(float64(interval) * 1.2)
		}
	}

	return resResult{State: "TIMEOUT", LatencyS: float64(timeoutS)}
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
