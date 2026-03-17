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
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

type route struct {
	Type     string
	Model    string
	Patterns []*regexp.Regexp
}

var routes = []route{
	{
		Type:  "math",
		Model: "mathstral",
		Patterns: []*regexp.Regexp{
			regexp.MustCompile(`(?i)\b(integral|derivative|calculus|theorem|proof|equation|solve|factor|matrix|eigen|prime|factorial|probability|statistics|algebra|geometry|trigonometry|logarithm|limit|series|vector|tensor)\b`),
			regexp.MustCompile(`\d+\s*[+\-*/^]\s*\d+`),
			regexp.MustCompile(`(?i)\bx\^?\d+\b`),
		},
	},
	{
		Type:  "code",
		Model: "qwen2.5-coder",
		Patterns: []*regexp.Regexp{
			regexp.MustCompile(`(?i)\b(function|class|def |import |algorithm|code|program|script|debug|refactor|implement|api|sql|query|regex|json|http|async|thread|recursion|sort|search)\b`),
			regexp.MustCompile("```"),
			regexp.MustCompile(`(?i)\bwrite a\b.*\b(in python|in javascript|in go|in rust|in java|in c\+\+)\b`),
		},
	},
	{
		Type:     "general",
		Model:    "llama3",
		Patterns: nil, // fallback
	},
}

var demoPrompts = []string{
	"What is the derivative of sin(x) * cos(x)?",
	"Write a Python function to find the longest common subsequence.",
	"Explain the causes of World War I in three sentences.",
	"Solve: 3x^2 - 12x + 9 = 0",
	"Implement a binary search tree insert method in Python.",
	"What is quantum entanglement?",
}

type routerResult struct {
	Prompt       string  `json:"prompt"`
	Type         string  `json:"type"`
	Model        string  `json:"model"`
	State        string  `json:"state"`
	Content      string  `json:"content"`
	LatencyS     float64 `json:"latency_s"`
	OutputTokens float64 `json:"output_tokens"`
	TPS          float64 `json:"tps"`
	AgentID      string  `json:"agent_id"`
	Error        string  `json:"error"`
}

