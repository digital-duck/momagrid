package main

// Recipe 30: Academic Paper Pipeline
//
// Demonstrates a complete academic writing workflow distributed across agents:
//   Stage 1  —  Abstract analysis (parse topic + key claims)
//   Stage 2  —  Literature framing (related work, motivation)
//   Stage 3  —  Methodology outline (approach + design)
//   Stage 4  —  Results interpretation (findings + implications)
//   Stage 5  —  Conclusion + abstract rewrite (consolidate)
//
// Stages 1–4 run in parallel on separate agents; Stage 5 aggregates.
// Showcases MomaGrid's map-reduce inference pipeline.
//
// Usage:
//   go run ./30_academic_paper_pipeline/academic_pipeline.go --hub http://localhost:9000

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

// Sample research topic for the pipeline
const paperTopic = `
Title: Momagrid: A Decentralized Inference Runtime with Semantic Chunking

Abstract excerpt:
The quadratic complexity O(N^2) of the self-attention mechanism remains the
fundamental barrier to long-context inference and hardware decentralization.
We present Momagrid, a distributed inference runtime for Structured Prompt Language
(SPL) that bypasses this bottleneck through Semantic Chunking. By treating the
transformer context window as a partitioned data structure, Momagrid enables a
Map-Reduce inference pipeline that reduces peak attention complexity from O(N^2)
to O(N*k), where k is a hardware-bound constant.
`

var stages = []struct {
	id     string
	name   string
	system string
	prompt func(topic string) string
}{
	{
		id:     "analysis",
		name:   "Abstract Analysis",
		system: "You are a research analyst. Extract structured information from academic text. Be precise and concise.",
		prompt: func(topic string) string {
			return fmt.Sprintf(`Analyze this research abstract and extract:
1. Core problem being solved (1 sentence)
2. Key technical contribution (1 sentence)
3. Main claim or result (1 sentence)
4. Research category (e.g., Systems, ML, Distributed Computing)

Abstract:
%s`, topic)
		},
	},
	{
		id:     "literature",
		name:   "Literature Framing",
		system: "You are an academic researcher writing a literature review. Be scholarly but concise.",
		prompt: func(topic string) string {
			return fmt.Sprintf(`Based on this research topic, write a 3-sentence related work framing that:
1. Identifies the existing approaches this work improves on
2. Explains the gap in the literature
3. States how this work fills the gap

Research topic:
%s`, topic)
		},
	},
	{
		id:     "methodology",
		name:   "Methodology Outline",
		system: "You are a systems researcher. Describe technical methodologies clearly and precisely.",
		prompt: func(topic string) string {
			return fmt.Sprintf(`Based on this research description, outline the methodology in 4 bullet points:
- Key design principle
- Core algorithmic approach
- Implementation strategy
- Evaluation approach

Research:
%s`, topic)
		},
	},
	{
		id:     "implications",
		name:   "Results & Implications",
		system: "You are a technical writer specializing in research impact. Focus on practical significance.",
		prompt: func(topic string) string {
			return fmt.Sprintf(`Based on this research, describe in 3 sentences:
1. What the system achieves practically
2. Who benefits from this work
3. What future work it enables

Research:
%s`, topic)
		},
	},
}

type stageResult struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Content     string  `json:"content"`
	AgentID     string  `json:"agent_id"`
	AgentName   string  `json:"agent_name"`
	AgentHost   string  `json:"agent_host"`
	OutputTokens float64 `json:"output_tokens"`
	LatencyMs   float64 `json:"latency_ms"`
	State       string  `json:"state"`
}

type pipelineReport struct {
	Timestamp    string        `json:"timestamp"`
	Topic        string        `json:"topic"`
	Model        string        `json:"model"`
	Stages       []stageResult `json:"stages"`
	Conclusion   stageResult   `json:"conclusion"`
	TotalTokens  int           `json:"total_tokens"`
	TotalLatencyS float64      `json:"total_latency_s"`
	Summary      string        `json:"summary"`
}

var client = &http.Client{Timeout: 120 * time.Second}

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
		return cfg.Hub.URLs[0]
	}
	return fmt.Sprintf("http://localhost:%d", cfg.Hub.Port)
}

