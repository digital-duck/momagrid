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

const (
	reviewSystem = "You are a senior software engineer conducting a thorough code review. " +
		"Identify bugs, security vulnerabilities, performance issues, and style problems. " +
		"Be specific — cite line numbers or variable names where possible."

	summarySystem = "You are a technical lead summarising a code review. " +
		"Produce a numbered action list of the top issues to fix, ordered by severity. " +
		"Be concise — one line per item."

	refactorSystem = "You are an expert programmer. Based on the code and the review, " +
		"suggest the most important refactoring. Show the improved code snippet only, " +
		"with a brief comment explaining each change."
)

type crResult struct {
	Label        string  `json:"label"`
	State        string  `json:"state"`
	Content      string  `json:"content"`
	Model        string  `json:"model"`
	LatencyS     float64 `json:"latency_s"`
	OutputTokens float64 `json:"output_tokens"`
	AgentID      string  `json:"agent_id"`
	Error        string  `json:"error"`
}

var crClient = &http.Client{Timeout: 30 * time.Second}

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
	filePath := flag.String("file", "", "Code file to review")
	reviewer := flag.String("reviewer", "deepseek-coder-v2", "Model for code review")
	summariser := flag.String("summariser", "llama3", "Model for summarisation")
	refactorModel := flag.String("refactor-model", "qwen2.5-coder", "Model for refactoring")
	maxTokens := flag.Int("max-tokens", 1024, "Max output tokens")
	timeoutS := flag.Int("timeout", 300, "Timeout in seconds")
	flag.Parse()

	var code, filename string
	if *filePath != "" {
		data, err := os.ReadFile(*filePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading file: %v\n", err)
			os.Exit(1)
		}
		code = string(data)
		filename = *filePath
	} else {
		// Demo code snippet
		code = `package main

import (
    "fmt"
    "os"
    "io/ioutil"
)

func readFile(path string) string {
    data, _ := ioutil.ReadFile(path)
    return string(data)
}

func main() {
    if len(os.Args) < 2 {
        fmt.Println("Usage: prog <file>")
        return
    }
    content := readFile(os.Args[1])
    fmt.Println(content)
}`
		filename = "demo_code.go (demo)"
	}

	if len(code) > 4000 {
		code = code[:4000]
	}

	fmt.Printf("\n  Code Review Pipeline\n")
	fmt.Printf("    Hub:      %s\n", *hubURL)
	fmt.Printf("    File:     %s (%d chars)\n", filename, len(code))
	fmt.Printf("    Step 1:   %s → review\n", *reviewer)
	fmt.Printf("    Step 2:   %s → summarise\n", *summariser)
	fmt.Printf("    Step 3:   %s → refactor\n\n", *refactorModel)

	wallStart := time.Now()

	// Step 1: Review (sequential)
	fmt.Printf("  → [Review] submitting to %s...\n", *reviewer)
	reviewResult := submitAndWaitCR(*hubURL,
		fmt.Sprintf("Review this code:\n\n```\n%s\n```", code),
		reviewSystem, *reviewer, *maxTokens, *timeoutS, "Review")
	if reviewResult.State != "COMPLETE" {
		fmt.Printf("  Pipeline aborted at Step 1: %s\n", reviewResult.Error)
		return
	}
	fmt.Printf("    OK [Review] done in %.1fs  %.0f tokens  agent=..%s\n",
		reviewResult.LatencyS, reviewResult.OutputTokens, lastN(reviewResult.AgentID, 12))

	// Step 2 + 3 in parallel
	reviewText := reviewResult.Content
	var wg sync.WaitGroup
	var summaryResult, refactorResult crResult

	wg.Add(2)
	go func() {
		defer wg.Done()
		fmt.Printf("  → [Summary] submitting to %s...\n", *summariser)
		summaryResult = submitAndWaitCR(*hubURL,
			fmt.Sprintf("Summarise this code review as a numbered action list:\n\n%s", reviewText),
			summarySystem, *summariser, 512, *timeoutS, "Summary")
		if summaryResult.State == "COMPLETE" {
			fmt.Printf("    OK [Summary] done in %.1fs  %.0f tokens  agent=..%s\n",
				summaryResult.LatencyS, summaryResult.OutputTokens, lastN(summaryResult.AgentID, 12))
		}
	}()

	go func() {
		defer wg.Done()
		fmt.Printf("  → [Refactor] submitting to %s...\n", *refactorModel)
		codeSnip := code
		if len(codeSnip) > 3000 {
			codeSnip = codeSnip[:3000]
		}
		reviewSnip := reviewText
		if len(reviewSnip) > 2000 {
			reviewSnip = reviewSnip[:2000]
		}
		refactorResult = submitAndWaitCR(*hubURL,
			fmt.Sprintf("Original code:\n```\n%s\n```\n\nReview findings:\n%s\n\nSuggest the most important refactoring:", codeSnip, reviewSnip),
			refactorSystem, *refactorModel, *maxTokens, *timeoutS, "Refactor")
		if refactorResult.State == "COMPLETE" {
			fmt.Printf("    OK [Refactor] done in %.1fs  %.0f tokens  agent=..%s\n",
				refactorResult.LatencyS, refactorResult.OutputTokens, lastN(refactorResult.AgentID, 12))
		}
	}()

	wg.Wait()
	wallTime := time.Since(wallStart).Seconds()

	// Print results
	fmt.Printf("\n  %s\n", strings.Repeat("=", 60))
	fmt.Printf("  STEP 1 — Code Review (%s)\n", *reviewer)
	fmt.Printf("  %s\n", strings.Repeat("-", 60))
	fmt.Printf("  %s\n\n", reviewResult.Content)

	if summaryResult.State == "COMPLETE" {
		fmt.Printf("  STEP 2 — Action List (%s)\n", *summariser)
		fmt.Printf("  %s\n", strings.Repeat("-", 60))
		fmt.Printf("  %s\n\n", summaryResult.Content)
	}

	if refactorResult.State == "COMPLETE" {
		fmt.Printf("  STEP 3 — Refactoring Suggestion (%s)\n", *refactorModel)
		fmt.Printf("  %s\n", strings.Repeat("-", 60))
		fmt.Printf("  %s\n\n", refactorResult.Content)
	}

	fmt.Printf("  %s\n", strings.Repeat("=", 60))
	fmt.Printf("  Total wall time: %.1fs  (steps 2+3 ran in parallel)\n\n", wallTime)
	saveResults("code_review", map[string]interface{}{
		"timestamp": time.Now().Format(time.RFC3339),
		"hub":       *hubURL,
		"file":      filename,
		"wall_time_s": wallTime,
		"review":    reviewResult,
		"summary":   summaryResult,
		"refactor":  refactorResult,
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

func submitAndWaitCR(hub, prompt, system, model string, maxTokens, timeoutS int, label string) crResult {
	taskID := fmt.Sprintf("cr-%s", uuid.New().String()[:8])
	t0 := time.Now()

	payload := map[string]interface{}{
		"task_id":    taskID,
		"model":      model,
		"prompt":     prompt,
		"system":     system,
		"max_tokens": maxTokens,
	}
	body, _ := json.Marshal(payload)
	resp, err := crClient.Post(fmt.Sprintf("%s/tasks", hub), "application/json", bytes.NewReader(body))
	if err != nil {
		return crResult{Label: label, State: "SUBMIT_FAILED", Error: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return crResult{Label: label, State: "SUBMIT_REJECTED", Error: fmt.Sprintf("HTTP %d", resp.StatusCode)}
	}

	deadline := time.Now().Add(time.Duration(timeoutS) * time.Second)
	interval := 2 * time.Second

	for time.Now().Before(deadline) {
		getResp, err := crClient.Get(fmt.Sprintf("%s/tasks/%s", hub, taskID))
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
			return crResult{
				Label:        label,
				State:        "COMPLETE",
				Content:      str(res, "content"),
				Model:        model,
				LatencyS:     time.Since(t0).Seconds(),
				OutputTokens: num(res, "output_tokens"),
				AgentID:      str(res, "agent_id"),
			}
		}
		if state == "FAILED" {
			res, _ := task["result"].(map[string]interface{})
			errMsg := ""
			if res != nil {
				errMsg = str(res, "error")
			}
			return crResult{Label: label, State: "FAILED", Error: errMsg}
		}

		time.Sleep(interval)
		if interval < 8*time.Second {
			interval = time.Duration(float64(interval) * 1.2)
		}
	}

	return crResult{Label: label, State: "TIMEOUT"}
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
