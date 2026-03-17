// Recipe 27 — Model Health Check
//
// Measures per-model loading time and inference TPS on each agent node by
// sending two identical probe requests back-to-back per model:
//   request 1 (warmup)  → model loads into VRAM  → records load_time
//   request 2 (probe)   → model already hot       → records infer_time + TPS
//
// Usage:
//
//	go run cookbook/27_model_health/model_health.go
//	go run cookbook/27_model_health/model_health.go --hub http://192.168.0.177:9000
//	go run cookbook/27_model_health/model_health.go --interval 60   # repeat every 60 min
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
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

const probePrompt = "Reply with exactly: 'Model online.'"
const probeMaxTokens = 20

type healthResult struct {
	Model      string  `json:"model"`
	AgentID    string  `json:"agent_id"`
	AgentName  string  `json:"agent_name"`
	LoadTimeMs float64 `json:"load_time_ms"`
	InferMs    float64 `json:"infer_ms"`
	TPS        float64 `json:"tps"`
	Tokens     float64 `json:"tokens"`
	Error      string  `json:"error,omitempty"`
}

var hClient = &http.Client{Timeout: 30 * time.Second}

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
	timeoutS := flag.Int("timeout", 180, "Per-task timeout in seconds")
	intervalMin := flag.Int("interval", 0, "Repeat every N minutes (0 = run once)")
	flag.Parse()

	for {
		runHealthCheck(*hubURL, *timeoutS)
		if *intervalMin <= 0 {
			break
		}
		fmt.Printf("Next check in %d min...\n\n", *intervalMin)
		time.Sleep(time.Duration(*intervalMin) * time.Minute)
	}
}

func runHealthCheck(hubURL string, timeoutS int) {
	ts := time.Now()
	fmt.Printf("Model Health Check  —  %s\n", ts.Format("2006-01-02 15:04:05"))
	fmt.Printf("  Hub: %s\n\n", hubURL)

	// Get agent list for name lookup
	agentName := map[string]string{}
	if adata, err := getJSON(fmt.Sprintf("%s/agents", hubURL)); err == nil {
		for _, a := range items(adata, "agents") {
			id := str(a, "agent_id")
			name := str(a, "name")
			if name == "" {
				name = id[:min(len(id), 12)]
			}
			agentName[id] = name
		}
	}

	// Collect unique models across all agents
	models := collectModels(hubURL)
	if len(models) == 0 {
		fmt.Println("No agents online.")
		return
	}
	fmt.Printf("  Models to probe: %s\n\n", strings.Join(models, ", "))

	var results []healthResult

	for _, model := range models {
		fmt.Printf("  [%s]\n", model)

		// Embedding models cannot be probed via /tasks (no text generation)
		if isEmbeddingModel(model) {
			fmt.Printf("    skip — embedding model (no inference probe)\n\n")
			results = append(results, healthResult{
				Model: model, Error: "embedding model",
			})
			continue
		}

		// Request 1: warmup (loads model)
		fmt.Printf("    warmup... ")
		w := probe(hubURL, model, timeoutS)
		wName := agentName[w.AgentID]
		if w.Error != "" {
			fmt.Printf("ERROR %s\n", w.Error)
			results = append(results, healthResult{
				Model: model, AgentName: wName, AgentID: w.AgentID,
				Error: "warmup: " + w.Error,
			})
			fmt.Println()
			continue
		}
		fmt.Printf("ok  %.0fms (load)  agent=%s\n", w.LoadTimeMs, wName)

		// Request 2: inference (model already hot)
		fmt.Printf("    probe...  ")
		p := probe(hubURL, model, timeoutS)
		pName := agentName[p.AgentID]
		if p.Error != "" {
			fmt.Printf("ERROR %s\n", p.Error)
			results = append(results, healthResult{
				Model: model, AgentName: wName, AgentID: w.AgentID,
				LoadTimeMs: w.LoadTimeMs,
				Error:      "probe: " + p.Error,
			})
			fmt.Println()
			continue
		}

		sameAgent := p.AgentID == w.AgentID
		agentNote := ""
		if !sameAgent {
			agentNote = fmt.Sprintf(" (routed to %s)", pName)
		}
		fmt.Printf("ok  %.1f TPS  %.0fms%s\n", p.TPS, p.InferMs, agentNote)

		results = append(results, healthResult{
			Model:      model,
			AgentID:    w.AgentID,
			AgentName:  wName,
			LoadTimeMs: w.LoadTimeMs,
			InferMs:    p.InferMs,
			TPS:        p.TPS,
			Tokens:     p.Tokens,
		})
		fmt.Println()
	}

	printTable(results, agentName)
	saveResults("model_health", map[string]interface{}{
		"timestamp": ts.Format(time.RFC3339),
		"hub":       hubURL,
		"results":   results,
	})
}

