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
	benchPrompt = "Explain gradient descent in three sentences."
	benchMaxTok = 150
)

var benchClient = &http.Client{Timeout: 30 * time.Second}

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

type benchResult struct {
	Model        string
	State        string
	OutputTokens float64
	LatencyS     float64
	TPS          float64
	Error        string
}

func main() {
	hubURL := flag.String("hub", defaultHubURL(), "Hub URL")
	models := flag.String("models", "llama3,mistral,phi3", "Comma-separated model names")
	timeoutS := flag.Int("timeout", 120, "Timeout in seconds")
	flag.Parse()

	modelList := strings.Split(*models, ",")
	for i, m := range modelList {
		modelList[i] = strings.TrimSpace(m)
	}

	fmt.Printf("Benchmarking %v on %s\n\n", modelList, *hubURL)

	var wg sync.WaitGroup
	ch := make(chan benchResult, len(modelList))

	for _, m := range modelList {
		wg.Add(1)
		go func(model string) {
			defer wg.Done()
			ch <- runBenchmark(*hubURL, model, *timeoutS)
		}(m)
	}

	go func() {
		wg.Wait()
		close(ch)
	}()

	var results []benchResult
	for r := range ch {
		results = append(results, r)
	}

	// Print in original model order
	ordered := make([]benchResult, 0, len(modelList))
	for _, m := range modelList {
		for _, r := range results {
			if r.Model == m {
				ordered = append(ordered, r)
				break
			}
		}
	}

	fmt.Printf("%-15s %-10s %8s %10s %8s\n", "MODEL", "STATE", "TOKENS", "LATENCY", "TPS")
	fmt.Println(strings.Repeat("-", 55))
	for _, r := range ordered {
		fmt.Printf("%-15s %-10s %8.0f %10.2f %8.1f\n",
			r.Model, r.State, r.OutputTokens, r.LatencyS, r.TPS)
	}
	saveResults("benchmark", map[string]interface{}{
		"timestamp": time.Now().Format(time.RFC3339),
		"hub":       *hubURL,
		"models":    modelList,
		"prompt":    benchPrompt,
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
	fmt.Printf("Results saved: %s\n", path)
}

func runBenchmark(hub, model string, timeoutS int) benchResult {
	taskID := uuid.New().String()
	start := time.Now()

	payload := map[string]interface{}{
		"task_id":    taskID,
		"model":      model,
		"prompt":     benchPrompt,
		"max_tokens": benchMaxTok,
	}
	body, _ := json.Marshal(payload)
	resp, err := benchClient.Post(fmt.Sprintf("%s/tasks", hub), "application/json", bytes.NewReader(body))
	if err != nil {
		return benchResult{Model: model, State: "SUBMIT_FAILED", Error: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return benchResult{Model: model, State: "SUBMIT_REJECTED", Error: fmt.Sprintf("HTTP %d", resp.StatusCode)}
	}

	deadline := time.Now().Add(time.Duration(timeoutS) * time.Second)
	interval := 2 * time.Second

	for time.Now().Before(deadline) {
		getResp, err := benchClient.Get(fmt.Sprintf("%s/tasks/%s", hub, taskID))
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
			elapsed := time.Since(start).Seconds()
			outTok := num(res, "output_tokens")
			tps := 0.0
			if elapsed > 0 {
				tps = outTok / elapsed
			}
			return benchResult{
				Model:        model,
				State:        "COMPLETE",
				OutputTokens: outTok,
				LatencyS:     elapsed,
				TPS:          tps,
			}
		}
		if state == "FAILED" {
			return benchResult{Model: model, State: "FAILED"}
		}

		time.Sleep(interval)
		if interval < 8*time.Second {
			interval = time.Duration(float64(interval) * 1.3)
		}
	}

	return benchResult{Model: model, State: "TIMEOUT"}
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
