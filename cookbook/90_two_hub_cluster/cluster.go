// Recipe 90: Two-Hub Cluster Demo
//
// Demonstrates hub-to-hub peering on a LAN: peer Hub A ↔ Hub B, submit a
// task to Hub A, and verify it completes (locally or forwarded to Hub B).
//
// Usage:
//   go run cluster.go status  --hub-a http://192.168.1.10:8080 --hub-b http://192.168.1.11:8080
//   go run cluster.go peer    --hub-a http://192.168.1.10:8080 --hub-b http://192.168.1.11:8080
//   go run cluster.go test    --hub-a http://192.168.1.10:8080 --hub-b http://192.168.1.11:8080
//   go run cluster.go full    --hub-a http://192.168.1.10:8080 --hub-b http://192.168.1.11:8080

package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"time"
)

// ── CLI flags ────────────────────────────────────────────────────────────────

var (
	hubA    = flag.String("hub-a", "http://localhost:8080", "URL of Hub A")
	hubB    = flag.String("hub-b", "http://localhost:8081", "URL of Hub B")
	model   = flag.String("model", "llama3", "Model name for test task")
	timeout = flag.Duration("timeout", 120*time.Second, "Max wait for task completion")
)

// ── HTTP helpers ─────────────────────────────────────────────────────────────

var client = &http.Client{Timeout: 15 * time.Second}

func getJSON(url string, dest interface{}) error {
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}
	return json.Unmarshal(body, dest)
}

func postJSON(url string, payload, dest interface{}) (int, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	resp, err := client.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if dest != nil {
		_ = json.Unmarshal(body, dest)
	}
	return resp.StatusCode, nil
}

// ── Data types ───────────────────────────────────────────────────────────────

type HubHealth struct {
	HubID      string `json:"hub_id"`
	Status     string `json:"status"`
	AgentCount int    `json:"agent_count"`
}

