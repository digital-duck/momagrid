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

var stepSystems = map[string]string{
	"translate": "You are a translation assistant. If the input is already in English, " +
		"output it unchanged. Otherwise translate it to English, preserving meaning.",
	"concepts": "You are an NLP analyst. Extract 3-5 key concepts from this query as a " +
		"comma-separated list. Output only the concepts, nothing else.",
	"optimise": "You are a prompt engineer. Rewrite this query to be clearer, more specific, " +
		"and more likely to get a precise answer. Output only the rewritten query.",
	"generate": "You are a knowledgeable assistant. Answer the question clearly and accurately " +
		"in 3-5 sentences.",
	"format": "You are a technical writer. Take this AI response and format it as a clean, " +
		"readable summary with: (1) a one-sentence TL;DR, (2) the full explanation.",
}

var demoQueries = []string{
	"What is distributed inference and why does it matter?",
}

type stepResult struct {
	Step         string  `json:"step"`
	State        string  `json:"state"`
	Content      string  `json:"content"`
	Model        string  `json:"model"`
	LatencyS     float64 `json:"latency_s"`
	OutputTokens float64 `json:"output_tokens"`
	AgentID      string  `json:"agent_id"`
	Error        string  `json:"error"`
}

var compilerClient = &http.Client{Timeout: 30 * time.Second}

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
	translateModel := flag.String("translate-model", "llama3", "Model for translation")
	analysisModel := flag.String("analysis-model", "llama3", "Model for concept extraction + optimisation")
	generateModel := flag.String("generate-model", "llama3", "Model for generation")
	formatModel := flag.String("format-model", "llama3", "Model for formatting")
	demo := flag.Bool("demo", false, "Run demo query")
	maxTokens := flag.Int("max-tokens", 512, "Max output tokens")
	timeoutS := flag.Int("timeout", 180, "Timeout in seconds")
	flag.Parse()

	var query string
	if *demo || len(flag.Args()) == 0 {
		query = demoQueries[0]
	} else {
		query = flag.Args()[0]
	}

	runPipeline(*hubURL, query,
		*translateModel, *analysisModel, *generateModel, *formatModel,
		*maxTokens, *timeoutS)
}

