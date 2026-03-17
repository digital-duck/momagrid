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
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

var allModels = []string{
	"llama3",
	"llama3.1",
	"mistral",
	"mathstral",
	"qwen3",
	"qwen2.5",
	"qwen2.5-coder",
	"qwen2-math",
	"deepseek-r1",
	"deepseek-coder-v2",
	"gemma3",
	"phi4",
	"phi4-mini",
	"phi3",
}

type benchmark struct {
	ID        string
	Domain    string
	Prompt    string
	System    string
	MaxTokens int
}

var benchmarks = []benchmark{
	{
		ID:        "general",
		Domain:    "General knowledge",
		Prompt:    "Explain why the sky is blue in exactly three sentences.",
		System:    "You are a concise science communicator.",
		MaxTokens: 200,
	},
	{
		ID:     "reasoning",
		Domain: "Logical reasoning",
		Prompt: "Alice is taller than Bob. Bob is taller than Carol. " +
			"Is Alice taller than Carol? Explain your reasoning.",
		System:    "You are a logical reasoning assistant. Be brief and precise.",
		MaxTokens: 150,
	},
	{
		ID: "math",
		Domain: "Mathematics",
		Prompt: "Solve: A train travels 120 km at 60 km/h, then 80 km at 40 km/h. " +
			"What is the average speed for the whole journey? Show working.",
		System:    "You are a mathematics tutor. Show step-by-step working.",
		MaxTokens: 300,
	},
	{
		ID: "code",
		Domain: "Code generation",
		Prompt: "Write a Python function `flatten(lst)` that recursively flattens " +
			"a nested list of arbitrary depth. Include a docstring and two examples.",
		System:    "You are an expert Python developer. Output only code and docstring.",
		MaxTokens: 350,
	},
	{
		ID:        "multilingual",
		Domain:    "Multilingual",
		Prompt:    "Translate to French, Spanish, and Japanese: 'Distributed AI inference makes powerful models accessible to everyone.'",
		System:    "You are a professional translator. Output each translation on its own line labelled with the language.",
		MaxTokens: 200,
	},
	{
		ID: "summarise",
		Domain: "Summarisation",
		Prompt: "Summarise in one sentence: Transformer models use self-attention mechanisms " +
			"to weigh the relevance of each token in a sequence relative to every other " +
			"token, enabling them to capture long-range dependencies more effectively " +
			"than recurrent neural networks.",
		System:    "You are a technical writer. Produce exactly one sentence.",
		MaxTokens: 100,
	},
}

var probeBenchmark = benchmark{
	ID:        "probe",
	Domain:    "Probe",
	Prompt:    "Reply with exactly: 'Model online.'",
	System:    "",
	MaxTokens: 20,
}

type divResult struct {
	Model        string
	BenchmarkID  string
	Domain       string
	Content      string
	OutputTokens float64
	LatencyMs    float64
	TPS          float64
	WallS        float64
	AgentID      string
	Error        string
}

