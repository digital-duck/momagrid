package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var rewardsClient = &http.Client{Timeout: 10 * time.Second}

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

type rewardEntry struct {
	OperatorID   string  `json:"operator_id"`
	TotalTasks   int     `json:"task_count"`
	TotalTokens  int     `json:"total_tokens"`
	TotalCredits float64 `json:"total_credits"`
}

func main() {
	hubURL := flag.String("hub", defaultHubURL(), "Hub URL")
	flag.Parse()

	hub := strings.TrimRight(*hubURL, "/")

	rewards := fetchRewards(hub)
	if len(rewards) == 0 {
		fmt.Println("  No rewards recorded yet. Run some tasks first.")
		return
	}

	agents := fetchAgents(hub)

	// Totals
	totalTasks := 0
	totalTokens := 0
	totalCredits := 0.0
	for _, r := range rewards {
		totalTasks += r.TotalTasks
		totalTokens += r.TotalTokens
		totalCredits += r.TotalCredits
	}

	fmt.Printf("\n  Reward Economy Report\n")
	fmt.Printf("    Hub:   %s\n", hub)
	fmt.Printf("    Time:  %s\n\n", time.Now().Format("2006-01-02 15:04:05"))

	fmt.Printf("  %s\n", strings.Repeat("─", 50))
	fmt.Printf("  GRID TOTALS\n")
	fmt.Printf("  %s\n", strings.Repeat("─", 50))
	fmt.Printf("  Total tasks:    %10d\n", totalTasks)
	fmt.Printf("  Total tokens:   %10d\n", totalTokens)
	fmt.Printf("  Total credits:  %10.4f\n", totalCredits)
	fmt.Printf("  Credit rate:    1 credit per 1,000 output tokens (PoC)\n\n")

	fmt.Printf("  %s\n", strings.Repeat("─", 50))
	fmt.Printf("  BY OPERATOR\n")
	fmt.Printf("  %s\n", strings.Repeat("─", 50))
	fmt.Printf("  %-20s %8s %12s %10s\n", "Operator", "Tasks", "Tokens", "Credits")
	fmt.Printf("  %s\n", strings.Repeat("-", 52))

	// Sort by credits descending
	sort.Slice(rewards, func(i, j int) bool {
		return rewards[i].TotalCredits > rewards[j].TotalCredits
	})

	for _, r := range rewards {
		label := r.OperatorID
		if agent, ok := agents[r.OperatorID]; ok {
			tier := str(agent, "tier")
			if tier != "" {
				label = fmt.Sprintf("%s (%s)", r.OperatorID, tier)
			}
		}
		if len(label) > 20 {
			label = label[:20]
		}
		fmt.Printf("  %-20s %8d %12d %10.4f\n",
			label, r.TotalTasks, r.TotalTokens, r.TotalCredits)
	}

	fmt.Printf("\n  Note: Full reward economy (redemption, transfer, billing)\n")
	fmt.Printf("        coming in Phase 9. Credits are currently indicative.\n\n")
	saveResults("rewards_report", map[string]interface{}{
		"timestamp":     time.Now().Format(time.RFC3339),
		"hub":           hub,
		"total_tasks":   totalTasks,
		"total_tokens":  totalTokens,
		"total_credits": totalCredits,
		"rewards":       rewards,
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

func fetchRewards(hub string) []rewardEntry {
	resp, err := rewardsClient.Get(fmt.Sprintf("%s/rewards", hub))
	if err != nil {
		fmt.Printf("  Cannot reach hub: %v\n", err)
		return nil
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(resp.Body)

	var data map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &data); err != nil {
		return nil
	}

	summaryRaw, _ := data["summary"].([]interface{})
	var rewards []rewardEntry
	for _, s := range summaryRaw {
		sm, ok := s.(map[string]interface{})
		if !ok {
			continue
		}
		r := rewardEntry{
			OperatorID:   str(sm, "operator_id"),
			TotalCredits: num(sm, "total_credits"),
			TotalTokens:  int(num(sm, "total_tokens")),
		}
		// Handle both "task_count" and "total_tasks"
		if v := num(sm, "task_count"); v > 0 {
			r.TotalTasks = int(v)
		} else {
			r.TotalTasks = int(num(sm, "total_tasks"))
		}
		rewards = append(rewards, r)
	}
	return rewards
}

func fetchAgents(hub string) map[string]map[string]interface{} {
	resp, err := rewardsClient.Get(fmt.Sprintf("%s/agents", hub))
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(resp.Body)

	var data map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &data); err != nil {
		return nil
	}

	agents, _ := data["agents"].([]interface{})
	result := make(map[string]map[string]interface{})
	for _, a := range agents {
		agent, ok := a.(map[string]interface{})
		if !ok {
			continue
		}
		opID := str(agent, "operator_id")
		if opID != "" {
			result[opID] = agent
		}
	}
	return result
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
