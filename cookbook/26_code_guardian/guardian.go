// Recipe 26: Code Guardian — multi-specialist parallel code review on the grid.
//
// Fans out 4 specialist reviews (Security, Performance, Docs, Refactoring) in parallel,
// then synthesizes them into a unified report via a 5th task.
//
// Usage:
//
//	go run guardian.go main.go
//	go run guardian.go --hub http://192.168.1.10:9000 --model llama3 mycode.py
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

type specialist struct {
	Name   string
	System string
	Prompt string
}

var specialists = []specialist{
	{
		Name:   "Security",
		System: "You are a senior security engineer. Scan the code for vulnerabilities.",
		Prompt: "Scan for: prompt injection, SQL injection, credential leakage, insecure defaults, " +
			"auth bypass, and OWASP Top 10 issues. List each finding with severity (HIGH/MEDIUM/LOW) " +
			"and a one-line fix recommendation.\n\nCode:\n%s",
	},
	{
		Name:   "Performance",
		System: "You are a performance optimization expert.",
		Prompt: "Identify: O(N²) bottlenecks, redundant I/O, blocking calls in async contexts, " +
			"unnecessary allocations, and missing caches. For each issue, estimate impact and suggest fix.\n\nCode:\n%s",
	},
	{
		Name:   "Documentation",
		System: "You are a technical writer and architect.",
		Prompt: "Identify: missing docstrings/comments, undocumented public APIs, unclear variable names, " +
			"and architectural concerns. Generate a high-level module summary and list what needs documentation.\n\nCode:\n%s",
	},
	{
		Name:   "Refactoring",
		System: "You are a clean code advocate.",
		Prompt: "Suggest: how to split large functions, reduce coupling, improve modularity, " +
			"eliminate duplication, and apply SOLID principles. Prioritize suggestions by impact.\n\nCode:\n%s",
	},
}

const synthesisSystem = "You are a senior engineering lead writing a unified code review report."
const synthesisTpl = `Synthesize the following specialist reviews into a unified, actionable code review report.
Structure: Executive Summary → Critical Issues → Recommendations by Category → Prioritized Action Plan.

Security Review:
%s

Performance Review:
%s

Documentation Review:
%s

Refactoring Review:
%s`

type reviewResult struct {
	Name    string
	State   string
	Content string
	Tokens  int
	Error   string
}

var httpClient = &http.Client{Timeout: 30 * time.Second}

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

func submitAndWait(hub, system, prompt, model string, maxTokens, timeoutS int) (string, int, error) {
	taskID := "guardian-" + uuid.New().String()[:8]
	body, _ := json.Marshal(map[string]interface{}{
		"task_id":    taskID,
		"model":      model,
		"prompt":     prompt,
		"system":     system,
		"max_tokens": maxTokens,
	})
	resp, err := httpClient.Post(hub+"/tasks", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", 0, fmt.Errorf("submit: %w", err)
	}
	resp.Body.Close()

	deadline := time.Now().Add(time.Duration(timeoutS) * time.Second)
	interval := 2 * time.Second
	for time.Now().Before(deadline) {
		time.Sleep(interval)
		interval = minDur(time.Duration(float64(interval)*1.3), 10*time.Second)

		r, err := httpClient.Get(fmt.Sprintf("%s/tasks/%s", hub, taskID))
		if err != nil {
			continue
		}
		var data map[string]interface{}
		json.NewDecoder(r.Body).Decode(&data)
		r.Body.Close()

		state, _ := data["state"].(string)
		if state == "COMPLETE" {
			res, _ := data["result"].(map[string]interface{})
			content, _ := res["content"].(string)
			out, _ := res["output_tokens"].(float64)
			return content, int(out), nil
		}
		if state == "FAILED" {
			res, _ := data["result"].(map[string]interface{})
			errMsg, _ := res["error"].(string)
			return "", 0, fmt.Errorf("task failed: %s", errMsg)
		}
	}
	return "", 0, fmt.Errorf("timeout after %ds", timeoutS)
}

func main() {
	hub := flag.String("hub", defaultHubURL(), "Hub URL")
	model := flag.String("model", "llama3", "Model to use")
	maxTokens := flag.Int("max-tokens", 1500, "Max tokens per specialist review")
	timeout := flag.Int("timeout", 300, "Timeout per task (seconds)")
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		fmt.Println("Usage: go run guardian.go [flags] <file>")
		flag.PrintDefaults()
		return
	}
	*hub = strings.TrimRight(*hub, "/")

	code, err := os.ReadFile(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading file: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\n  Code Guardian\n")
	fmt.Printf("    File:  %s (%d bytes)\n", args[0], len(code))
	fmt.Printf("    Hub:   %s\n", *hub)
	fmt.Printf("    Model: %s\n", *model)
	fmt.Printf("    Steps: Security | Performance | Documentation | Refactoring → Synthesis\n\n")

	// Phase 1: run 4 specialist reviews in parallel
	var mu sync.Mutex
	var wg sync.WaitGroup
	results := make([]reviewResult, len(specialists))

	for i, sp := range specialists {
		wg.Add(1)
		go func(idx int, s specialist) {
			defer wg.Done()
			prompt := fmt.Sprintf(s.Prompt, string(code))
			fmt.Printf("  [%s] analyzing...\n", s.Name)
			content, tokens, err := submitAndWait(*hub, s.System, prompt, *model, *maxTokens, *timeout)
			mu.Lock()
			if err != nil {
				results[idx] = reviewResult{Name: s.Name, State: "FAILED", Error: err.Error()}
				fmt.Printf("  [%s] FAILED: %v\n", s.Name, err)
			} else {
				results[idx] = reviewResult{Name: s.Name, State: "COMPLETE", Content: content, Tokens: tokens}
				fmt.Printf("  [%s] done  %d tokens\n", s.Name, tokens)
			}
			mu.Unlock()
		}(i, sp)
	}
	wg.Wait()

	// Collect specialist outputs
	reviews := make(map[string]string)
	totalTokens := 0
	for _, r := range results {
		if r.State == "COMPLETE" {
			reviews[r.Name] = r.Content
			totalTokens += r.Tokens
		} else {
			reviews[r.Name] = fmt.Sprintf("[FAILED: %s]", r.Error)
		}
	}

	// Phase 2: synthesis
	fmt.Printf("\n  [Synthesis] composing unified report...\n")
	synthPrompt := fmt.Sprintf(synthesisTpl,
		reviews["Security"], reviews["Performance"],
		reviews["Documentation"], reviews["Refactoring"])

	report, tokens, err := submitAndWait(*hub, synthesisSystem, synthPrompt, *model, 3000, *timeout)
	if err != nil {
		fmt.Printf("  [Synthesis] FAILED: %v\n", err)
	} else {
		totalTokens += tokens
		fmt.Printf("  [Synthesis] done  %d tokens\n", tokens)
	}

	// Print report
	fmt.Printf("\n  %s\n", strings.Repeat("=", 60))
	fmt.Printf("  Code Guardian Report — %s\n", args[0])
	fmt.Printf("  Total tokens: %d\n", totalTokens)
	fmt.Printf("  %s\n\n", strings.Repeat("=", 60))

	if report != "" {
		fmt.Println(report)
	} else {
		for _, r := range results {
			fmt.Printf("\n--- %s ---\n%s\n", r.Name, r.Content)
		}
	}
	fmt.Println()
}

func minDur(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