func submitTask(hubURL, model, system, prompt string) (*stageResult, error) {
	taskID := uuid.New().String()
	payload := map[string]interface{}{
		"task_id":     taskID,
		"model":       model,
		"system":      system,
		"prompt":      prompt,
		"max_tokens":  512,
		"temperature": 0.3,
	}
	body, _ := json.Marshal(payload)
	resp, err := http.Post(hubURL+"/tasks", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	resp.Body.Close()

	for i := 0; i < 90; i++ {
		time.Sleep(2 * time.Second)
		r, err := http.Get(fmt.Sprintf("%s/tasks/%s", hubURL, taskID))
		if err != nil {
			continue
		}
		var status map[string]interface{}
		json.NewDecoder(r.Body).Decode(&status)
		r.Body.Close()

		state, _ := status["state"].(string)
		if state == "COMPLETE" || state == "FAILED" {
			sr := &stageResult{State: state}
			if res, ok := status["result"].(map[string]interface{}); ok {
				sr.Content = fmt.Sprint(res["content"])
				sr.AgentID = fmt.Sprint(res["agent_id"])
				sr.AgentName = fmt.Sprint(res["agent_name"])
				sr.AgentHost = fmt.Sprint(res["agent_host"])
				sr.OutputTokens, _ = res["output_tokens"].(float64)
				sr.LatencyMs, _ = res["latency_ms"].(float64)
			}
			return sr, nil
		}
	}
	return &stageResult{State: "TIMEOUT"}, nil
}

func saveResults(outDir string, report *pipelineReport) error {
	os.MkdirAll(outDir, 0755)
	ts := time.Now().Format("20060102_150405")
	path := filepath.Join(outDir, fmt.Sprintf("academic_pipeline_%s.json", ts))
	data, _ := json.MarshalIndent(report, "", "  ")
	return os.WriteFile(path, data, 0644)
}

func main() {
	hubURL := flag.String("hub", defaultHubURL(), "Hub URL")
	model := flag.String("model", "llama3", "Model to use")
	outDir := flag.String("out", "cookbook/out", "Output directory")
	flag.Parse()

	fmt.Println("🦆 Academic Paper Pipeline — Distributed Research Workflow")
	fmt.Printf("   Hub:    %s\n", *hubURL)
	fmt.Printf("   Model:  %s\n", *model)
	fmt.Printf("   Stages: %d parallel + 1 synthesis\n", len(stages))
	fmt.Println()

	start := time.Now()

	// Stage 1–4: Run in parallel across agents
	fmt.Println("▶ Phase 1: Parallel Analysis (4 agents)")
	var mu sync.Mutex
	var wg sync.WaitGroup
	stageResults := make([]stageResult, len(stages))

	for i, stage := range stages {
		wg.Add(1)
		go func(idx int, s struct {
			id     string
			name   string
			system string
			prompt func(topic string) string
		}) {
			defer wg.Done()
			fmt.Printf("   → [%s] dispatching to grid...\n", s.name)
			sr, err := submitTask(*hubURL, *model, s.system, s.prompt(paperTopic))
			if err != nil {
				sr = &stageResult{State: "ERROR"}
			}
			sr.ID = s.id
			sr.Name = s.name

			agentLabel := sr.AgentName
			if agentLabel == "" || agentLabel == "<nil>" {
				agentLabel = sr.AgentID[:min8(sr.AgentID)]
			}
			fmt.Printf("   ✓ [%s] → agent=%s  %d tok  %.0fms\n",
				s.name, agentLabel, int(sr.OutputTokens), sr.LatencyMs)

			mu.Lock()
			stageResults[idx] = *sr
			mu.Unlock()
		}(i, stage)
	}
	wg.Wait()

	// Stage 5: Synthesis — sequential, depends on all parallel stages
	fmt.Println("\n▶ Phase 2: Synthesis (conclusion + refined abstract)")

	var synthesisInput strings.Builder
	totalTokens := 0
	synthesisInput.WriteString(fmt.Sprintf("Original research topic:\n%s\n\n", paperTopic))
	for _, sr := range stageResults {
		synthesisInput.WriteString(fmt.Sprintf("=== %s ===\n%s\n\n", sr.Name, sr.Content))
		totalTokens += int(sr.OutputTokens)
	}

	conclusionSystem := "You are a senior researcher writing the conclusion of an academic paper. Synthesize diverse analyses into a coherent, compelling conclusion."
	conclusionPrompt := fmt.Sprintf(`Based on the following analyses of a research paper, write:
1. A 3-sentence conclusion that captures the core contribution and its significance
2. A polished 4-sentence abstract rewrite

%s`, synthesisInput.String())

	conclusion, err := submitTask(*hubURL, *model, conclusionSystem, conclusionPrompt)
	if err != nil || conclusion == nil {
		conclusion = &stageResult{State: "ERROR"}
	}
	conclusion.ID = "conclusion"
	conclusion.Name = "Conclusion & Abstract"
	totalTokens += int(conclusion.OutputTokens)

	agentLabel := conclusion.AgentName
	if agentLabel == "" || agentLabel == "<nil>" {
		agentLabel = conclusion.AgentID[:min8(conclusion.AgentID)]
	}
	fmt.Printf("   ✓ Synthesis → agent=%s  %d tok  %.0fms\n",
		agentLabel, int(conclusion.OutputTokens), conclusion.LatencyMs)

	totalLatency := time.Since(start).Seconds()
	completed := 0
	for _, sr := range stageResults {
		if sr.State == "COMPLETE" {
			completed++
		}
	}
	if conclusion.State == "COMPLETE" {
		completed++
	}
	summary := fmt.Sprintf("%d/%d stages complete · %d total tokens · %.1fs",
		completed, len(stages)+1, totalTokens, totalLatency)

	report := &pipelineReport{
		Timestamp:     time.Now().Format("2006-01-02 15:04:05"),
		Topic:         strings.TrimSpace(paperTopic),
		Model:         *model,
		Stages:        stageResults,
		Conclusion:    *conclusion,
		TotalTokens:   totalTokens,
		TotalLatencyS: totalLatency,
		Summary:       summary,
	}

	if err := saveResults(*outDir, report); err != nil {
		fmt.Fprintf(os.Stderr, "⚠️  save error: %v\n", err)
	}

	fmt.Printf("\n📊 Pipeline complete: %s\n", summary)
	fmt.Println("\n── Conclusion excerpt ──")
	fmt.Println(truncate(conclusion.Content, 300))
}

func min8(s string) int {
	if len(s) < 8 {
		return len(s)
	}
	return 8
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