var divClient = &http.Client{Timeout: 30 * time.Second}

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
	modelsFlag := flag.String("models", strings.Join(allModels, ","), "Comma-separated models")
	timeoutS := flag.Int("timeout", 180, "Per-task timeout in seconds")
	probe := flag.Bool("probe", false, "Quick probe only")
	skipErrors := flag.Bool("skip-errors", false, "Continue if a model errors")
	flag.Parse()

	modelList := []string{}
	for _, m := range strings.Split(*modelsFlag, ",") {
		m = strings.TrimSpace(m)
		if m != "" {
			modelList = append(modelList, m)
		}
	}

	selectedBenchmarks := benchmarks
	if *probe {
		selectedBenchmarks = []benchmark{probeBenchmark}
	}

	total := len(modelList) * len(selectedBenchmarks)
	fmt.Printf("Model diversity benchmark\n")
	fmt.Printf("  Hub:        %s\n", *hubURL)
	fmt.Printf("  Models:     %d\n", len(modelList))
	benchLabel := "full suite"
	if *probe {
		benchLabel = "probe only"
	}
	fmt.Printf("  Benchmarks: %d (%s)\n", len(selectedBenchmarks), benchLabel)
	fmt.Printf("  Tasks:      %d\n", total)
	fmt.Printf("  Timeout:    %ds per task\n\n", *timeoutS)

	var results []divResult

	for _, model := range modelList {
		fmt.Printf("[%s]\n", model)

		// Warmup: load the model into VRAM before timing begins.
		// The first request after a model switch incurs load time; we discard it.
		fmt.Printf("  %-25s ", "warmup (loading)...")
		w := runOne(*hubURL, model, probeBenchmark, *timeoutS)
		if w.Error != "" {
			fmt.Printf("ERROR  %s\n", w.Error)
			if !*skipErrors {
				fmt.Printf("  Skipping %s (warmup failed)\n\n", model)
				continue
			}
		} else {
			fmt.Printf("ok  (%.0fms load time excluded)\n", w.LatencyMs)
		}

		modelFailed := false
		for _, bench := range selectedBenchmarks {
			domain := bench.Domain
			for len(domain) < 25 {
				domain += " "
			}
			fmt.Printf("  %s ", domain)

			r := runOne(*hubURL, model, bench, *timeoutS)
			results = append(results, r)

			if r.Error != "" {
				errMsg := r.Error
				if len(errMsg) > 60 {
					errMsg = errMsg[:60]
				}
				fmt.Printf("ERROR  %s\n", errMsg)
				if !*skipErrors && !*probe {
					fmt.Printf("  Skipping remaining benchmarks for %s (use --skip-errors to continue)\n", model)
					modelFailed = true
					break
				}
			} else {
				fmt.Printf("ok  %5.1f TPS  %6.0fms  %.0f tok\n",
					r.TPS, r.LatencyMs, r.OutputTokens)
			}
		}
		if !modelFailed {
			_ = modelFailed // suppress unused warning
		}
		fmt.Println()
	}

	// Summary table
	fmt.Printf("%s\n", strings.Repeat("=", 60))
	fmt.Printf("%-22s %4s %8s %8s %7s\n", "MODEL", "PASS", "AVG TPS", "TOKENS", "ERRORS")
	fmt.Printf("%s\n", strings.Repeat("-", 60))

	for _, model := range modelList {
		var modelResults []divResult
		for _, r := range results {
			if r.Model == model {
				modelResults = append(modelResults, r)
			}
		}
		var okRows []divResult
		errors := 0
		for _, r := range modelResults {
			if r.Error == "" {
				okRows = append(okRows, r)
			} else {
				errors++
			}
		}
		avgTPS := 0.0
		totTok := 0.0
		for _, r := range okRows {
			avgTPS += r.TPS
			totTok += r.OutputTokens
		}
		if len(okRows) > 0 {
			avgTPS /= float64(len(okRows))
		}
		status := "OK"
		if errors > 0 {
			status = fmt.Sprintf("%d ERR", errors)
		}
		fmt.Printf("%-22s %4d/%-3d %7.1f %8.0f %7s\n",
			model, len(okRows), len(selectedBenchmarks), avgTPS, totTok, status)
	}
	fmt.Printf("%s\n", strings.Repeat("=", 60))
	saveResults("model_diversity", map[string]interface{}{
		"timestamp":  time.Now().Format(time.RFC3339),
		"hub":        *hubURL,
		"models":     modelList,
		"benchmarks": len(selectedBenchmarks),
		"results":    results,
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

func runOne(hub, model string, bench benchmark, timeoutS int) divResult {
	taskID := fmt.Sprintf("div-%s", uuid.New().String()[:10])
	t0 := time.Now()

	payload := map[string]interface{}{
		"task_id":    taskID,
		"model":      model,
		"prompt":     bench.Prompt,
		"system":     bench.System,
		"max_tokens": bench.MaxTokens,
	}
	body, _ := json.Marshal(payload)
	resp, err := divClient.Post(fmt.Sprintf("%s/tasks", hub), "application/json", bytes.NewReader(body))
	if err != nil {
		return divResult{Model: model, BenchmarkID: bench.ID, Domain: bench.Domain, Error: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return divResult{Model: model, BenchmarkID: bench.ID, Domain: bench.Domain,
			Error: fmt.Sprintf("HTTP %d", resp.StatusCode)}
	}

	deadline := time.Now().Add(time.Duration(timeoutS) * time.Second)
	interval := 2 * time.Second

	for time.Now().Before(deadline) {
		getResp, err := divClient.Get(fmt.Sprintf("%s/tasks/%s", hub, taskID))
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
			wallS := time.Since(t0).Seconds()
			outTok := num(res, "output_tokens")
			latMs := num(res, "latency_ms")
			tps := 0.0
			if latMs > 0 {
				tps = outTok / (latMs / 1000)
			}
			return divResult{
				Model:        model,
				BenchmarkID:  bench.ID,
				Domain:       bench.Domain,
				Content:      str(res, "content"),
				OutputTokens: outTok,
				LatencyMs:    latMs,
				TPS:          tps,
				WallS:        wallS,
				AgentID:      str(res, "agent_id"),
			}
		}
		if state == "FAILED" {
			errMsg := "failed"
			if res, ok := task["result"].(map[string]interface{}); ok {
				errMsg = str(res, "error")
			}
			return divResult{Model: model, BenchmarkID: bench.ID, Domain: bench.Domain, Error: errMsg}
		}

		time.Sleep(interval)
		if interval < 8*time.Second {
			interval = time.Duration(float64(interval) * 1.2)
		}
	}

	return divResult{Model: model, BenchmarkID: bench.ID, Domain: bench.Domain, Error: "timeout"}
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
