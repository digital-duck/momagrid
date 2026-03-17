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

const sampleDocument = `
Momahub Distributed Inference Agreement (Demo Document)

Section 1 — Parties
This agreement is entered into between Digital Duck Labs ("Provider") and the
participating GPU operators ("Contributors"). The Provider operates the hub
infrastructure; Contributors supply compute via the Momahub agent software.

Section 2 — Compute Contribution
Contributors agree to make available their GPU resources during registered hours.
The hub dispatches inference tasks based on tier (PLATINUM/GOLD/SILVER/BRONZE),
VRAM availability, and model support. Contributors are compensated in Momahub
credits at the rate of 1 credit per 1,000 output tokens generated.

Section 3 — Privacy and Data Handling
No task prompt shall be stored beyond the task lifecycle (default: 24 hours).
Contributors acknowledge that prompt chunks may not represent complete documents.
The hub retains sole access to task assembly and final result delivery.

Section 4 — Liability
The Provider makes no warranty regarding uptime, task completion rates, or
inference quality. Contributors are responsible for their hardware and network.
Neither party is liable for indirect or consequential damages.

Section 5 — Termination
Either party may terminate participation with 24 hours notice via moma down.
Outstanding tasks at termination time will be re-queued to available agents.
Credits earned prior to termination remain redeemable indefinitely.
`

const chunkSystem = "You are a document analyst. You are reading ONE EXCERPT of a larger document. " +
	"Extract and summarise the key points from this excerpt only. " +
	"Do not speculate about content outside the excerpt."

const assemblySystem = "You are a senior analyst assembling a document analysis from multiple excerpts. " +
	"Combine the partial analyses into a coherent summary. " +
	"Identify the main themes, key obligations, and notable clauses."

type chunkResult struct {
	Chunk        int
	State        string
	Content      string
	AgentID      string
	LatencyS     float64
	OutputTokens float64
	Error        string
}