type Agent struct {
	AgentID string `json:"agent_id"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Tier    string `json:"tier"`
}

type AgentsResp struct {
	Agents []Agent `json:"agents"`
}

type Peer struct {
	HubID  string `json:"hub_id"`
	HubURL string `json:"hub_url"`
	Status string `json:"status"`
}

type ClusterStatus struct {
	Peers []Peer `json:"peers"`
}

type TaskResp struct {
	TaskID string `json:"task_id"`
}

type TaskStatus struct {
	TaskID  string `json:"task_id"`
	State   string `json:"state"`
	Content string `json:"content"`
	AgentID string `json:"agent_id"`
	Error   string `json:"error"`
}

// ── Commands ─────────────────────────────────────────────────────────────────

// cmdStatus prints health and agent/peer info for both hubs.
func cmdStatus() error {
	for _, entry := range []struct{ label, url string }{
		{"Hub A", *hubA},
		{"Hub B", *hubB},
	} {
		fmt.Printf("\n=== %s (%s) ===\n", entry.label, entry.url)

		var health HubHealth
		if err := getJSON(entry.url+"/health", &health); err != nil {
			fmt.Printf("  health: UNREACHABLE (%v)\n", err)
			continue
		}
		fmt.Printf("  hub_id : %s\n", health.HubID)
		fmt.Printf("  status : %s\n", health.Status)
		fmt.Printf("  agents : %d\n", health.AgentCount)

		var ar AgentsResp
		if err := getJSON(entry.url+"/agents", &ar); err == nil {
			for _, a := range ar.Agents {
				fmt.Printf("    [%s] %-20s tier=%-8s status=%s\n",
					a.AgentID[:8], a.Name, a.Tier, a.Status)
			}
		}

		var cs ClusterStatus
		if err := getJSON(entry.url+"/cluster/status", &cs); err == nil {
			if len(cs.Peers) == 0 {
				fmt.Println("  peers  : (none)")
			}
			for _, p := range cs.Peers {
				fmt.Printf("  peer   : %s  url=%s  status=%s\n",
					p.HubID[:8], p.HubURL, p.Status)
			}
		}
	}
	return nil
}

// cmdPeer establishes bidirectional peering between Hub A and Hub B.
func cmdPeer() error {
	fmt.Println("\n── Peering Hub A → Hub B ──")
	payload := map[string]string{"hub_url": *hubB}
	code, err := postJSON(*hubA+"/cluster/peers", payload, nil)
	if err != nil {
		return fmt.Errorf("peer A→B: %w", err)
	}
	if code == 200 || code == 201 {
		fmt.Printf("  ✓ Hub A now knows Hub B (HTTP %d)\n", code)
	} else {
		fmt.Printf("  ✗ Unexpected status %d\n", code)
	}

	fmt.Println("\n── Peering Hub B → Hub A ──")
	payload = map[string]string{"hub_url": *hubA}
	code, err = postJSON(*hubB+"/cluster/peers", payload, nil)
	if err != nil {
		return fmt.Errorf("peer B→A: %w", err)
	}
	if code == 200 || code == 201 {
		fmt.Printf("  ✓ Hub B now knows Hub A (HTTP %d)\n", code)
	} else {
		fmt.Printf("  ✗ Unexpected status %d\n", code)
	}

	fmt.Println("\n── Cluster status after peering ──")
	return cmdStatus()
}

// cmdTest submits a task to Hub A and polls until completion or timeout.
func cmdTest() error {
	prompt := "In one sentence, what is the Transformer attention mechanism?"
	fmt.Printf("\n── Submitting test task to Hub A ──\n")
	fmt.Printf("  model  : %s\n", *model)
	fmt.Printf("  prompt : %s\n", prompt)

	payload := map[string]interface{}{
		"model":      *model,
		"prompt":     prompt,
		"max_tokens": 128,
	}
	var tr TaskResp
	code, err := postJSON(*hubA+"/tasks", payload, &tr)
	if err != nil {
		return fmt.Errorf("submit task: %w", err)
	}
	if code != 200 && code != 201 {
		return fmt.Errorf("submit returned HTTP %d", code)
	}
	fmt.Printf("  task_id: %s\n", tr.TaskID)

	// Poll with exponential backoff
	fmt.Println("\n── Polling for result ──")
	deadline := time.Now().Add(*timeout)
	delay := 1.0
	for time.Now().Before(deadline) {
		var ts TaskStatus
		if err := getJSON(*hubA+"/tasks/"+tr.TaskID, &ts); err != nil {
			fmt.Printf("  poll error: %v\n", err)
		} else {
			fmt.Printf("  state: %-12s", ts.State)
			if ts.AgentID != "" {
				fmt.Printf("  agent: %s", ts.AgentID[:8])
			}
			fmt.Println()

			switch ts.State {
			case "COMPLETE":
				fmt.Printf("\n✓ Task completed\n")
				fmt.Printf("  result: %s\n", ts.Content)
				return nil
			case "FAILED":
				return fmt.Errorf("task FAILED: %s", ts.Error)
			}
		}
		time.Sleep(time.Duration(delay * float64(time.Second)))
		delay = math.Min(delay*1.5, 8)
	}
	return fmt.Errorf("timed out after %s", *timeout)
}

// cmdFull runs: status → peer → test → status.
func cmdFull() error {
	steps := []struct {
		name string
		fn   func() error
	}{
		{"Status (before)", cmdStatus},
		{"Peer", cmdPeer},
		{"Test", cmdTest},
		{"Status (after)", cmdStatus},
	}
	for _, s := range steps {
		fmt.Printf("\n\n══════════════════════════════════════════\n")
		fmt.Printf("  STEP: %s\n", s.name)
		fmt.Printf("══════════════════════════════════════════\n")
		if err := s.fn(); err != nil {
			return fmt.Errorf("%s failed: %w", s.name, err)
		}
	}
	fmt.Println("\n✓ Full two-hub cluster demo completed successfully.")
	return nil
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	flag.Parse()
	args := flag.Args()
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "Usage: cluster.go <command> [flags]\n")
		fmt.Fprintf(os.Stderr, "Commands: status | peer | test | full\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
		os.Exit(1)
	}

	var err error
	switch args[0] {
	case "status":
		err = cmdStatus()
	case "peer":
		err = cmdPeer()
	case "test":
		err = cmdTest()
	case "full":
		err = cmdFull()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", args[0])
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "\n✗ Error: %v\n", err)
		os.Exit(1)
	}
}
