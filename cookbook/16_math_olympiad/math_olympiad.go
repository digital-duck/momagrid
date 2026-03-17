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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

const mathSystem = "You are a precise mathematics solver. Show your work briefly, " +
	"then state the final answer clearly prefixed with 'Answer:'. " +
	"Be concise but accurate."

type mathProblem struct {
	Q      string `json:"q"`
	Answer string `json:"answer"`
}

var mathProblems = map[string][]mathProblem{
	"easy": {
		{"What is 15% of 240?", "36"},
		{"Simplify: (x^2 - 9) / (x - 3)", "x+3"},
		{"What is the sum of interior angles of a hexagon?", "720"},
		{"Solve: 3x + 7 = 22", "5"},
		{"What is 12! / 10!?", "132"},
	},
	"medium": {
		{"Find the derivative of f(x) = x^3 - 4x^2 + 2x - 1", "3x^2-8x+2"},
		{"Solve the quadratic: x^2 - 5x + 6 = 0", "2,3"},
		{"What is the probability of rolling two sixes with two fair dice?", "1/36"},
		{"Integrate: ∫(2x + 3)dx", "x^2+3x+C"},
		{"If log₂(x) = 5, what is x?", "32"},
	},
	"hard": {
		{"Prove that √2 is irrational. Give the key step of the proof.", "contradiction"},
		{"Find the sum of the infinite geometric series: 1 + 1/2 + 1/4 + 1/8 + ...", "2"},
		{"How many ways can 8 people be seated in a circle?", "5040"},
		{"What is the Euler's formula relating e, π, i, 1, and 0?", "e^(iπ)+1=0"},
		{"Find all prime numbers p such that p^2 + 2 is also prime.", "3"},
	},
}

type mathResult struct {
	Model        string  `json:"model"`
	Q            string  `json:"q"`
	Expected     string  `json:"expected"`
	Response     string  `json:"response"`
	Correct      bool    `json:"correct"`
	State        string  `json:"state"`
	LatencyS     float64 `json:"latency_s"`
	OutputTokens float64 `json:"output_tokens"`
	TPS          float64 `json:"tps"`
	AgentID      string  `json:"agent_id"`
	Error        string  `json:"error"`
}

