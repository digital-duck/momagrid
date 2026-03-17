package main

// Recipe 29: Model Fingerprinting
//
// Tests output consistency and determinism from the same model across different
// agents. Sends identical prompts to multiple agents and compares responses to
// identify variance, agent identity, and cross-node reliability.
//
// Usage:
//   go run ./29_model_fingerprinting/model_fingerprinting.go --hub http://localhost:9000

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

const (
	// Deterministic prompt — low temperature should produce similar answers
	deterministicPrompt = "What is 2 + 2? Answer with just the number."
	// Creative prompt — high temperature will vary across agents
	creativePrompt = "Write a haiku about a mountain. Output ONLY the haiku, no titles."
	// Factual prompt — should be stable across models
	factualPrompt = "What is the boiling point of water at sea level in Celsius? Answer with just the number."
)

var testCases = []struct {
	name        string
	prompt      string
	temperature float64
	expectStable bool
}{
	{"deterministic-math", deterministicPrompt, 0.0, true},
	{"stable-factual", factualPrompt, 0.1, true},
	{"creative-haiku", creativePrompt, 0.8, false},
}

type fingerprint struct {
	TaskID      string  `json:"task_id"`
	AgentID     string  `json:"agent_id"`
	AgentName   string  `json:"agent_name"`
	AgentHost   string  `json:"agent_host"`
	Model       string  `json:"model"`
	Content     string  `json:"content"`
	OutputTokens float64 `json:"output_tokens"`
	LatencyMs   float64 `json:"latency_ms"`
	State       string  `json:"state"`
}

type testResult struct {
	TestName     string        `json:"test_name"`
	Prompt       string        `json:"prompt"`
	Temperature  float64       `json:"temperature"`
	ExpectStable bool          `json:"expect_stable"`
	Fingerprints []fingerprint `json:"fingerprints"`
	UniqueOutputs int          `json:"unique_outputs"`
	ConsistencyPct float64     `json:"consistency_pct"`
}

type fingerprintReport struct {
	Timestamp   string       `json:"timestamp"`
	Hub         string       `json:"hub"`
	Model       string       `json:"model"`
	NumRuns     int          `json:"num_runs"`
	Results     []testResult `json:"results"`
	Summary     string       `json:"summary"`
}

var client = &http.Client{Timeout: 90 * time.Second}

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
		return cfg.Hub.URLs[0]
	}
	return fmt.Sprintf("http://localhost:%d", cfg.Hub.Port)
}

func submitAndPoll(hubURL, model, prompt string, temperature float64) fingerprint {
	taskID := uuid.New().String()
	payload := map[string]interface{}{
		"task_id":     taskID,
		"model":       model,
		"prompt":      prompt,
		"max_tokens":  64,
		"temperature": temperature,
	}
	body, _ := json.Marshal(payload)
	resp, err := http.Post(hubURL+"/tasks", "application/json", bytes.NewReader(body))
	if err != nil {
		return fingerprint{TaskID: taskID, State: "SUBMIT_ERROR"}
	}
	resp.Body.Close()

	for i := 0; i < 60; i++ {
		time.Sleep(2 * time.Second)
		r, err := http.Get(fmt.Sprintf("%s/tasks/%s", hubURL, taskID))
		if err != nil {
			continue
		}
		var status map[string]interface{}
		json.NewDecoder(r.Body).Decode(&status)
		r.Body.Close()

		state, _ := status["state"].(string)
		if state == "COMPLETE" || state == "FAILED" {
			fp := fingerprint{TaskID: taskID, State: state, Model: model}
			if res, ok := status["result"].(map[string]interface{}); ok {
				fp.Content = strings.TrimSpace(fmt.Sprint(res["content"]))
				fp.AgentID = fmt.Sprint(res["agent_id"])
				fp.AgentName = fmt.Sprint(res["agent_name"])
				fp.AgentHost = fmt.Sprint(res["agent_host"])
				fp.OutputTokens, _ = res["output_tokens"].(float64)
				fp.LatencyMs, _ = res["latency_ms"].(float64)
			}
			return fp
		}
	}
	return fingerprint{TaskID: taskID, State: "TIMEOUT", Model: model}
}

