package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
)

// Status implements "mg status".
func Status(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	hubURL := fs.String("hub-url", "", "Hub URL")
	fs.Parse(args)

	url := ResolveHubURL(*hubURL)
	data, err := getJSON(fmt.Sprintf("%s/health", url))
	if err != nil {
		return err
	}
	fmt.Printf("Hub: %s  Status: %s  Agents: %.0f\n",
		str(data, "hub_id"), str(data, "status"), num(data, "agents_online"))
	return nil
}

// Agents implements "mg agents".
func Agents(args []string) error {
	fs := flag.NewFlagSet("agents", flag.ExitOnError)
	hubURL := fs.String("hub-url", "", "Hub URL")
	fs.Parse(args)

	url := ResolveHubURL(*hubURL)
	data, err := getJSON(fmt.Sprintf("%s/agents", url))
	if err != nil {
		return err
	}
	agents := items(data, "agents")
	if len(agents) == 0 {
		fmt.Println("No agents online.")
		return nil
	}
	fmt.Printf("%-16s %-38s %-10s %-10s %6s\n", "NAME", "AGENT_ID", "TIER", "STATUS", "TPS")
	fmt.Println(repeat('-', 86))
	for _, a := range agents {
		fmt.Printf("%-16s %-38s %-10s %-10s %6.1f\n",
			str(a, "name"), str(a, "agent_id"), str(a, "tier"), str(a, "status"), num(a, "current_tps"))
	}
	return nil
}

// Tasks implements "mg tasks".
func Tasks(args []string) error {
	fs := flag.NewFlagSet("tasks", flag.ExitOnError)
	hubURL := fs.String("hub-url", "", "Hub URL")
	limit := fs.Int("limit", 20, "Number of tasks to show")
	detail := fs.Bool("detail", false, "Show detailed task info")
	fs.BoolVar(detail, "d", false, "Show detailed task info (shorthand)")
	fs.Parse(args)

	url := ResolveHubURL(*hubURL)
	data, err := getJSON(fmt.Sprintf("%s/tasks?limit=%d", url, *limit))
	if err != nil {
		return err
	}
	tasks := items(data, "tasks")
	if len(tasks) == 0 {
		fmt.Println("No tasks.")
		return nil
	}

	// Build agent_id → name lookup (best-effort; ignore errors)
	agentName := map[string]string{}
	agentHost := map[string]string{}
	if adata, aerr := getJSON(fmt.Sprintf("%s/agents", url)); aerr == nil {
		for _, a := range items(adata, "agents") {
			id := str(a, "agent_id")
			name := str(a, "name")
			if name == "" {
				name = truncate(id, 14)
			}
			agentName[id] = name
			agentHost[id] = str(a, "host")
		}
	}

	// fmtTS formats a DB timestamp ("2006-01-02 15:04:05" or RFC3339) for display.
	fmtTS := func(ts string) string {
		var t time.Time
		var parseErr error
		for _, layout := range []string{
			"2006-01-02 15:04:05.999999999Z07:00",
			"2006-01-02 15:04:05.999999999-07:00",
			"2006-01-02 15:04:05Z07:00",
			"2006-01-02 15:04:05-07:00",
			"2006-01-02 15:04:05.999999999",
			"2006-01-02 15:04:05",
			time.RFC3339Nano,
			time.RFC3339,
		} {
			t, parseErr = time.Parse(layout, ts)
			if parseErr == nil {
				break
			}
		}
		if parseErr != nil || ts == "" {
			return truncate(ts, 24)
		}
		return t.Local().Format("2006-01-02 15:04:05 MST")
	}

	// execBy returns a human label for which agent ran the task.
	execBy := func(t map[string]interface{}) string {
		id := str(t, "agent_id")
		if id == "" {
			return "—"
		}
		if name, ok := agentName[id]; ok {
			return name
		}
		return truncate(id, 16)
	}

	// submittedFrom shows whether the task arrived directly or via cluster peering.
	submittedFrom := func(t map[string]interface{}) string {
		peer := str(t, "peer_hub_id")
		if peer == "" {
			return "direct"
		}
		return truncate(peer, 12) + " (cluster)"
	}

	if *detail {
		for _, t := range tasks {
			fmt.Println(repeat('-', 80))
			fmt.Printf("  task_id:   %s\n", str(t, "task_id"))
			fmt.Printf("  state:     %s\n", str(t, "state"))
			fmt.Printf("  model:     %s\n", str(t, "model"))
			fmt.Printf("  submitted: %s  (%s)\n", str(t, "created_at"), submittedFrom(t))
			fmt.Printf("  updated:   %s\n", str(t, "updated_at"))
			id := str(t, "agent_id")
			host := agentHost[id]
			name := agentName[id]
			agentLine := id
			if name != "" {
				agentLine = fmt.Sprintf("%s  (%s)", name, id)
			}
			if host != "" {
				agentLine += "  @" + host
			}
			fmt.Printf("  executed:  %s\n", agentLine)
			fmt.Printf("  tokens:    %.0f in / %.0f out\n", num(t, "input_tokens"), num(t, "output_tokens"))
			fmt.Printf("  latency:   %.0f ms\n", num(t, "latency_ms"))
			if r := num(t, "retries"); r > 0 {
				fmt.Printf("  retries:   %.0f\n", r)
			}
			prompt := str(t, "prompt")
			if len(prompt) > 120 {
				prompt = prompt[:120] + "…"
			}
			fmt.Printf("  prompt:    %s\n", prompt)
			if content := str(t, "content"); content != "" {
				preview := content
				if len(preview) > 200 {
					preview = preview[:200] + "…"
				}
				fmt.Printf("  response:  %s\n", preview)
			}
			if e := str(t, "error"); e != "" {
				fmt.Printf("  error:     %s\n", e)
			}
		}
		fmt.Println(repeat('-', 80))
	} else {
		//  TIME              TASK_ID              STATE        MODEL                AGENT            FROM
		fmt.Printf("%-24s  %-20s  %-12s  %-20s  %-16s  %s\n",
			"TIME", "TASK_ID", "STATE", "MODEL", "AGENT", "FROM")
		fmt.Println(repeat('-', 106))
		for _, t := range tasks {
			taskID := str(t, "task_id")
			model := str(t, "model")
			fmt.Printf("%-24s  %-20s  %-12s  %-20s  %-16s  %s\n",
				fmtTS(str(t, "created_at")),
				truncate(taskID, 20),
				str(t, "state"),
				truncate(model, 20),
				truncate(execBy(t), 16),
				submittedFrom(t),
			)
		}
	}
	return nil
}

