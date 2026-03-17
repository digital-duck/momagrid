// Recipe 06: arXiv Paper Digest — fetch abstracts and generate structured digests on the grid.
//
// Accepts arXiv IDs or full arxiv.org URLs. Fetches the abstract from arXiv,
// submits a structured digest task to the grid, and prints results.
//
// Usage:
//
//	go run digest.go 2312.12345 2401.99999
//	go run digest.go https://arxiv.org/abs/2409.11111
//	go run digest.go --model llama3 --hub http://localhost:9000 2312.12345
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
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

const digestSystem = "You are a rigorous academic reviewer who produces clear, structured paper digests. " +
	"Write precisely — avoid vague phrases. Ground every claim in the provided text."

const digestPromptTpl = `## arXiv paper abstract

%s

---

Produce a structured digest with these sections:

### 1. Title & Core Claim
What does this paper claim to contribute? (1–2 sentences)

### 2. Problem Statement
What specific problem does it address and why does it matter?

### 3. Methodology
Key technical approach and design choices.

### 4. Key Results
Most important quantitative or qualitative findings.

### 5. Limitations & Open Questions
What does the paper leave unresolved?

### 6. One-Line Summary
A single crisp sentence suitable for a literature review citation.`

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

var arxivIDRe = regexp.MustCompile(`(\d{4}\.\d{4,5}(?:v\d+)?)`)

type paperResult struct {
	ID      string
	Title   string
	State   string
	Digest  string
	Tokens  int
	Latency float64
	Error   string
}

func parseArxivID(raw string) string {
	m := arxivIDRe.FindString(raw)
	return m
}

func fetchAbstract(arxivID string) (title, abstract string, err error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get("https://arxiv.org/abs/" + arxivID)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(resp.Body)
	html := string(bodyBytes)

	// Extract title
	titleRe := regexp.MustCompile(`<h1 class="title[^"]*">\s*(?:<span[^>]*>[^<]*</span>\s*)?([^<]+)`)
	if m := titleRe.FindStringSubmatch(html); len(m) > 1 {
		title = strings.TrimSpace(m[1])
	}
	if title == "" {
		title = arxivID
	}

	// Extract abstract
	abstractRe := regexp.MustCompile(`<blockquote class="abstract[^"]*">\s*(?:<span[^>]*>Abstract:</span>\s*)?(.+?)</blockquote>`)
	if m := abstractRe.FindStringSubmatch(html); len(m) > 1 {
		// Strip HTML tags
		tagRe := regexp.MustCompile(`<[^>]+>`)
		abstract = strings.TrimSpace(tagRe.ReplaceAllString(m[1], " "))
		// Collapse whitespace
		spaceRe := regexp.MustCompile(`\s+`)
		abstract = spaceRe.ReplaceAllString(abstract, " ")
	}
	if abstract == "" {
		return title, "", fmt.Errorf("abstract not found on arxiv page")
	}
	return title, abstract, nil
}

