// Recipe 05: RAG on Grid (Simulated) — retrieve context, answer question on the grid.
//
// Simulates retrieval-augmented generation by embedding a context snippet into
// the prompt and submitting the question to the grid for answering.
//
// Usage:
//
//	go run rag.go "What are the key benefits of hub-and-spoke inference?"
//	go run rag.go --context "custom context here" "Your question"
//	go run rag.go --hub http://192.168.1.10:9000 "question"
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

const defaultContext = `Hub-and-spoke inference (Momahub) offers several key benefits:
1. Reduced hardware requirements for clients — inference runs on GPU nodes, not the requester.
2. Parallel execution across multiple GPU nodes for high throughput.
3. Centralized task management with priority queuing and retry logic.
4. Reward tracking — operators earn credits per 1000 output tokens.
5. Resilience via automatic agent failover and task re-queuing on eviction.
6. Flexible dispatch: push mode (hub → agent HTTP POST) or pull mode (agent SSE stream).`

const ragSystem = "You are a helpful assistant that answers questions based only on the provided context. " +
	"Do not use outside knowledge. If the context does not contain the answer, say so."

func ragPrompt(context, question string) string {
	return fmt.Sprintf("Context:\n%s\n\n---\n\nQuestion: %s", context, question)
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

func submitAndWait(hub, taskID, system, prompt, model string, maxTokens, timeoutS int) (map[string]interface{}, error) {
	client := &http.Client{Timeout: time.Duration(timeoutS+30) * time.Second}

	body, _ := json.Marshal(map[string]interface{}{
		"task_id":    taskID,
		"model":      model,
		"prompt":     prompt,
		"system":     system,
		"max_tokens": maxTokens,
	})
	resp, err := client.Post(hub+"/tasks", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("submit: %w", err)
	}
	resp.Body.Close()

	deadline := time.Now().Add(time.Duration(timeoutS) * time.Second)
	interval := 2 * time.Second
	for time.Now().Before(deadline) {
		time.Sleep(interval)
		interval = minDur(time.Duration(float64(interval)*1.3), 10*time.Second)

		r, err := client.Get(fmt.Sprintf("%s/tasks/%s", hub, taskID))
		if err != nil {
			continue
		}
		var data map[string]interface{}
		json.NewDecoder(r.Body).Decode(&data)
		r.Body.Close()

		state, _ := data["state"].(string)
		if state == "COMPLETE" || state == "FAILED" {
			return data, nil
		}
	}
	return map[string]interface{}{"state": "TIMEOUT"}, nil
}

func main() {
	hub := flag.String("hub", defaultHubURL(), "Hub URL")
	model := flag.String("model", "llama3", "Model to use")
	context := flag.String("context", defaultContext, "Context for RAG (overrides built-in)")
	maxTokens := flag.Int("max-tokens", 1000, "Max output tokens")
	timeout := flag.Int("timeout", 300, "Timeout (seconds)")
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		fmt.Println("Usage: go run rag.go [flags] \"<question>\"")
		flag.PrintDefaults()
		return
	}
	question := strings.Join(args, " ")
	*hub = strings.TrimRight(*hub, "/")

	fmt.Printf("\n  RAG on Grid\n")
	fmt.Printf("    Hub:      %s\n", *hub)
	fmt.Printf("    Model:    %s\n", *model)
	fmt.Printf("    Question: %s\n", question)
	fmt.Println()

	taskID := "rag-" + uuid.New().String()[:8]
	t0 := time.Now()

	fmt.Print("  Submitting to grid... ")
	result, err := submitAndWait(*hub, taskID, ragSystem, ragPrompt(*context, question), *model, *maxTokens, *timeout)
	if err != nil {
		fmt.Printf("ERROR: %v\n", err)
		return
	}

	state, _ := result["state"].(string)
	if state == "COMPLETE" {
		r, _ := result["result"].(map[string]interface{})
		content, _ := r["content"].(string)
		outputTokens := numInt(r, "output_tokens")
		agentID, _ := r["agent_id"].(string)
		elapsed := time.Since(t0)

		fmt.Printf("done  %.1fs  %d tokens  agent=..%s\n\n", elapsed.Seconds(), outputTokens, lastN(agentID, 12))
		fmt.Println("  ─────────────────────────────────────────────────────────")
		fmt.Printf("  Answer:\n\n  %s\n", strings.ReplaceAll(content, "\n", "\n  "))
		fmt.Println("  ─────────────────────────────────────────────────────────")
		saveResults("rag", map[string]interface{}{
			"timestamp":     time.Now().Format(time.RFC3339),
			"hub":           *hub,
			"state":         state,
			"output_tokens": outputTokens,
			"content":       content,
		})
	} else {
		fmt.Printf("FAILED: state=%s\n", state)
	}
	fmt.Println()
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

func numInt(m map[string]interface{}, k string) int {
	if m == nil {
		return 0
	}
	switch v := m[k].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

func lastN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func minDur(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