// Submit implements "mg submit".
func Submit(args []string) error {
	fs := flag.NewFlagSet("submit", flag.ExitOnError)
	hubURL := fs.String("hub-url", "", "Hub URL")
	model := fs.String("model", "llama3", "Model name")
	maxTokens := fs.Int("max-tokens", 1024, "Max output tokens")
	noWait := fs.Bool("no-wait", false, "Don't wait for result")
	fs.Parse(args)

	remaining := fs.Args()
	if len(remaining) == 0 {
		return fmt.Errorf("usage: mg submit <prompt> [flags]")
	}
	prompt := remaining[0]

	url := ResolveHubURL(*hubURL)
	taskID := uuid.New().String()

	_, err := postJSON(fmt.Sprintf("%s/tasks", url), map[string]interface{}{
		"task_id":    taskID,
		"model":      *model,
		"prompt":     prompt,
		"max_tokens": *maxTokens,
	})
	if err != nil {
		return err
	}
	fmt.Printf("Task submitted: %s\n", taskID)

	if *noWait {
		return nil
	}

	// Poll for result
	deadline := time.Now().Add(5 * time.Minute)
	interval := 2 * time.Second
	for time.Now().Before(deadline) {
		data, err := getJSON(fmt.Sprintf("%s/tasks/%s", url, taskID))
		if err != nil {
			time.Sleep(interval)
			continue
		}
		state := str(data, "state")
		if state == "COMPLETE" {
			result, _ := data["result"].(map[string]interface{})
			if result == nil {
				result = data
			}
			fmt.Printf("\n%s\n", str(result, "content"))
			agentInfo := str(result, "agent_name")
			if agentInfo == "" {
				agentInfo = str(result, "agent_host")
			}
			if agentInfo == "" {
				agentInfo = str(result, "agent_id")
			}
			completedAt := str(result, "completed_at")
			fmt.Printf("[model=%s tokens=%.0f+%.0f latency=%.0fms agent=%s completed=%s]\n",
				str(result, "model"),
				num(result, "input_tokens"),
				num(result, "output_tokens"),
				num(result, "latency_ms"),
				agentInfo,
				completedAt)
			return nil
		}
		if state == "FAILED" {
			result, _ := data["result"].(map[string]interface{})
			errMsg := "unknown"
			if result != nil {
				errMsg = str(result, "error")
			}
			return fmt.Errorf("FAILED: %s", errMsg)
		}
		time.Sleep(interval)
		if interval < 10*time.Second {
			interval = time.Duration(float64(interval) * 1.3)
		}
	}
	return fmt.Errorf("timed out waiting for task %s", taskID)
}