var routerClient = &http.Client{Timeout: 30 * time.Second}

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
	promptArg := flag.String("prompt", "", "Single prompt to route")
	demo := flag.Bool("demo", false, "Run built-in demo prompts")
	model := flag.String("model", "", "Override auto-routing with a specific model")
	maxTokens := flag.Int("max-tokens", 512, "Max output tokens")
	timeoutS := flag.Int("timeout", 180, "Timeout in seconds")
	showResponse := flag.Bool("show-response", true, "Print model responses")
	flag.Parse()

	var prompts []string
	if *demo || (*promptArg == "" && len(flag.Args()) == 0) {
		prompts = demoPrompts
	} else if *promptArg != "" {
		prompts = []string{*promptArg}
	} else if len(flag.Args()) > 0 {
		prompts = []string{flag.Args()[0]}
	}

	overrideModel := strings.TrimSpace(*model)

	fmt.Printf("\n  Smart Router\n")
	fmt.Printf("    Hub: %s\n", *hubURL)
	fmt.Printf("    Prompts: %d\n\n", len(prompts))

	fmt.Println("  Routing decisions:")
	for _, p := range prompts {
		r := detectRoute(p)
		m := overrideModel
		if m == "" {
			m = r.Model
		}
		q := p
		if len(q) > 60 {
			q = q[:60]
		}
		fmt.Printf("    [%-8s] → %-20s  %s\n", r.Type, m, q)
	}
	fmt.Println()

	var wg sync.WaitGroup
	ch := make(chan routerResult, len(prompts))

	for _, p := range prompts {
		wg.Add(1)
		go func(prompt string) {
			defer wg.Done()
			ch <- routeAndRun(*hubURL, prompt, overrideModel, *maxTokens, *timeoutS)
		}(p)
	}

	go func() {
		wg.Wait()
		close(ch)
	}()

	var results []routerResult
	for r := range ch {
		results = append(results, r)
	}

	fmt.Println("  Results:")
	byType := make(map[string]int)
	completed := 0
	for _, r := range results {
		if r.State == "COMPLETE" {
			completed++
			byType[r.Type]++
			q := r.Prompt
			if len(q) > 80 {
				q = q[:80]
			}
			fmt.Printf("\n  [%-8s] %-20s %5.1fs %5.1ftps\n",
				r.Type, r.Model, r.LatencyS, r.TPS)
			fmt.Printf("  Q: %s\n", q)
			if *showResponse {
				ans := r.Content
				if len(ans) > 300 {
					ans = ans[:300]
				}
				fmt.Printf("  A: %s\n", ans)
			}
		} else {
			fmt.Printf("\n  [%-8s] %-20s %s: %s\n",
				r.Type, r.Model, r.State, r.Error)
		}
	}

	fmt.Printf("\n  %d/%d completed\n", completed, len(prompts))
	for t, cnt := range byType {
		fmt.Printf("    %s: %d tasks\n", t, cnt)
	}
	fmt.Println()
	saveResults("smart_router", map[string]interface{}{
		"timestamp": time.Now().Format(time.RFC3339),
		"hub":       *hubURL,
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

func detectRoute(prompt string) route {
	lower := strings.ToLower(prompt)
	for _, r := range routes[:len(routes)-1] { // skip fallback
		for _, pat := range r.Patterns {
			if pat.MatchString(lower) {
				return r
			}
		}
	}
	return routes[len(routes)-1]
}

func routeAndRun(hub, prompt, overrideModel string, maxTokens, timeoutS int) routerResult {
	r := detectRoute(prompt)
	model := overrideModel
	if model == "" {
		model = r.Model
	}
	taskType := r.Type

	taskID := fmt.Sprintf("route-%s", uuid.New().String()[:8])
	t0 := time.Now()

	payload := map[string]interface{}{
		"task_id":    taskID,
		"model":      model,
		"prompt":     prompt,
		"max_tokens": maxTokens,
	}
	body, _ := json.Marshal(payload)
	resp, err := routerClient.Post(fmt.Sprintf("%s/tasks", hub), "application/json", bytes.NewReader(body))
	if err != nil {
		return routerResult{Prompt: prompt, Type: taskType, Model: model, State: "SUBMIT_FAILED", Error: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return routerResult{Prompt: prompt, Type: taskType, Model: model, State: "SUBMIT_REJECTED"}
	}

	deadline := time.Now().Add(time.Duration(timeoutS) * time.Second)
	interval := 1500 * time.Millisecond

	for time.Now().Before(deadline) {
		getResp, err := routerClient.Get(fmt.Sprintf("%s/tasks/%s", hub, taskID))
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
			tps := 0.0
			if elapsed > 0 {
				tps = outTok / elapsed
			}
			q := prompt
			if len(q) > 80 {
				q = q[:80]
			}
			return routerResult{
				Prompt:       q,
				Type:         taskType,
				Model:        model,
				State:        "COMPLETE",
				Content:      str(res, "content"),
				LatencyS:     elapsed,
				OutputTokens: outTok,
				TPS:          tps,
				AgentID:      str(res, "agent_id"),
			}
		}
		if state == "FAILED" {
			res, _ := task["result"].(map[string]interface{})
			errMsg := ""
			if res != nil {
				errMsg = str(res, "error")
			}
			return routerResult{Prompt: prompt, Type: taskType, Model: model, State: "FAILED", Error: errMsg}
		}

		time.Sleep(interval)
		if interval < 8*time.Second {
			interval = time.Duration(float64(interval) * 1.2)
		}
	}

	return routerResult{Prompt: prompt, Type: taskType, Model: model, State: "TIMEOUT"}
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
