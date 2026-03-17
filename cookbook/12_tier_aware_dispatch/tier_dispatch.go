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

type tierTask struct {
	Label      string
	Model      string
	MinVRAMGB  float64
	Prompt     string
}

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

var defaultTasks = []tierTask{
	{"No VRAM constraint", "llama3", 0, "Name the planets in our solar system."},
	{"VRAM >= 4 GB", "llama3", 4, "What is photosynthesis?"},
	{"VRAM >= 8 GB", "llama3", 8, "Explain gradient descent in one paragraph."},
	{"VRAM >= 10 GB", "llama3", 10, "What is a transformer architecture?"},
}

type tierResult struct {
	Label        string  `json:"label"`
	Model        string  `json:"model"`
	MinVRAMGB    float64 `json:"min_vram_gb"`
	State        string  `json:"state"`
	AgentID      string  `json:"agent_id"`
	LatencyS     float64 `json:"latency_s"`
	OutputTokens float64 `json:"output_tokens"`
	TPS          float64 `json:"tps"`
	Error        string  `json:"error"`
}

var tierClient = &http.Client{Timeout: 30 * time.Second}

func main() {
	hubURL := flag.String("hub", defaultHubURL(), "Hub URL")
	maxTokens := flag.Int("max-tokens", 128, "Max output tokens")
	timeoutS := flag.Int("timeout", 120, "Timeout in seconds")
	vram := flag.Float64("vram", -1, "Run a single custom task with this VRAM requirement (GB)")
	model := flag.String("model", "llama3", "Model for --vram custom task")
	flag.Parse()

	tasks := defaultTasks
	if *vram >= 0 {
		tasks = []tierTask{{
			Label:     fmt.Sprintf("Custom VRAM >= %.0f GB", *vram),
			Model:     *model,
			MinVRAMGB: *vram,
			Prompt:    "Explain distributed AI inference in two sentences.",
		}}
	}

	fmt.Printf("\n  Tier-Aware Dispatch\n")
	fmt.Printf("    Hub:   %s\n", *hubURL)
	fmt.Printf("    Tasks: %d\n\n", len(tasks))

	// Show available agents
	showAgents(*hubURL)

	var wg sync.WaitGroup
	ch := make(chan tierResult, len(tasks))

	for _, t := range tasks {
		wg.Add(1)
		go func(task tierTask) {
			defer wg.Done()
			ch <- dispatchTierTask(*hubURL, task, *maxTokens, *timeoutS)
		}(t)
	}

	go func() {
		wg.Wait()
		close(ch)
	}()

	// Collect in order
	resultMap := make(map[string]tierResult)
	for r := range ch {
		resultMap[r.Label] = r
	}
	ordered := make([]tierResult, 0, len(tasks))
	for _, t := range tasks {
		ordered = append(ordered, resultMap[t.Label])
	}

	fmt.Printf("  %-30s %6s  %-10s %20s  %6s  %5s\n",
		"Label", "VRAM", "State", "Agent", "Lat", "Tok")
	fmt.Printf("  %s\n", strings.Repeat("-", 85))
	for _, r := range ordered {
		agentShort := "-"
		if r.AgentID != "" {
			agentShort = ".." + lastN(r.AgentID, 14)
		}
		fmt.Printf("  %-30s %5.0fG  %-10s %20s  %5.1fs  %5.0f\n",
			r.Label, r.MinVRAMGB, r.State, agentShort, r.LatencyS, r.OutputTokens)
	}

	passed := 0
	var failedItems []tierResult
	for _, r := range ordered {
		if r.State == "COMPLETE" {
			passed++
		} else {
			failedItems = append(failedItems, r)
		}
	}
	fmt.Printf("\n  %d/%d dispatched successfully\n", passed, len(tasks))
	for _, r := range failedItems {
		msg := r.Error
		if msg == "" {
			msg = r.State
		}
		fmt.Printf("  FAILED: %s — %s\n", r.Label, msg)
	}
	fmt.Println()
	saveResults("tier_dispatch", map[string]interface{}{
		"timestamp": time.Now().Format(time.RFC3339),
		"hub":       *hubURL,
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

func showAgents(hub string) {
	resp, err := tierClient.Get(fmt.Sprintf("%s/agents", hub))
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
	if len(agents) == 0 {
		return
	}
	fmt.Println("  Available agents:")
	for _, a := range agents {
		agent, ok := a.(map[string]interface{})
		if !ok {
			continue
		}
		name := str(agent, "name")
		tier := str(agent, "tier")
		status := str(agent, "status")
		// Parse VRAM from gpus field
		vram := 0.0
		gpusRaw := agent["gpus"]
		switch g := gpusRaw.(type) {
		case string:
			var gpus []map[string]interface{}
			if json.Unmarshal([]byte(g), &gpus) == nil && len(gpus) > 0 {
				vram = num(gpus[0], "vram_gb")
			}
		case []interface{}:
			if len(g) > 0 {
				if gm, ok := g[0].(map[string]interface{}); ok {
					vram = num(gm, "vram_gb")
				}
			}
		}
		fmt.Printf("    %-20s tier=%-10s vram=%.0fGB  status=%s\n",
			name, tier, vram, status)
	}
	fmt.Println()
}

func dispatchTierTask(hub string, task tierTask, maxTokens, timeoutS int) tierResult {
	taskID := fmt.Sprintf("tier-%s", uuid.New().String()[:8])
	t0 := time.Now()

	payload := map[string]interface{}{
		"task_id":     taskID,
		"model":       task.Model,
		"prompt":      task.Prompt,
		"max_tokens":  maxTokens,
		"min_vram_gb": task.MinVRAMGB,
	}
	body, _ := json.Marshal(payload)
	resp, err := tierClient.Post(fmt.Sprintf("%s/tasks", hub), "application/json", bytes.NewReader(body))
	if err != nil {
		return tierResult{Label: task.Label, Model: task.Model, MinVRAMGB: task.MinVRAMGB, State: "SUBMIT_FAILED", Error: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return tierResult{Label: task.Label, Model: task.Model, MinVRAMGB: task.MinVRAMGB, State: "SUBMIT_REJECTED", Error: fmt.Sprintf("HTTP %d", resp.StatusCode)}
	}

	deadline := time.Now().Add(time.Duration(timeoutS) * time.Second)
	interval := 1500 * time.Millisecond

	for time.Now().Before(deadline) {
		getResp, err := tierClient.Get(fmt.Sprintf("%s/tasks/%s", hub, taskID))
		if err != nil {
			time.Sleep(interval)
			continue
		}
		bodyBytes, _ := io.ReadAll(getResp.Body)
		getResp.Body.Close()

		var taskData map[string]interface{}
		if err := json.Unmarshal(bodyBytes, &taskData); err != nil {
			time.Sleep(interval)
			continue
		}

		state := str(taskData, "state")
		if state == "COMPLETE" {
			res, _ := taskData["result"].(map[string]interface{})
			if res == nil {
				res = taskData
			}
			elapsed := time.Since(t0).Seconds()
			outTok := num(res, "output_tokens")
			tps := 0.0
			if elapsed > 0 {
				tps = outTok / elapsed
			}
			return tierResult{
				Label:        task.Label,
				Model:        task.Model,
				MinVRAMGB:    task.MinVRAMGB,
				State:        "COMPLETE",
				AgentID:      str(res, "agent_id"),
				LatencyS:     elapsed,
				OutputTokens: outTok,
				TPS:          tps,
			}
		}
		if state == "FAILED" {
			res, _ := taskData["result"].(map[string]interface{})
			errMsg := ""
			if res != nil {
				errMsg = str(res, "error")
			}
			return tierResult{Label: task.Label, Model: task.Model, MinVRAMGB: task.MinVRAMGB, State: "FAILED", Error: errMsg}
		}

		time.Sleep(interval)
		if interval < 8*time.Second {
			interval = time.Duration(float64(interval) * 1.2)
		}
	}

	return tierResult{Label: task.Label, Model: task.Model, MinVRAMGB: task.MinVRAMGB, State: "TIMEOUT"}
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