// Rewards implements "mg rewards".
func Rewards(args []string) error {
	fs := flag.NewFlagSet("rewards", flag.ExitOnError)
	hubURL := fs.String("hub-url", "", "Hub URL")
	fs.Parse(args)

	url := ResolveHubURL(*hubURL)
	data, err := getJSON(fmt.Sprintf("%s/rewards", url))
	if err != nil {
		return err
	}
	rows := items(data, "summary")
	if len(rows) == 0 {
		fmt.Println("No rewards yet.")
		return nil
	}
	fmt.Printf("%-20s %8s %12s %10s\n", "OPERATOR", "TASKS", "TOKENS", "CREDITS")
	for _, r := range rows {
		fmt.Printf("%-20s %8.0f %12.0f %10.2f\n",
			str(r, "operator_id"), num(r, "total_tasks"), num(r, "total_tokens"), num(r, "total_credits"))
	}
	return nil
}

// Logs implements "mg logs".
func Logs(args []string) error {
	fs := flag.NewFlagSet("logs", flag.ExitOnError)
	hubURL := fs.String("hub-url", "", "Hub URL")
	follow := fs.Bool("follow", false, "Follow log updates")
	fs.BoolVar(follow, "f", false, "Follow (shorthand)")
	interval := fs.Float64("interval", 5.0, "Poll interval in seconds")
	limit := fs.Int("limit", 20, "Number of entries")
	fs.Parse(args)

	url := ResolveHubURL(*hubURL)
	seenIDs := map[string]bool{}

	for {
		data, err := getJSON(fmt.Sprintf("%s/logs?limit=%d", url, *limit))
		if err != nil {
			return err
		}
		entries := items(data, "logs")
		// Print in reverse order (oldest first)
		for i := len(entries) - 1; i >= 0; i-- {
			e := entries[i]
			eid := str(e, "id")
			if eid == "" {
				eid = fmt.Sprintf("%.0f", num(e, "id"))
			}
			if seenIDs[eid] {
				continue
			}
			seenIDs[eid] = true
			fmt.Printf("[%s] %s status=%s tps=%.1f\n",
				str(e, "logged_at"), str(e, "agent_id"), str(e, "status"), num(e, "current_tps"))
		}
		if !*follow {
			break
		}
		time.Sleep(time.Duration(*interval * float64(time.Second)))
	}
	return nil
}

// Export implements "mg export".
func Export(args []string) error {
	fs := flag.NewFlagSet("export", flag.ExitOnError)
	hubURL := fs.String("hub-url", "", "Hub URL")
	output := fs.String("output", "", "Output file path")
	fs.StringVar(output, "o", "", "Output file (shorthand)")
	label := fs.String("label", "", "Label for this test run")
	fs.StringVar(label, "l", "", "Label (shorthand)")
	limit := fs.Int("limit", 500, "Max tasks to export")
	fs.Parse(args)

	url := ResolveHubURL(*hubURL)

	health, err := getJSON(fmt.Sprintf("%s/health", url))
	if err != nil {
		return err
	}
	hubID := str(health, "hub_id")

	tasksData, err := getJSON(fmt.Sprintf("%s/tasks?limit=%d", url, *limit))
	if err != nil {
		return err
	}
	agentsData, err := getJSON(fmt.Sprintf("%s/agents", url))
	if err != nil {
		return err
	}
	rewardsData, err := getJSON(fmt.Sprintf("%s/rewards", url))
	if err != nil {
		return err
	}

	lbl := *label
	if lbl == "" {
		lbl = hubID
	}

	exportData := map[string]interface{}{
		"label":       lbl,
		"hub_id":      hubID,
		"hub_url":     url,
		"exported_at": time.Now().UTC().Format(time.RFC3339),
		"agents":      agentsData["agents"],
		"rewards":     rewardsData["summary"],
		"tasks":       tasksData["tasks"],
	}

	outPath := *output
	if outPath == "" {
		outPath = fmt.Sprintf("results-%s.json", lbl)
	}

	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(exportData); err != nil {
		return err
	}

	tasks := items(tasksData, "tasks")
	completed := 0
	for _, t := range tasks {
		if str(t, "state") == "COMPLETE" {
			completed++
		}
	}
	fmt.Printf("Exported %d tasks (%d completed) to %s\n", len(tasks), completed, outPath)
	return nil
}
