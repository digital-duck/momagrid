// Recipe 28: Federated Search Grid — Distribute search and synthesize results.
//
// This recipe demonstrates how to use the grid to parallelize information gathering:
// 1. Partitioning: Split a complex query into 3 specific sub-queries.
// 2. Parallel Search: Dispatch sub-queries to different agents (mocking search).
// 3. Synthesis: Aggregate the findings into a final report.
//
// Usage:
//   go run search.go "Recent breakthroughs in room-temperature superconductivity"
package main

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

func defaultHubURL() string {
	home, _ := os.UserHomeDir()
	data, err := os.ReadFile(filepath.Join(home, ".igrid", "config.yaml"))
	if err != nil {
		return "http://localhost:9000"
	}
	var cfg struct {
		Hub struct {
			URLs []string `yaml:"urls"`
		} `yaml:"hub"`
	}
	yaml.Unmarshal(data, &cfg)
	if len(cfg.Hub.URLs) > 0 {
		return strings.TrimRight(cfg.Hub.URLs[0], "/")
	}
	return "http://localhost:9000"
}

func main() {
	hubURL := flag.String("hub", defaultHubURL(), "Hub URL")
	model := flag.String("model", "llama3", "Model to use")
	flag.Parse()

	query := flag.Arg(0)
	if query == "" {
		fmt.Println("Usage: go run search.go <query>")
		return
	}

	fmt.Printf("\n🔍 Federated Search Grid: \"%s\"\n", query)
	fmt.Printf("   Hub: %s\n\n", *hubURL)

	// Step 1: Partitioning
	fmt.Print("   [1/3] Partitioning query... ")
	partitionSystem := "Split the query into 3 distinct, specific sub-queries for research. Return ONLY JSON: [\"q1\", \"q2\", \"q3\"]"
	partitionJSON, _, err := submitAndWait(*hubURL, partitionSystem, "Query: "+query, *model)
	if err != nil {
		fmt.Printf("FAILED: %v\n", err)
		return
	}
	var subQueries []string
	json.Unmarshal([]byte(cleanJSON(partitionJSON)), &subQueries)
	fmt.Printf("done (%d sub-queries)\n", len(subQueries))

	// Step 2: Parallel Search (Mocking search by asking LLM to 'search' its knowledge)
	fmt.Printf("   [2/3] Dispatching federated search tasks...\n")
	var wg sync.WaitGroup
	results := make([]string, len(subQueries))
	searchSystem := "You are a search agent. Provide a detailed summary of facts for the given sub-query."
	for i, q := range subQueries {
		wg.Add(1)
		go func(idx int, sq string) {
			defer wg.Done()
			fmt.Printf("         → Sub-query %d: %s\n", idx+1, sq)
			content, _, _ := submitAndWait(*hubURL, searchSystem, "Research: "+sq, *model)
			results[idx] = content
		}(i, q)
	}
	wg.Wait()

	// Step 3: Synthesis
	fmt.Printf("\n   [3/3] Synthesizing final report...\n")
	synthesisSystem := "Synthesize the provided research findings into a single, cohesive technical report."
	findings := ""
	for i, r := range results {
		findings += fmt.Sprintf("Findings from Sub-query %d:\n%s\n\n", i+1, r)
	}
	report, _, _ := submitAndWait(*hubURL, synthesisSystem, findings, *model)

	fmt.Printf("\n%s\n", strings.Repeat("=", 60))
	fmt.Printf("FINAL FEDERATED SEARCH REPORT\n")
	fmt.Printf("%s\n\n", strings.Repeat("=", 60))
	fmt.Println(report)
}

func submitAndWait(hub, system, prompt, model string) (string, int, error) {
	taskID := "search-" + uuid.New().String()[:8]
	payload := map[string]interface{}{
		"task_id":    taskID,
		"model":      model,
		"prompt":     prompt,
		"system":     system,
		"max_tokens": 1024,
	}
	b, _ := json.Marshal(payload)
	resp, err := http.Post(hub+"/tasks", "application/json", bytes.NewReader(b))
	if err != nil {
		return "", 0, err
	}
	resp.Body.Close()

	for i := 0; i < 60; i++ {
		r, err := http.Get(fmt.Sprintf("%s/tasks/%s", hub, taskID))
		if err != nil {
			time.Sleep(1 * time.Second)
			continue
		}
		var data map[string]interface{}
		json.NewDecoder(r.Body).Decode(&data)
		r.Body.Close()

		if data["state"] == "COMPLETE" {
			res := data["result"].(map[string]interface{})
			return res["content"].(string), int(res["output_tokens"].(float64)), nil
		}
		if data["state"] == "FAILED" {
			return "", 0, fmt.Errorf("task failed")
		}
		time.Sleep(1 * time.Second)
	}
	return "", 0, fmt.Errorf("timeout")
}

func cleanJSON(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}
