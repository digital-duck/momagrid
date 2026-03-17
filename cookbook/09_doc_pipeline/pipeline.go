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

const (
	docSystem = "You are an expert document analyst. Produce clear, well-structured summaries. " +
		"Be specific and cite details from the text."

	docPromptTpl = `## Document text (%d characters)

%s

---

Produce a structured summary with:

### Overview
What is this document about? (2-3 sentences)

### Key Points
The most important facts, findings, or arguments (bullet list).

### Details
Expand on the key points with specific details from the text.

### Conclusion
What are the main takeaways?`
)

var docClient = &http.Client{Timeout: 30 * time.Second}

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

type docResult struct {
	State        string  `json:"state"`
	Content      string  `json:"content"`
	OutputTokens float64 `json:"output_tokens"`
	LatencyMs    float64 `json:"latency_ms"`
	AgentID      string  `json:"agent_id"`
	Error        string  `json:"error"`
}

func main() {
	hubURL := flag.String("hub", defaultHubURL(), "Hub URL")
	model := flag.String("model", "llama3", "Model name")
	maxTokens := flag.Int("max-tokens", 4096, "Max output tokens")
	timeoutS := flag.Int("timeout", 300, "Timeout in seconds")
	file := flag.String("file", "", "Text file to summarize")
	flag.Parse()

	var text string
	var filename string

	if *file != "" {
		data, err := os.ReadFile(*file)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading file: %v\n", err)
			os.Exit(1)
		}
		text = string(data)
		filename = *file
	} else {
		args := flag.Args()
		if len(args) > 0 {
			text = args[0]
			filename = "stdin"
		} else {
			// Use a demo text
			text = `Artificial Intelligence and Machine Learning

Machine learning is a subset of artificial intelligence that enables systems to learn and
improve from experience without being explicitly programmed. It focuses on developing
computer programs that can access data and use it to learn for themselves.

The process begins with observations or data, such as examples, direct experience, or
instruction, so that computers can look for patterns in data and make better decisions
in the future. The primary aim is to allow computers to learn automatically without
human intervention or assistance.

Deep learning is part of a broader family of machine learning methods based on artificial
neural networks with representation learning. Learning can be supervised, semi-supervised
or unsupervised. Deep learning architectures such as convolutional neural networks,
recurrent neural networks, and transformers have been applied to fields including
computer vision, natural language processing, and speech recognition.`
			filename = "demo_text.txt"
		}
	}

	fmt.Printf("\n  Document Pipeline\n")
	fmt.Printf("    Source:  %s\n", filename)
	fmt.Printf("    Hub:     %s\n", *hubURL)
	fmt.Printf("    Model:   %s\n\n", *model)

	// Step 1: Prepare (show char count)
	fmt.Printf("  [1/3] Preparing text... %d chars\n", len(text))

	// Step 2: Summarize on grid
	fmt.Printf("  [2/3] Summarizing on grid (%s)...", *model)
	result := submitAndWait(*hubURL, text, *model, *maxTokens, *timeoutS)
	if result.State == "COMPLETE" {
		fmt.Printf(" %.0f tokens  %.0fms\n", result.OutputTokens, result.LatencyMs)
	} else {
		fmt.Printf(" %s: %s\n", result.State, result.Error)
		return
	}

	// Step 3: Print output
	fmt.Printf("  [3/3] Formatting output...\n")
	fmt.Printf("\n  %s\n", result.Content)
	fmt.Printf("\n  Done! Agent: ..%s\n\n", lastN(result.AgentID, 12))
	saveResults("doc_pipeline", map[string]interface{}{
		"timestamp": time.Now().Format(time.RFC3339),
		"hub":       *hubURL,
		"model":     *model,
		"source":    filename,
		"result":    result,
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

func submitAndWait(hub, text, model string, maxTokens, timeoutS int) docResult {
	taskID := fmt.Sprintf("doc-%s", uuid.New().String()[:8])
	prompt := fmt.Sprintf(docPromptTpl, len(text), text)

	payload := map[string]interface{}{
		"task_id":    taskID,
		"model":      model,
		"prompt":     prompt,
		"system":     docSystem,
		"max_tokens": maxTokens,
	}
	body, _ := json.Marshal(payload)
	resp, err := docClient.Post(fmt.Sprintf("%s/tasks", hub), "application/json", bytes.NewReader(body))
	if err != nil {
		return docResult{State: "SUBMIT_FAILED", Error: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return docResult{State: "SUBMIT_REJECTED", Error: fmt.Sprintf("HTTP %d", resp.StatusCode)}
	}

	deadline := time.Now().Add(time.Duration(timeoutS) * time.Second)
	interval := 2 * time.Second

	for time.Now().Before(deadline) {
		getResp, err := docClient.Get(fmt.Sprintf("%s/tasks/%s", hub, taskID))
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
			return docResult{
				State:        "COMPLETE",
				Content:      str(res, "content"),
				OutputTokens: num(res, "output_tokens"),
				LatencyMs:    num(res, "latency_ms"),
				AgentID:      str(res, "agent_id"),
			}
		}
		if state == "FAILED" {
			res, _ := task["result"].(map[string]interface{})
			errMsg := ""
			if res != nil {
				errMsg = str(res, "error")
			}
			return docResult{State: "FAILED", Error: errMsg}
		}

		time.Sleep(interval)
		if interval < 10*time.Second {
			interval = time.Duration(float64(interval) * 1.3)
		}
	}

	return docResult{State: "TIMEOUT"}
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