var privacyClient = &http.Client{Timeout: 30 * time.Second}

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
	filePath := flag.String("file", "", "Document to chunk and analyse")
	numChunks := flag.Int("chunks", 3, "Number of chunks to split into")
	chunkModel := flag.String("chunk-model", "llama3", "Model for chunk analysis")
	assemblyModel := flag.String("assembly-model", "llama3", "Model for final assembly")
	maxTokens := flag.Int("max-tokens", 512, "Max output tokens")
	timeoutS := flag.Int("timeout", 300, "Timeout in seconds")
	flag.Parse()

	var document, filename string
	if *filePath != "" {
		data, err := os.ReadFile(*filePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading file: %v\n", err)
			os.Exit(1)
		}
		document = string(data)
		filename = *filePath
	} else {
		document = sampleDocument
		filename = "demo_contract.txt"
	}

	chunks := splitIntoChunks(document, *numChunks)

	fmt.Printf("\n  Privacy Chunk Demo\n")
	fmt.Printf("    Hub:           %s\n", *hubURL)
	fmt.Printf("    Document:      %s (%d chars)\n", filename, len(document))
	fmt.Printf("    Chunks:        %d (dispatched in parallel)\n", len(chunks))
	fmt.Printf("    Chunk model:   %s\n", *chunkModel)
	fmt.Printf("    Assembly model:%s\n\n", *assemblyModel)
	fmt.Println("  Privacy guarantee: no single agent sees the full document.")
	fmt.Println("  The hub is the only point that assembles all chunk analyses.")
	fmt.Println()

	// Show active agents
	showPrivacyAgents(*hubURL)

	wallStart := time.Now()

	// Dispatch all chunks in parallel
	var wg sync.WaitGroup
	chunkResults := make([]chunkResult, len(chunks))
	for i, chunk := range chunks {
		wg.Add(1)
		go func(idx int, c string) {
			defer wg.Done()
			fmt.Printf("  → Chunk %d: dispatching to agent (%d chars)...\n", idx+1, len(c))
			r := processChunk(*hubURL, c, idx+1, *chunkModel, *maxTokens/2, *timeoutS)
			chunkResults[idx] = r
			if r.State == "COMPLETE" {
				fmt.Printf("    OK Chunk %d complete  %.1fs  agent=..%s\n",
					idx+1, r.LatencyS, lastN(r.AgentID, 12))
			} else {
				fmt.Printf("    FAILED Chunk %d: %s\n", idx+1, r.Error)
			}
		}(i, chunk)
	}
	wg.Wait()

	// Assembly
	fmt.Printf("\n  → Assembly: combining %d chunk analyses...\n", len(chunkResults))
	finalResult := assembleResults(*hubURL, chunkResults, *assemblyModel, *maxTokens, *timeoutS)
	if finalResult.State == "COMPLETE" {
		fmt.Printf("    OK Assembly complete  %.1fs\n", finalResult.LatencyS)
	}

	wallTime := time.Since(wallStart).Seconds()

	// Show agent distribution
	agentIDs := make(map[string]bool)
	for _, r := range chunkResults {
		if r.State == "COMPLETE" && r.AgentID != "" {
			agentIDs[lastN(r.AgentID, 14)] = true
		}
	}
	agentList := make([]string, 0, len(agentIDs))
	for a := range agentIDs {
		agentList = append(agentList, ".."+a)
	}
	fmt.Printf("\n  Chunks handled by %d agent(s): %s\n", len(agentIDs), strings.Join(agentList, ", "))
	fmt.Printf("  (Each agent saw only 1/%d of the document)\n\n", len(chunks))

	fmt.Printf("  %s\n", strings.Repeat("=", 60))
	fmt.Printf("  ASSEMBLED ANALYSIS\n")
	fmt.Printf("  %s\n", strings.Repeat("-", 60))
	content := finalResult.Content
	if content == "" {
		content = "Assembly failed"
	}
	fmt.Printf("  %s\n", content)
	fmt.Printf("\n  Wall time: %.1fs\n\n", wallTime)
	saveResults("privacy_demo", map[string]interface{}{
		"timestamp":   time.Now().Format(time.RFC3339),
		"hub":         *hubURL,
		"document":    filename,
		"num_chunks":  *numChunks,
		"wall_time_s": wallTime,
		"chunks":      chunkResults,
		"assembly":    finalResult,
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

func splitIntoChunks(text string, n int) []string {
	paragraphs := []string{}
	for _, p := range strings.Split(strings.TrimSpace(text), "\n\n") {
		p = strings.TrimSpace(p)
		if p != "" {
			paragraphs = append(paragraphs, p)
		}
	}
	if n > len(paragraphs) {
		n = len(paragraphs)
	}
	perChunk := (len(paragraphs) + n - 1) / n
	chunks := make([]string, 0, n)
	for i := 0; i < len(paragraphs); i += perChunk {
		end := i + perChunk
		if end > len(paragraphs) {
			end = len(paragraphs)
		}
		chunks = append(chunks, strings.Join(paragraphs[i:end], "\n\n"))
		if len(chunks) >= n {
			break
		}
	}
	return chunks
}

func showPrivacyAgents(hub string) {
	resp, err := privacyClient.Get(fmt.Sprintf("%s/agents", hub))
	if err != nil {
		return
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(resp.Body)
	var data map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &data); err != nil {
		return
	}
	agents, _ := data["agents"].([]interface{})
	var online []interface{}
	for _, a := range agents {
		agent, ok := a.(map[string]interface{})
		if ok && str(agent, "status") == "ONLINE" {
			online = append(online, a)
		}
	}
	fmt.Printf("  Active agents: %d\n", len(online))
	for _, a := range online {
		agent := a.(map[string]interface{})
		fmt.Printf("    - %s (%s)\n", str(agent, "name"), str(agent, "tier"))
	}
	fmt.Println()
}

func processChunk(hub, chunk string, chunkNum int, model string, maxTokens, timeoutS int) chunkResult {
	taskID := fmt.Sprintf("chunk%d-%s", chunkNum, uuid.New().String()[:8])
	t0 := time.Now()

	payload := map[string]interface{}{
		"task_id":    taskID,
		"model":      model,
		"prompt":     fmt.Sprintf("Analyse this document excerpt:\n\n%s", chunk),
		"system":     chunkSystem,
		"max_tokens": maxTokens,
	}
	body, _ := json.Marshal(payload)
	var resp *http.Response
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt*2) * time.Second)
		}
		var reqErr error
		resp, reqErr = privacyClient.Post(fmt.Sprintf("%s/tasks", hub), "application/json", bytes.NewReader(body))
		if reqErr != nil {
			return chunkResult{Chunk: chunkNum, State: "SUBMIT_FAILED", Error: reqErr.Error()}
		}
		if resp.StatusCode == 429 {
			resp.Body.Close()
			continue
		}
		break
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return chunkResult{Chunk: chunkNum, State: "SUBMIT_REJECTED", Error: fmt.Sprintf("HTTP %d", resp.StatusCode)}
	}

	deadline := time.Now().Add(time.Duration(timeoutS) * time.Second)
	interval := 2 * time.Second

	for time.Now().Before(deadline) {
		getResp, err := privacyClient.Get(fmt.Sprintf("%s/tasks/%s", hub, taskID))
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
			return chunkResult{
				Chunk:        chunkNum,
				State:        "COMPLETE",
				Content:      str(res, "content"),
				AgentID:      str(res, "agent_id"),
				LatencyS:     time.Since(t0).Seconds(),
				OutputTokens: num(res, "output_tokens"),
			}
		}
		if state == "FAILED" {
			res, _ := task["result"].(map[string]interface{})
			errMsg := ""
			if res != nil {
				errMsg = str(res, "error")
			}
			return chunkResult{Chunk: chunkNum, State: "FAILED", Error: errMsg}
		}

		time.Sleep(interval)
		if interval < 8*time.Second {
			interval = time.Duration(float64(interval) * 1.2)
		}
	}

	return chunkResult{Chunk: chunkNum, State: "TIMEOUT"}
}