func runTestCase(hubURL, model string, tc struct {
	name        string
	prompt      string
	temperature float64
	expectStable bool
}, numRuns int) testResult {
	fmt.Printf("  [%s] temp=%.1f runs=%d\n", tc.name, tc.temperature, numRuns)

	var mu sync.Mutex
	var prints []fingerprint
	var wg sync.WaitGroup

	for i := 0; i < numRuns; i++ {
		wg.Add(1)
		go func(run int) {
			defer wg.Done()
			fp := submitAndPoll(hubURL, model, tc.prompt, tc.temperature)
			mu.Lock()
			prints = append(prints, fp)
			mu.Unlock()
			fmt.Printf("    run=%d agent=%s content=%q\n", run+1,
				shortID(fp.AgentName, fp.AgentID), truncate(fp.Content, 40))
		}(i)
	}
	wg.Wait()

	// Measure uniqueness
	unique := map[string]bool{}
	for _, fp := range prints {
		if fp.Content != "" {
			unique[fp.Content] = true
		}
	}
	complete := 0
	for _, fp := range prints {
		if fp.State == "COMPLETE" {
			complete++
		}
	}
	var consistencyPct float64
	if complete > 0 {
		// consistency = fraction that share the most-common answer
		counts := map[string]int{}
		for _, fp := range prints {
			if fp.State == "COMPLETE" {
				counts[fp.Content]++
			}
		}
		maxCount := 0
		for _, v := range counts {
			if v > maxCount {
				maxCount = v
			}
		}
		consistencyPct = float64(maxCount) / float64(complete) * 100
	}

	return testResult{
		TestName:       tc.name,
		Prompt:         tc.prompt,
		Temperature:    tc.temperature,
		ExpectStable:   tc.expectStable,
		Fingerprints:   prints,
		UniqueOutputs:  len(unique),
		ConsistencyPct: consistencyPct,
	}
}

func shortID(name, id string) string {
	if name != "" && name != "<nil>" {
		return name
	}
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func saveResults(outDir string, report *fingerprintReport) error {
	os.MkdirAll(outDir, 0755)
	ts := time.Now().Format("20060102_150405")
	path := filepath.Join(outDir, fmt.Sprintf("model_fingerprinting_%s.json", ts))
	data, _ := json.MarshalIndent(report, "", "  ")
	return os.WriteFile(path, data, 0644)
}

func main() {
	hubURL := flag.String("hub", defaultHubURL(), "Hub URL")
	model := flag.String("model", "llama3", "Model to fingerprint")
	runs := flag.Int("runs", 3, "Number of repeated runs per test case")
	outDir := flag.String("out", "cookbook/out", "Output directory")
	flag.Parse()

	fmt.Println("🦆 Model Fingerprinting — Cross-Agent Consistency Test")
	fmt.Printf("   Hub:   %s\n", *hubURL)
	fmt.Printf("   Model: %s\n", *model)
	fmt.Printf("   Runs:  %d × %d tests\n", *runs, len(testCases))
	fmt.Println()

	start := time.Now()
	var results []testResult

	for _, tc := range testCases {
		r := runTestCase(*hubURL, *model, tc, *runs)
		results = append(results, r)
		stableStr := "variable"
		if tc.expectStable {
			stableStr = "stable"
		}
		fmt.Printf("    ✓ unique=%d  consistency=%.0f%%  (expected %s)\n\n",
			r.UniqueOutputs, r.ConsistencyPct, stableStr)
	}

	// Summary
	stableCount := 0
	for _, r := range results {
		if r.ExpectStable && r.ConsistencyPct >= 80 {
			stableCount++
		}
	}
	summary := fmt.Sprintf("model=%s runs=%d×%d stable=%d/%d deterministic tests passed",
		*model, *runs, len(testCases), stableCount,
		func() int {
			n := 0
			for _, tc := range testCases {
				if tc.expectStable {
					n++
				}
			}
			return n
		}())

	report := &fingerprintReport{
		Timestamp: time.Now().Format("2006-01-02 15:04:05"),
		Hub:       *hubURL,
		Model:     *model,
		NumRuns:   *runs,
		Results:   results,
		Summary:   summary,
	}

	if err := saveResults(*outDir, report); err != nil {
		fmt.Fprintf(os.Stderr, "⚠️  save error: %v\n", err)
	}

	fmt.Printf("📊 Fingerprinting complete (%.1fs)\n", time.Since(start).Seconds())
	fmt.Printf("   %s\n", summary)
}