// probe sends one probe request and returns timing info.
func probe(hubURL, model string, timeoutS int) healthResult {
	taskID := fmt.Sprintf("health-%s", uuid.New().String()[:8])
	t0 := time.Now()

	payload := map[string]interface{}{
		"task_id":    taskID,
		"model":      model,
		"prompt":     probePrompt,
		"max_tokens": probeMaxTokens,
	}
	body, _ := json.Marshal(payload)
	resp, err := hClient.Post(fmt.Sprintf("%s/tasks", hubURL), "application/json", bytes.NewReader(body))
	if err != nil {
		return healthResult{Model: model, Error: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return healthResult{Model: model, Error: fmt.Sprintf("HTTP %d", resp.StatusCode)}
	}

	deadline := time.Now().Add(time.Duration(timeoutS) * time.Second)
	interval := 2 * time.Second
	for time.Now().Before(deadline) {
		gr, err := hClient.Get(fmt.Sprintf("%s/tasks/%s", hubURL, taskID))
		if err != nil {
			time.Sleep(interval)
			continue
		}
		b, _ := io.ReadAll(gr.Body)
		gr.Body.Close()

		var task map[string]interface{}
		if json.Unmarshal(b, &task) != nil {
			time.Sleep(interval)
			continue
		}
		state := str(task, "state")
		if state == "COMPLETE" {
			res, _ := task["result"].(map[string]interface{})
			if res == nil {
				res = task
			}
			latMs := num(res, "latency_ms")
			outTok := num(res, "output_tokens")
			tps := 0.0
			if latMs > 0 {
				tps = outTok / (latMs / 1000)
			}
			return healthResult{
				Model:      model,
				AgentID:    str(res, "agent_id"),
				LoadTimeMs: float64(time.Since(t0).Milliseconds()),
				InferMs:    latMs,
				TPS:        tps,
				Tokens:     outTok,
			}
		}
		if state == "FAILED" {
			return healthResult{Model: model, Error: "task failed"}
		}
		time.Sleep(interval)
		if interval < 8*time.Second {
			interval = time.Duration(float64(interval) * 1.2)
		}
	}
	return healthResult{Model: model, Error: "timeout"}
}

func printTable(results []healthResult, agentName map[string]string) {
	// Group by agent
	byAgent := map[string][]healthResult{}
	var agentOrder []string
	seen := map[string]bool{}
	for _, r := range results {
		key := r.AgentName
		if key == "" {
			key = r.AgentID
		}
		if !seen[key] {
			agentOrder = append(agentOrder, key)
			seen[key] = true
		}
		byAgent[key] = append(byAgent[key], r)
	}
	sort.Strings(agentOrder)

	sep := strings.Repeat("─", 74)
	fmt.Printf("\n%s\n", sep)
	fmt.Printf("%-20s  %-22s  %9s  %9s  %7s  %s\n",
		"AGENT", "MODEL", "LOAD_TIME", "INFER_MS", "TPS", "STATUS")
	fmt.Printf("%s\n", sep)

	for _, agent := range agentOrder {
		for _, r := range byAgent[agent] {
			status := "OK"
			if r.Error != "" {
				status = "ERROR"
			}
			if r.Error == "embedding model" {
				fmt.Printf("%-20s  %-22s  %9s  %9s  %7s  %s\n",
					agent, r.Model, "—", "—", "—", "EMBED")
			} else if r.Error != "" {
				fmt.Printf("%-20s  %-22s  %9s  %9s  %7s  %s\n",
					agent, r.Model, "—", "—", "—", "ERROR: "+r.Error)
			} else {
				fmt.Printf("%-20s  %-22s  %8.0fms  %8.0fms  %6.1f  %s\n",
					agent, r.Model,
					r.LoadTimeMs, r.InferMs, r.TPS, status)
			}
		}
	}
	fmt.Printf("%s\n\n", sep)
}

// collectModels returns unique models advertised by online agents.
func collectModels(hubURL string) []string {
	data, err := getJSON(fmt.Sprintf("%s/agents", hubURL))
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var models []string
	for _, a := range items(data, "agents") {
		if str(a, "status") != "ONLINE" {
			continue
		}
		var modelList []string
		if modelsStr, ok := a["supported_models"].(string); ok {
			json.Unmarshal([]byte(modelsStr), &modelList)
		}
		for _, s := range modelList {
			if s != "" && !seen[s] {
				seen[s] = true
				models = append(models, s)
			}
		}
	}
	sort.Strings(models)
	return models
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

func getJSON(url string) (map[string]interface{}, error) {
	resp, err := hClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result, nil
}

func items(m map[string]interface{}, key string) []map[string]interface{} {
	v, ok := m[key]
	if !ok {
		return nil
	}
	arr, ok := v.([]interface{})
	if !ok {
		return nil
	}
	result := make([]map[string]interface{}, 0, len(arr))
	for _, item := range arr {
		if m, ok := item.(map[string]interface{}); ok {
			result = append(result, m)
		}
	}
	return result
}

func str(m map[string]interface{}, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
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

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// isEmbeddingModel returns true for models that produce embeddings, not text.
func isEmbeddingModel(model string) bool {
	lower := strings.ToLower(model)
	for _, pat := range []string{"embed", "bge-", "bge_"} {
		if strings.Contains(lower, pat) {
			return true
		}
	}
	return false
}