func assembleResults(hub string, chunks []chunkResult, model string, maxTokens, timeoutS int) chunkResult {
	parts := make([]string, 0, len(chunks))
	for _, r := range chunks {
		if r.State == "COMPLETE" {
			parts = append(parts, fmt.Sprintf("--- Excerpt %d Analysis ---\n%s", r.Chunk, r.Content))
		}
	}
	combined := strings.Join(parts, "\n\n")

	taskID := fmt.Sprintf("assemble-%s", uuid.New().String()[:8])
	t0 := time.Now()

	payload := map[string]interface{}{
		"task_id":    taskID,
		"model":      model,
		"prompt":     fmt.Sprintf("Combine these partial document analyses:\n\n%s", combined),
		"system":     assemblySystem,
		"max_tokens": maxTokens,
	}
	body, _ := json.Marshal(payload)
	resp, err := privacyClient.Post(fmt.Sprintf("%s/tasks", hub), "application/json", bytes.NewReader(body))
	if err != nil {
		return chunkResult{State: "SUBMIT_FAILED", Error: err.Error()}
	}
	defer resp.Body.Close()

	deadline := time.Now().Add(time.Duration(timeoutS) * time.Second)
	interval := 2 * time.Second

	for time.Now().Before(deadline) {
		getResp, err := privacyClient.Get(fmt.Sprintf("%s/tasks/%s", hub, taskID))
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
			return chunkResult{
				State:    "COMPLETE",
				Content:  str(res, "content"),
				AgentID:  str(res, "agent_id"),
				LatencyS: time.Since(t0).Seconds(),
			}
		}
		if state == "FAILED" {
			return chunkResult{State: "FAILED", Content: ""}
		}

		time.Sleep(interval)
		if interval < 8*time.Second {
			interval = time.Duration(float64(interval) * 1.2)
		}
	}

	return chunkResult{State: "TIMEOUT"}
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