func submitTask(hub, taskID, prompt, model string, maxTokens int) error {
	client := &http.Client{Timeout: 10 * time.Second}
	body, _ := json.Marshal(map[string]interface{}{
		"task_id":     taskID,
		"model":       model,
		"prompt":      prompt,
		"system":      digestSystem,
		"max_tokens":  maxTokens,
		"temperature": 0.3,
	})
	resp, err := client.Post(hub+"/tasks", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func pollTask(hub, taskID string, timeoutS int) map[string]interface{} {
	client := &http.Client{Timeout: 10 * time.Second}
	deadline := time.Now().Add(time.Duration(timeoutS) * time.Second)
	interval := 5 * time.Second
	for time.Now().Before(deadline) {
		time.Sleep(interval)
		interval = minDur(time.Duration(float64(interval)*1.2), 20*time.Second)

		r, err := client.Get(fmt.Sprintf("%s/tasks/%s", hub, taskID))
		if err != nil {
			continue
		}
		var data map[string]interface{}
		json.NewDecoder(r.Body).Decode(&data)
		r.Body.Close()
		state, _ := data["state"].(string)
		if state == "COMPLETE" || state == "FAILED" {
			return data
		}
	}
	return map[string]interface{}{"state": "TIMEOUT"}
}

func main() {
	hub := flag.String("hub", defaultHubURL(), "Hub URL")
	model := flag.String("model", "llama3", "Model to use")
	maxTokens := flag.Int("max-tokens", 2048, "Max output tokens")
	timeout := flag.Int("timeout", 600, "Timeout per paper (seconds)")
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		fmt.Println("Usage: go run digest.go [flags] <arxiv_id_or_url> ...")
		flag.PrintDefaults()
		return
	}
	*hub = strings.TrimRight(*hub, "/")

	fmt.Printf("\n  arXiv Paper Digest\n")
	fmt.Printf("    Hub:    %s\n", *hub)
	fmt.Printf("    Model:  %s\n", *model)
	fmt.Printf("    Papers: %d\n\n", len(args))

	type pendingPaper struct {
		id     string
		title  string
		taskID string
	}
	var pending []pendingPaper

	// Phase 1: fetch abstracts and submit tasks
	for _, raw := range args {
		id := parseArxivID(raw)
		if id == "" {
			fmt.Printf("  [%s] could not parse arXiv ID — skipping\n", raw)
			continue
		}
		fmt.Printf("  [%s] fetching abstract... ", id)
		title, abstract, err := fetchAbstract(id)
		if err != nil {
			fmt.Printf("FAILED: %v\n", err)
			continue
		}
		fmt.Printf("ok  \"%s\"\n", title[:min(len(title), 60)])

		taskID := "digest-" + id[:min(len(id), 10)] + "-" + uuid.New().String()[:6]
		prompt := fmt.Sprintf(digestPromptTpl, abstract)
		fmt.Printf("  [%s] submitting to grid... ", id)
		if err := submitTask(*hub, taskID, prompt, *model, *maxTokens); err != nil {
			fmt.Printf("FAILED: %v\n", err)
			continue
		}
		fmt.Printf("task_id=%s\n", taskID)
		pending = append(pending, pendingPaper{id: id, title: title, taskID: taskID})
	}

	if len(pending) == 0 {
		fmt.Println("  No papers submitted.")
		return
	}

	// Phase 2: poll for results
	fmt.Printf("\n  Waiting for %d digest(s)...\n\n", len(pending))
	var results []paperResult
	for _, p := range pending {
		fmt.Printf("  [%s] polling... ", p.id)
		t0 := time.Now()
		data := pollTask(*hub, p.taskID, *timeout)
		state, _ := data["state"].(string)
		r, _ := data["result"].(map[string]interface{})
		elapsed := time.Since(t0).Seconds()

		pr := paperResult{ID: p.id, Title: p.title, State: state}
		if state == "COMPLETE" && r != nil {
			content, _ := r["content"].(string)
			in, _ := r["input_tokens"].(float64)
			out, _ := r["output_tokens"].(float64)
			pr.Digest = content
			pr.Tokens = int(in + out)
			pr.Latency = elapsed
			fmt.Printf("done  %d tokens  %.1fs\n", pr.Tokens, elapsed)
		} else {
			if r != nil {
				pr.Error, _ = r["error"].(string)
			}
			fmt.Printf("FAILED: state=%s\n", state)
		}
		results = append(results, pr)
	}

	// Phase 3: print results
	fmt.Printf("\n  %s\n", strings.Repeat("=", 60))
	for _, pr := range results {
		fmt.Printf("\n  [%s] %s\n", pr.ID, pr.Title)
		fmt.Printf("  https://arxiv.org/abs/%s\n\n", pr.ID)
		if pr.State == "COMPLETE" {
			fmt.Println(pr.Digest)
		} else {
			fmt.Printf("  ERROR: %s\n", pr.Error)
		}
		fmt.Println()
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func minDur(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
