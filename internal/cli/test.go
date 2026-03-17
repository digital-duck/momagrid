package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

type promptEntry struct {
	Category    string  `json:"-"`
	Prompt      string  `json:"prompt"`
	Model       string  `json:"model"`
	System      string  `json:"system"`
	MaxTokens   int     `json:"max_tokens"`
	Temperature float64 `json:"temperature"`
}

type testResult struct {
	TaskID       string  `json:"task_id"`
	Category     string  `json:"category"`
	Prompt       string  `json:"prompt"`
	Model        string  `json:"model"`
	State        string  `json:"state"`
	Content      string  `json:"content"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	LatencyMs    float64 `json:"latency_ms"`
	AgentID      string  `json:"agent_id"`
	Error        string  `json:"error"`
	WallTimeMs   float64 `json:"wall_time_ms"`
	TPS          float64 `json:"tps"`
}

type testReport struct {
	Label     string       `json:"label"`
	HubURL    string       `json:"hub_url"`
	Summary   interface{}  `json:"summary"`
	Results   []testResult `json:"results"`
	WallTimeS float64      `json:"wall_time_s"`
}

// Test implements "mg test".
func Test(args []string) error {
	fs := flag.NewFlagSet("test", flag.ExitOnError)
	hubURL := fs.String("hub-url", "", "Hub URL")
	promptsFile := fs.String("prompts", "", "Path to prompts JSON file")
	fs.StringVar(promptsFile, "p", "", "Prompts file (shorthand)")
	category := fs.String("category", "", "Run only this category")
	fs.StringVar(category, "c", "", "Category (shorthand)")
	concurrency := fs.Int("concurrency", 1, "Parallel task submissions")
	fs.IntVar(concurrency, "j", 1, "Concurrency (shorthand)")
	repeatN := fs.Int("repeat", 1, "Repeat the batch N times")
	fs.IntVar(repeatN, "r", 1, "Repeat (shorthand)")
	timeoutS := fs.Int("timeout", 300, "Per-task timeout in seconds")
	label := fs.String("label", "test", "Label for this test run")
	fs.StringVar(label, "l", "test", "Label (shorthand)")
	output := fs.String("output", "", "Save results to JSON file")
	fs.StringVar(output, "o", "", "Output (shorthand)")
	listCats := fs.Bool("list", false, "List available categories and exit")
	fs.Parse(args)

	// Load prompts
	promptsDict, err := loadPrompts(*promptsFile)
	if err != nil {
		return fmt.Errorf("cannot load prompts: %w", err)
	}

	if *listCats {
		for cat, entries := range promptsDict {
			fmt.Printf("  %-30s (%d prompts)\n", cat, len(entries))
		}
		return nil
	}

	url := ResolveHubURL(*hubURL)

	// Build list of entries to run
	var entries []promptEntry
	for cat, prompts := range promptsDict {
		if *category != "" && cat != *category {
			continue
		}
		for _, p := range prompts {
			e := promptEntry{Category: cat}
			// Parse the prompt map
			if s, ok := p["prompt"].(string); ok {
				e.Prompt = s
			}
			if s, ok := p["model"].(string); ok {
				e.Model = s
			} else {
				e.Model = "llama3.1:8b"
			}
			if s, ok := p["system"].(string); ok {
				e.System = s
			}
			if v, ok := p["max_tokens"].(float64); ok {
				e.MaxTokens = int(v)
			} else {
				e.MaxTokens = 1024
			}
			if v, ok := p["temperature"].(float64); ok {
				e.Temperature = v
			} else {
				e.Temperature = 0.7
			}
			entries = append(entries, e)
		}
	}

	// Repeat
	var expanded []promptEntry
	for i := 0; i < *repeatN; i++ {
		expanded = append(expanded, entries...)
	}
	total := len(expanded)

	fmt.Printf("Running %d tasks  concurrency=%d  repeat=%d  hub=%s\n\n", total, *concurrency, *repeatN, url)

	// Run tests
	start := time.Now()
	results := runTestBatch(url, expanded, *concurrency, *timeoutS, total)
	wallTime := time.Since(start).Seconds()

	// Summary
	summary := computeSummary(results, wallTime)
	s := summary

	fmt.Printf("\n%s\n", repeat('-', 72))
	fmt.Printf("  Total: %d  |  Completed: %d  |  Failed: %d  |  Timeout: %d\n",
		s["total"], s["completed"], s["failed"], s["timed_out"])
	if avgLat, ok := s["avg_latency_ms"].(float64); ok && avgLat > 0 {
		fmt.Printf("  Avg latency: %.0fms  |  Avg TPS: %.1f  |  Wall time: %.1fs\n",
			avgLat, s["avg_tps"], wallTime)
	}
	if dist, ok := s["agent_distribution"].(map[string]int); ok && len(dist) > 0 {
		fmt.Println("  Agent distribution:")
		for agent, count := range dist {
			fmt.Printf("    %s: %d tasks\n", agent, count)
		}
	}

	// Save results
	if *output != "" {
		report := testReport{
			Label:     *label,
			HubURL:    url,
			Summary:   summary,
			Results:   results,
			WallTimeS: wallTime,
		}
		f, err := os.Create(*output)
		if err != nil {
			return err
		}
		defer f.Close()
		enc := json.NewEncoder(f)
		enc.SetIndent("", "  ")
		enc.Encode(report)
		fmt.Printf("  Results saved to %s\n", *output)
	}

	return nil
}

func loadPrompts(path string) (map[string][]map[string]interface{}, error) {
	// Try specified path, then common locations
	candidates := []string{path}
	if path == "" {
		candidates = []string{
			"tests/lan/prompts.json",
			"../mghub.py/tests/lan/prompts.json",
			"prompts.json",
		}
	}

	for _, p := range candidates {
		if p == "" {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var result map[string][]map[string]interface{}
		if err := json.Unmarshal(data, &result); err != nil {
			return nil, fmt.Errorf("invalid JSON in %s: %w", p, err)
		}
		return result, nil
	}
	return nil, fmt.Errorf("prompts file not found (use --prompts to specify path)")
}

func runTestBatch(hubURL string, entries []promptEntry, concurrency, timeoutS, total int) []testResult {
	var mu sync.Mutex
	var results []testResult
	done := 0

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for _, entry := range entries {
		wg.Add(1)
		sem <- struct{}{} // acquire semaphore
		go func(e promptEntry) {
			defer wg.Done()
			defer func() { <-sem }() // release semaphore

			r := submitAndPoll(hubURL, e, timeoutS)
			mu.Lock()
			results = append(results, r)
			done++
			n := done
			mu.Unlock()

			// Live progress
			status := "OK"
			if r.State != "COMPLETE" {
				status = r.State
			}
			tpsStr := ""
			if r.TPS > 0 {
				tpsStr = fmt.Sprintf("%.1f tps", r.TPS)
			}
			fmt.Printf("  [%3d/%d] %-8s %-20s %8.0fms  %5d tok  %10s  %s\n",
				n, total, status, r.Model, r.LatencyMs, r.OutputTokens, tpsStr, r.AgentID)
		}(entry)
	}

	wg.Wait()
	return results
}

func submitAndPoll(hubURL string, entry promptEntry, timeoutS int) testResult {
	taskID := uuid.New().String()
	result := testResult{
		TaskID:   taskID,
		Category: entry.Category,
		Prompt:   entry.Prompt,
		Model:    entry.Model,
	}

	submitStart := time.Now()

	payload := map[string]interface{}{
		"task_id":     taskID,
		"model":       entry.Model,
		"prompt":      entry.Prompt,
		"system":      entry.System,
		"max_tokens":  entry.MaxTokens,
		"temperature": entry.Temperature,
	}

	_, err := postJSON(fmt.Sprintf("%s/tasks", hubURL), payload)
	if err != nil {
		result.State = "FAILED"
		result.Error = fmt.Sprintf("submit error: %v", err)
		result.WallTimeMs = float64(time.Since(submitStart).Milliseconds())
		return result
	}

	// Poll
	deadline := time.Now().Add(time.Duration(timeoutS) * time.Second)
	interval := time.Second
	for time.Now().Before(deadline) {
		data, err := getJSON(fmt.Sprintf("%s/tasks/%s", hubURL, taskID))
		if err != nil {
			time.Sleep(interval)
			continue
		}
		state := str(data, "state")
		if state == "COMPLETE" {
			r, _ := data["result"].(map[string]interface{})
			if r == nil {
				r = data
			}
			result.State = "COMPLETE"
			result.Content = str(r, "content")
			result.Model = str(r, "model")
			result.InputTokens = int(num(r, "input_tokens"))
			result.OutputTokens = int(num(r, "output_tokens"))
			result.LatencyMs = num(r, "latency_ms")
			result.AgentID = str(r, "agent_id")
			result.WallTimeMs = float64(time.Since(submitStart).Milliseconds())
			if result.LatencyMs > 0 && result.OutputTokens > 0 {
				result.TPS = float64(result.OutputTokens) / (result.LatencyMs / 1000)
			}
			return result
		}
		if state == "FAILED" {
			r, _ := data["result"].(map[string]interface{})
			result.State = "FAILED"
			if r != nil {
				result.Error = str(r, "error")
			} else {
				result.Error = "unknown"
			}
			result.WallTimeMs = float64(time.Since(submitStart).Milliseconds())
			return result
		}
		time.Sleep(interval)
		if interval < 5*time.Second {
			interval = time.Duration(float64(interval) * 1.2)
		}
	}

	result.State = "TIMEOUT"
	result.Error = fmt.Sprintf("timed out after %ds", timeoutS)
	result.WallTimeMs = float64(time.Since(submitStart).Milliseconds())
	return result
}

func computeSummary(results []testResult, wallTimeS float64) map[string]interface{} {
	total := len(results)
	var completed, failed, timedOut int
	var totalLatency float64
	var totalTokens int
	agents := map[string]int{}
	models := map[string]bool{}

	for _, r := range results {
		switch r.State {
		case "COMPLETE":
			completed++
			totalLatency += r.LatencyMs
			totalTokens += r.OutputTokens
			if r.AgentID != "" {
				agents[r.AgentID]++
			}
			if r.Model != "" {
				models[r.Model] = true
			}
		case "FAILED":
			failed++
		default:
			timedOut++
		}
	}

	s := map[string]interface{}{
		"total":    total,
		"completed": completed,
		"failed":   failed,
		"timed_out": timedOut,
	}

	if completed > 0 {
		avgLatency := totalLatency / float64(completed)
		var avgTPS float64
		if totalLatency > 0 {
			avgTPS = float64(totalTokens) / (totalLatency / 1000)
		}
		s["total_tokens"] = totalTokens
		s["avg_latency_ms"] = avgLatency
		s["avg_tps"] = avgTPS
		s["wall_time_s"] = wallTimeS
		s["agents_used"] = len(agents)

		modelList := make([]string, 0, len(models))
		for m := range models {
			modelList = append(modelList, m)
		}
		sort.Strings(modelList)
		s["models_used"] = modelList
		s["agent_distribution"] = agents
	}

	return s
}