var mathClient = &http.Client{Timeout: 30 * time.Second}

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
	models := flag.String("models", "mathstral,qwen2-math", "Comma-separated model names")
	difficulty := flag.String("difficulty", "medium", "Difficulty: easy, medium, hard")
	maxTokens := flag.Int("max-tokens", 512, "Max output tokens")
	timeoutS := flag.Int("timeout", 180, "Timeout in seconds")
	flag.Parse()

	modelList := strings.Split(*models, ",")
	for i, m := range modelList {
		modelList[i] = strings.TrimSpace(m)
	}

	problems, ok := mathProblems[*difficulty]
	if !ok {
		problems = mathProblems["medium"]
	}

	fmt.Printf("\n  Math Olympiad — %s\n", strings.ToUpper(*difficulty))
	fmt.Printf("    Hub:       %s\n", *hubURL)
	fmt.Printf("    Models:    %v\n", modelList)
	fmt.Printf("    Problems:  %d\n\n", len(problems))

	// Submit all (model x problem) combinations in parallel
	type combo struct {
		model   string
		problem mathProblem
		idx     int
	}
	var combos []combo
	for _, m := range modelList {
		for i, p := range problems {
			combos = append(combos, combo{m, p, i})
		}
	}

	var wg sync.WaitGroup
	ch := make(chan mathResult, len(combos))
	for _, c := range combos {
		wg.Add(1)
		go func(cc combo) {
			defer wg.Done()
			ch <- solveOne(*hubURL, cc.model, cc.problem, *maxTokens, *timeoutS)
		}(c)
	}
	go func() {
		wg.Wait()
		close(ch)
	}()

	var results []mathResult
	for r := range ch {
		results = append(results, r)
	}

	// Group by model
	byModel := make(map[string][]mathResult)
	for _, m := range modelList {
		byModel[m] = make([]mathResult, len(problems))
	}
	for _, r := range results {
		// Find the problem index
		for i, p := range problems {
			if p.Q == r.Q && r.Model == r.Model {
				if byModel[r.Model][i].Q == "" {
					byModel[r.Model][i] = r
				}
				break
			}
		}
	}

	// Simpler: rebuild by iterating model order
	modelResults := make(map[string][]mathResult)
	for _, r := range results {
		modelResults[r.Model] = append(modelResults[r.Model], r)
	}

	// Print per-problem results
	header := fmt.Sprintf("  %-45s ", "Problem")
	for _, m := range modelList {
		header += fmt.Sprintf("  %-12s", m[:min(12, len(m))])
	}
	fmt.Println(header)
	sep := fmt.Sprintf("  %s ", strings.Repeat("-", 45))
	for range modelList {
		sep += fmt.Sprintf("  %s", strings.Repeat("-", 12))
	}
	fmt.Println(sep)

	for _, prob := range problems {
		q := prob.Q
		if len(q) > 44 {
			q = q[:44]
		}
		row := fmt.Sprintf("  %-45s", q)
		for _, m := range modelList {
			// Find result for this model+problem
			var found *mathResult
			for i := range modelResults[m] {
				if modelResults[m][i].Q == prob.Q {
					found = &modelResults[m][i]
					break
				}
			}
			if found != nil && found.State == "COMPLETE" {
				mark := "✗"
				if found.Correct {
					mark = "✓"
				}
				row += fmt.Sprintf("  %s %4.1fs %5.1ftps", mark, found.LatencyS, found.TPS)
			} else if found != nil {
				row += fmt.Sprintf("  %-12s", found.State)
			} else {
				row += fmt.Sprintf("  %-12s", "?")
			}
		}
		fmt.Println(row)
	}

	// Model summary
	fmt.Printf("\n  %-20s %7s %9s %9s %8s\n", "Model", "Score", "Avg Lat", "Avg TPS", "Tokens")
	fmt.Printf("  %s\n", strings.Repeat("-", 57))
	for _, m := range modelList {
		res := modelResults[m]
		var done []mathResult
		for _, r := range res {
			if r.State == "COMPLETE" {
				done = append(done, r)
			}
		}
		correct := 0
		for _, r := range done {
			if r.Correct {
				correct++
			}
		}
		score := fmt.Sprintf("%d/%d", correct, len(problems))
		avgLat, avgTPS, totalTok := 0.0, 0.0, 0.0
		for _, r := range done {
			avgLat += r.LatencyS
			avgTPS += r.TPS
			totalTok += r.OutputTokens
		}
		if len(done) > 0 {
			avgLat /= float64(len(done))
			avgTPS /= float64(len(done))
		}
		fmt.Printf("  %-20s %7s %8.1fs %9.1f %8.0f\n",
			m, score, avgLat, avgTPS, totalTok)
	}
	fmt.Println()
	saveResults("math_olympiad", map[string]interface{}{
		"timestamp":  time.Now().Format(time.RFC3339),
		"hub":        *hubURL,
		"models":     modelList,
		"difficulty": *difficulty,
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
	fmt.Printf("  Results saved: %s\n", path)
}

func solveOne(hub, model string, prob mathProblem, maxTokens, timeoutS int) mathResult {
	taskID := fmt.Sprintf("math-%s", uuid.New().String()[:8])
	t0 := time.Now()

	payload := map[string]interface{}{
		"task_id":    taskID,
		"model":      model,
		"prompt":     prob.Q,
		"system":     mathSystem,
		"max_tokens": maxTokens,
	}
	body, _ := json.Marshal(payload)
	resp, err := mathClient.Post(fmt.Sprintf("%s/tasks", hub), "application/json", bytes.NewReader(body))
	if err != nil {
		return mathResult{Model: model, Q: prob.Q, Expected: prob.Answer, State: "SUBMIT_FAILED", Error: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return mathResult{Model: model, Q: prob.Q, Expected: prob.Answer, State: "SUBMIT_REJECTED"}
	}

	deadline := time.Now().Add(time.Duration(timeoutS) * time.Second)
	interval := 1500 * time.Millisecond

	for time.Now().Before(deadline) {
		getResp, err := mathClient.Get(fmt.Sprintf("%s/tasks/%s", hub, taskID))
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
			content := str(res, "content")
			outTok := num(res, "output_tokens")
			tps := 0.0
			if elapsed > 0 {
				tps = outTok / elapsed
			}
			response := content
			if len(response) > 200 {
				response = response[:200]
			}
			return mathResult{
				Model:        model,
				Q:            prob.Q,
				Expected:     prob.Answer,
				Response:     response,
				Correct:      checkAnswer(content, prob.Answer),
				State:        "COMPLETE",
				LatencyS:     elapsed,
				OutputTokens: outTok,
				TPS:          tps,
				AgentID:      str(res, "agent_id"),
			}
		}
		if state == "FAILED" {
			return mathResult{Model: model, Q: prob.Q, Expected: prob.Answer, State: "FAILED"}
		}

		time.Sleep(interval)
		if interval < 8*time.Second {
			interval = time.Duration(float64(interval) * 1.2)
		}
	}

	return mathResult{Model: model, Q: prob.Q, Expected: prob.Answer, State: "TIMEOUT"}
}

// normalize strips spaces and lowercases so "x + 3" matches "x+3".
func normalize(s string) string {
	return strings.ToLower(strings.ReplaceAll(s, " ", ""))
}

func checkAnswer(response, expected string) bool {
	// Exact substring match (case-insensitive, space-normalized).
	if strings.Contains(normalize(response), normalize(expected)) {
		return true
	}
	// Also try without space normalization in case expected has multi-word phrases.
	responseLower := strings.ToLower(response)
	expectedLower := strings.ToLower(expected)
	if strings.Contains(responseLower, expectedLower) {
		return true
	}
	// For comma-separated answers like "2,3" check each part appears in response.
	parts := strings.Split(expectedLower, ",")
	if len(parts) > 1 {
		allFound := true
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" && !strings.Contains(responseLower, p) {
				allFound = false
				break
			}
		}
		if allFound {
			return true
		}
	}
	// Numeric check: parse expected as a single number and scan response.
	expNum, err := strconv.ParseFloat(strings.ReplaceAll(expected, ",", ""), 64)
	if err == nil {
		re := regexp.MustCompile(`-?\d+\.?\d*`)
		nums := re.FindAllString(response, -1)
		for _, n := range nums {
			if f, err := strconv.ParseFloat(n, 64); err == nil {
				if abs(f-expNum) < 0.01 {
					return true
				}
			}
		}
	}
	return false
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
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
