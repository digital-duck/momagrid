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
	systemPrompt = "You are a professional translator. Translate accurately while preserving tone, nuance, and meaning. Output ONLY the translation, no commentary."
	promptTpl    = "Translate the following text into %s:\n\n%s"
)

type translationResult struct {
	Language     string  `json:"language"`
	State        string  `json:"state"`
	Translation  string  `json:"translation"`
	OutputTokens float64 `json:"output_tokens"`
	LatencyMs    float64 `json:"latency_ms"`
	AgentID      string  `json:"agent_id"`
	Error        string  `json:"error,omitempty"`
}

var client = &http.Client{Timeout: 30 * time.Second}

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
	languages := flag.String("langs", "French,German,Chinese,Spanish", "Comma-separated languages")
	model := flag.String("model", "llama3", "Model name")
	maxTokens := flag.Int("max-tokens", 1024, "Max output tokens")
	timeoutS := flag.Int("timeout", 120, "Timeout in seconds")
	file := flag.String("file", "", "Input text file")
	flag.Parse()

	var text string
	if *file != "" {
		data, err := os.ReadFile(*file)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading file: %v\n", err)
			os.Exit(1)
		}
		text = string(data)
	} else {
		args := flag.Args()
		if len(args) == 0 {
			fmt.Println("Usage: go run translate.go [flags] \"Text to translate\"")
			flag.PrintDefaults()
			os.Exit(1)
		}
		text = args[0]
	}

	langs := strings.Split(*languages, ",")
	fmt.Printf("Translating into %d languages using %s...\n", len(langs), *model)

	var wg sync.WaitGroup
	resultsChan := make(chan translationResult, len(langs))

	start := time.Now()

	for _, lang := range langs {
		wg.Add(1)
		go func(l string) {
			defer wg.Done()
			resultsChan <- translateOne(*hubURL, text, l, *model, *maxTokens, *timeoutS)
		}(strings.TrimSpace(lang))
	}

	go func() {
		wg.Wait()
		close(resultsChan)
	}()

	var results []translationResult
	for r := range resultsChan {
		results = append(results, r)
		if r.State == "COMPLETE" {
			fmt.Printf("  %-12s %4.0f tok  %5.1fs  agent=..%s\n",
				r.Language, r.OutputTokens, r.LatencyMs/1000.0, lastN(r.AgentID, 12))
		} else {
			fmt.Printf("  %-12s %s: %s\n", r.Language, r.State, r.Error)
		}
	}

	elapsed := time.Since(start).Seconds()
	fmt.Printf("\nDone in %.2fs\n", elapsed)
	saveResults("translate", map[string]interface{}{
		"timestamp": time.Now().Format(time.RFC3339),
		"hub":       *hubURL,
		"model":     *model,
		"languages": langs,
		"elapsed_s": elapsed,
		"results":   results,
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

func translateOne(hub, text, lang, model string, maxTokens, timeoutS int) translationResult {
	taskID := fmt.Sprintf("translate-%s-%s", strings.ToLower(lang[:3]), uuid.New().String()[:6])
	
	payload := map[string]interface{}{
		"task_id":    taskID,
		"model":      model,
		"prompt":     fmt.Sprintf(promptTpl, lang, text),
		"system":     systemPrompt,
		"max_tokens": maxTokens,
	}

	body, _ := json.Marshal(payload)
	resp, err := client.Post(fmt.Sprintf("%s/tasks", hub), "application/json", bytes.NewReader(body))
	if err != nil {
		return translationResult{Language: lang, State: "SUBMIT_FAILED", Error: err.Error()}
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return translationResult{Language: lang, State: "SUBMIT_REJECTED", Error: fmt.Sprintf("HTTP %d", resp.StatusCode)}
	}

	deadline := time.Now().Add(time.Duration(timeoutS) * time.Second)
	interval := 2 * time.Second

	for time.Now().Before(deadline) {
		getResp, err := client.Get(fmt.Sprintf("%s/tasks/%s", hub, taskID))
		if err != nil {
			time.Sleep(interval)
			continue
		}

		bodyBytes, _ := io.ReadAll(getResp.Body)
		getResp.Body.Close()

		if getResp.StatusCode == 404 {
			time.Sleep(interval)
			continue
		}

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
			return translationResult{
				Language:     lang,
				State:        "COMPLETE",
				Translation:  str(res, "content"),
				OutputTokens: num(res, "output_tokens"),
				LatencyMs:    num(res, "latency_ms"),
				AgentID:      str(res, "agent_id"),
			}
		}
		if state == "FAILED" {
			res, _ := task["result"].(map[string]interface{})
			errMsg := "unknown"
			if res != nil {
				errMsg = str(res, "error")
			}
			return translationResult{Language: lang, State: "FAILED", Error: errMsg}
		}

		time.Sleep(interval)
		if interval < 8*time.Second {
			interval = time.Duration(float64(interval) * 1.2)
		}
	}

	return translationResult{Language: lang, State: "TIMEOUT"}
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
