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
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

const langSystem = "You are a helpful assistant. Answer the question in the same language it was asked. " +
	"Be clear and concise — 2-4 sentences maximum."

type language struct {
	Code string
	Q    string
}

var languages = map[string]language{
	"English":    {"en", "What is artificial intelligence?"},
	"Chinese":    {"zh", "人工智能是什么？"},
	"Spanish":    {"es", "¿Qué es la inteligencia artificial?"},
	"French":     {"fr", "Qu'est-ce que l'intelligence artificielle?"},
	"Arabic":     {"ar", "ما هو الذكاء الاصطناعي؟"},
	"Hindi":      {"hi", "कृत्रिम बुद्धिमत्ता क्या है?"},
	"Portuguese": {"pt", "O que é inteligência artificial?"},
	"Russian":    {"ru", "Что такое искусственный интеллект?"},
	"Japanese":   {"ja", "人工知能とは何ですか？"},
	"German":     {"de", "Was ist künstliche Intelligenz?"},
}

type langResult struct {
	Language     string
	Question     string
	State        string
	Answer       string
	LatencyS     float64
	OutputTokens float64
	TPS          float64
	AgentID      string
	Error        string
}

var langClient = &http.Client{Timeout: 30 * time.Second}

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
	model := flag.String("model", "llama3", "Model name")
	question := flag.String("question", "", "Custom question to ask in all languages")
	langsFlag := flag.String("languages", "", "Comma-separated languages (default: all 10)")
	maxTokens := flag.Int("max-tokens", 256, "Max output tokens")
	timeoutS := flag.Int("timeout", 300, "Timeout in seconds")
	flag.Parse()

	// Also check positional args
	if *question == "" && len(flag.Args()) > 0 {
		*question = flag.Args()[0]
	}

	// Select languages
	var selectedLangs []string
	if *langsFlag != "" {
		for _, l := range strings.Split(*langsFlag, ",") {
			l = strings.TrimSpace(l)
			if _, ok := languages[l]; ok {
				selectedLangs = append(selectedLangs, l)
			}
		}
	}
	if len(selectedLangs) == 0 {
		for l := range languages {
			selectedLangs = append(selectedLangs, l)
		}
		sort.Strings(selectedLangs)
	}

	// Build queries
	queries := make(map[string]string)
	for _, lang := range selectedLangs {
		if *question != "" {
			queries[lang] = *question
		} else {
			queries[lang] = languages[lang].Q
		}
	}

	fmt.Printf("\n  Language Accessibility Demo\n")
	fmt.Printf("    Hub:       %s\n", *hubURL)
	fmt.Printf("    Model:     %s\n", *model)
	fmt.Printf("    Languages: %d\n\n", len(queries))

	wallStart := time.Now()

	var wg sync.WaitGroup
	ch := make(chan langResult, len(queries))

	for lang, q := range queries {
		wg.Add(1)
		go func(language, question string) {
			defer wg.Done()
			r := askOne(*hubURL, language, question, *model, *maxTokens, *timeoutS)
			ch <- r
			if r.State == "COMPLETE" {
				fmt.Printf("    %-12s %4.0f tok  %5.1fs\n",
					language, r.OutputTokens, r.LatencyS)
			} else {
				fmt.Printf("    %-12s %s\n", language, r.State)
			}
		}(lang, q)
	}

	go func() {
		wg.Wait()
		close(ch)
	}()

	var results []langResult
	for r := range ch {
		results = append(results, r)
	}

	wallTime := time.Since(wallStart).Seconds()

	// Sort and print answers
	sort.Slice(results, func(i, j int) bool {
		return results[i].Language < results[j].Language
	})

	var completed []langResult
	for _, r := range results {
		if r.State == "COMPLETE" {
			completed = append(completed, r)
		}
	}

	fmt.Printf("\n  %s\n", strings.Repeat("=", 60))
	for _, r := range completed {
		fmt.Printf("\n  [%s]\n", r.Language)
		q := r.Question
		if len(q) > 80 {
			q = q[:80]
		}
		fmt.Printf("  Q: %s\n", q)
		ans := r.Answer
		if len(ans) > 300 {
			ans = ans[:300]
		}
		fmt.Printf("  A: %s\n", ans)
	}

	fmt.Printf("\n  %s\n", strings.Repeat("-", 60))
	fmt.Printf("  %d/%d languages answered in %.1fs wall time\n",
		len(completed), len(queries), wallTime)
	fmt.Print("  (All dispatched in parallel — wall time ≈ slowest single response)\n\n")
	saveResults("language_grid", map[string]interface{}{
		"timestamp":   time.Now().Format(time.RFC3339),
		"hub":         *hubURL,
		"model":       *model,
		"wall_time_s": wallTime,
		"results":     results,
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

func askOne(hub, lang, question, model string, maxTokens, timeoutS int) langResult {
	code := lang
	if len(code) >= 3 {
		code = strings.ToLower(code[:3])
	}
	taskID := fmt.Sprintf("lang-%s-%s", code, uuid.New().String()[:6])
	t0 := time.Now()

	payload := map[string]interface{}{
		"task_id":    taskID,
		"model":      model,
		"prompt":     question,
		"system":     langSystem,
		"max_tokens": maxTokens,
	}
	body, _ := json.Marshal(payload)
	resp, err := langClient.Post(fmt.Sprintf("%s/tasks", hub), "application/json", bytes.NewReader(body))
	if err != nil {
		return langResult{Language: lang, Question: question, State: "SUBMIT_FAILED", Error: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return langResult{Language: lang, Question: question, State: "SUBMIT_REJECTED"}
	}

	deadline := time.Now().Add(time.Duration(timeoutS) * time.Second)
	interval := 1500 * time.Millisecond

	for time.Now().Before(deadline) {
		getResp, err := langClient.Get(fmt.Sprintf("%s/tasks/%s", hub, taskID))
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
			return langResult{
				Language:     lang,
				Question:     question,
				State:        "COMPLETE",
				Answer:       str(res, "content"),
				LatencyS:     elapsed,
				OutputTokens: outTok,
				TPS:          tps,
				AgentID:      str(res, "agent_id"),
			}
		}
		if state == "FAILED" {
			return langResult{Language: lang, Question: question, State: "FAILED"}
		}

		time.Sleep(interval)
		if interval < 8*time.Second {
			interval = time.Duration(float64(interval) * 1.2)
		}
	}

	return langResult{Language: lang, Question: question, State: "TIMEOUT"}
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
