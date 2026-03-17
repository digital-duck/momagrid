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
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

type step struct {
	Name   string
	System string
	Prompt string
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

var steps = []step{
	{
		Name:   "Research",
		System: "You are a thorough researcher. Gather comprehensive facts and data.",
		Prompt: "Research the following topic thoroughly. List key facts, important developments, major players, and current state of the art. Topic: %s",
	},
	{
		Name:   "Analyze",
		System: "You are an analytical thinker. Find patterns, connections, and insights.",
		Prompt: "Based on the following research, provide a deep analysis:\n\n%s\n\nIdentify trends, strengths, weaknesses, and unique insights.",
	},
	{
		Name:   "Summarize",
		System: "You are a concise executive writer. Distill complex analysis into clear summaries.",
		Prompt: "Based on the following research and analysis, write an executive summary:\n\n%s\n\nWrite a clear overview with key takeaways and recommended next steps.",
	},
}

func main() {
	hubURL := flag.String("hub", defaultHubURL(), "Hub URL")
	model := flag.String("model", "llama3", "Model name")
	maxTokens := flag.Int("max-tokens", 2048, "Max output tokens")
	timeoutS := flag.Int("timeout", 300, "Timeout in seconds")
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		fmt.Println("Usage: go run chain.go [flags] \"Topic to explore\"")
		os.Exit(1)
	}
	topic := args[0]

	fmt.Printf("Starting reasoning chain for: %s\n", topic)
	fmt.Printf("Model: %s  Hub: %s\n\n", *model, *hubURL)

	prevOutput := topic
	start := time.Now()

	type namedResult struct {
		Step         string  `json:"step"`
		Content      string  `json:"content"`
		OutputTokens float64 `json:"output_tokens"`
		LatencyMs    float64 `json:"latency_ms"`
		AgentID      string  `json:"agent_id"`
	}
	var stepResults []namedResult

	for i, s := range steps {
		fmt.Printf("Step %d: %-10s ... ", i+1, s.Name)

		prompt := fmt.Sprintf(s.Prompt, prevOutput)
		result, err := submitAndPoll(*hubURL, s.System, prompt, *model, *maxTokens, *timeoutS)
		if err != nil {
			fmt.Printf("FAILED: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("OK (%.1fs, %s)\n", result.LatencyMs/1000.0, lastN(result.AgentID, 8))
		stepResults = append(stepResults, namedResult{s.Name, result.Content, result.OutputTokens, result.LatencyMs, result.AgentID})
		prevOutput = result.Content
	}

	elapsed := time.Since(start).Seconds()
	fmt.Printf("\n--- FINAL SUMMARY ---\n\n%s\n", prevOutput)
	fmt.Printf("\nTotal time: %.2fs\n", elapsed)
	saveResults("chain", map[string]interface{}{
		"timestamp":  time.Now().Format(time.RFC3339),
		"hub":        *hubURL,
		"model":      *model,
		"topic":      topic,
		"elapsed_s":  elapsed,
		"steps":      stepResults,
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

type chainResult struct {
	Content      string
	OutputTokens float64
	LatencyMs    float64
	AgentID      string
}

func submitAndPoll(hub, system, prompt, model string, maxTokens, timeoutS int) (*chainResult, error) {
	taskID := uuid.New().String()
	u := fmt.Sprintf("%s/tasks", hub)

	payload := map[string]interface{}{
		"task_id":    taskID,
		"model":      model,
		"prompt":     prompt,
		"system":     system,
		"max_tokens": maxTokens,
	}

	data, _ := json.Marshal(payload)
	_, err := http.Post(u, "application/json", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}

	deadline := time.Now().Add(time.Duration(timeoutS) * time.Second)
	interval := 2 * time.Second

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
			return &chainResult{
				Content:      str(res, "content"),
				OutputTokens: num(res, "output_tokens"),
				LatencyMs:    num(res, "latency_ms"),
				AgentID:      str(res, "agent_id"),
			}, nil
		}
		if state == "FAILED" {
			return nil, fmt.Errorf("task failed on agent")
		}
		time.Sleep(interval)
	}

	return nil, fmt.Errorf("timeout")
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