func runPipeline(hub, query, translateModel, analysisModel, generateModel, formatModel string, maxTokens, timeoutS int) {
	fmt.Printf("\n  Momahub Compiler Pipeline Demo\n")
	fmt.Printf("    Hub:     %s\n", hub)
	q := query
	if len(q) > 80 {
		q = q[:80]
	}
	fmt.Printf("    Input:   %s\n\n", q)
	fmt.Println("  Front-end:")

	wallStart := time.Now()

	// Step 1: Translate (sequential)
	step1 := runStep(hub, "translate", query, translateModel, 256, timeoutS)
	if step1.State != "COMPLETE" {
		fmt.Printf("  Pipeline aborted at Step 1: %s\n", step1.Error)
		return
	}
	englishQuery := strings.TrimSpace(step1.Content)

	// Steps 2+3 in parallel (mid-end)
	fmt.Println("\n  Mid-end (parallel):")
	var wg sync.WaitGroup
	var step2, step3 stepResult
	wg.Add(2)
	go func() {
		defer wg.Done()
		step2 = runStep(hub, "concepts", englishQuery, analysisModel, 128, timeoutS)
	}()
	go func() {
		defer wg.Done()
		step3 = runStep(hub, "optimise", englishQuery, analysisModel, 256, timeoutS)
	}()
	wg.Wait()

	optimisedQuery := englishQuery
	if step3.State == "COMPLETE" {
		optimisedQuery = strings.TrimSpace(step3.Content)
	}

	// Step 4: Generate (back-end)
	fmt.Println("\n  Back-end:")
	step4 := runStep(hub, "generate", optimisedQuery, generateModel, maxTokens, timeoutS)
	if step4.State != "COMPLETE" {
		fmt.Println("  Pipeline aborted at Step 4.")
		return
	}
	rawResponse := strings.TrimSpace(step4.Content)

	// Step 5: Format output
	step5 := runStep(hub, "format",
		fmt.Sprintf("Query: %s\n\nResponse: %s", englishQuery, rawResponse),
		formatModel, maxTokens, timeoutS)

	wallTime := time.Since(wallStart).Seconds()

	steps := []stepResult{step1, step2, step3, step4, step5}
	agentSet := make(map[string]bool)
	totalTokens := 0.0
	for _, s := range steps {
		if s.AgentID != "" {
			agentSet[lastN(s.AgentID, 14)] = true
		}
		totalTokens += s.OutputTokens
	}

	fmt.Printf("\n  %s\n", strings.Repeat("=", 60))
	fmt.Printf("  PIPELINE RESULTS\n")
	fmt.Printf("  %s\n", strings.Repeat("-", 60))
	fmt.Printf("  Original query:  %s\n", query)
	fmt.Printf("  English:         %s\n", englishQuery)
	if step2.State == "COMPLETE" {
		fmt.Printf("  Key concepts:    %s\n", strings.TrimSpace(step2.Content))
	}
	fmt.Printf("  Optimised query: %s\n", optimisedQuery)

	final := rawResponse
	if step5.State == "COMPLETE" {
		final = step5.Content
	}
	fmt.Printf("\n  FINAL OUTPUT:\n")
	fmt.Printf("  %s\n", strings.Repeat("-", 60))
	fmt.Printf("  %s\n", final)
	fmt.Printf("\n  %s\n", strings.Repeat("=", 60))
	fmt.Printf("  Wall time:   %.1fs\n", wallTime)
	fmt.Printf("  Steps:       5 (1 sequential + 2 parallel + 2 sequential)\n")
	fmt.Printf("  Agents used: %d\n", len(agentSet))
	fmt.Printf("  Total tokens:%.0f\n\n", totalTokens)
	saveResults("compiler_pipeline", map[string]interface{}{
		"timestamp":    time.Now().Format(time.RFC3339),
		"hub":          hub,
		"query":        query,
		"wall_time_s":  wallTime,
		"total_tokens": totalTokens,
		"steps":        steps,
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

func runStep(hub, stepName, prompt, model string, maxTokens, timeoutS int) stepResult {
	taskID := fmt.Sprintf("compiler-%s-%s", stepName, uuid.New().String()[:6])
	system := stepSystems[stepName]
	t0 := time.Now()

	pad := stepName
	for len(pad) < 10 {
		pad += " "
	}
	modelPad := model
	for len(modelPad) < 20 {
		modelPad += " "
	}
	fmt.Printf("  → [%s] %s ...", pad, modelPad)

	payload := map[string]interface{}{
		"task_id":    taskID,
		"model":      model,
		"prompt":     prompt,
		"system":     system,
		"max_tokens": maxTokens,
	}
	body, _ := json.Marshal(payload)
	resp, err := compilerClient.Post(fmt.Sprintf("%s/tasks", hub), "application/json", bytes.NewReader(body))
	if err != nil {
		fmt.Printf(" SUBMIT_FAILED: %v\n", err)
		return stepResult{Step: stepName, State: "SUBMIT_FAILED", Error: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		fmt.Printf(" HTTP %d\n", resp.StatusCode)
		return stepResult{Step: stepName, State: "SUBMIT_REJECTED"}
	}

	deadline := time.Now().Add(time.Duration(timeoutS) * time.Second)
	interval := 1500 * time.Millisecond

	for time.Now().Before(deadline) {
		getResp, err := compilerClient.Get(fmt.Sprintf("%s/tasks/%s", hub, taskID))
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
			elapsed := time.Since(t0).Seconds()
			outTok := num(res, "output_tokens")
			agentID := str(res, "agent_id")
			fmt.Printf(" OK %.1fs  %.0f tok  agent=..%s\n",
				elapsed, outTok, lastN(agentID, 10))
			return stepResult{
				Step:         stepName,
				State:        "COMPLETE",
				Content:      str(res, "content"),
				Model:        model,
				LatencyS:     elapsed,
				OutputTokens: outTok,
				AgentID:      agentID,
			}
		}
		if state == "FAILED" {
			errMsg := ""
			if res, ok := task["result"].(map[string]interface{}); ok {
				errMsg = str(res, "error")
			}
			fmt.Printf(" FAILED: %s\n", errMsg)
			return stepResult{Step: stepName, State: "FAILED", Error: errMsg}
		}

		time.Sleep(interval)
		if interval < 8*time.Second {
			interval = time.Duration(float64(interval) * 1.2)
		}
	}

	fmt.Printf(" TIMEOUT\n")
	return stepResult{Step: stepName, State: "TIMEOUT"}
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
