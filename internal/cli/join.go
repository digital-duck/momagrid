package cli

// Join implements "mg join <hub-url>" — registers this machine as a grid agent
// and starts a heartbeat loop.
//
// Ed25519 signing flow:
//  1. Load (or generate) ~/.igrid/agent_key.pem
//  2. Include public_key + signed challenge in JoinRequest
//  3. Sign every PulseReport with the same private key
//
// Unsigned agents (no --sign flag, or legacy builds) still work on hubs that
// haven't enabled --require-signing.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/digital-duck/momagrid/internal/identity"
	"github.com/digital-duck/momagrid/internal/schema"
	"github.com/google/uuid"
)

// Join implements "mg join".
func Join(args []string) error {
	fs := flag.NewFlagSet("join", flag.ExitOnError)
	operatorID := fs.String("operator-id", "", "Operator ID (default: from config)")
	name := fs.String("name", "", "Agent display name (default: hostname)")
	model := fs.String("model", "llama3", "Primary model to advertise")
	models := fs.String("models", "", "Comma-separated list of supported models")
	ollamaURL := fs.String("ollama", "", "Ollama URL (default: from config)")
	pullMode := fs.Bool("pull", false, "Use SSE pull mode (for agents behind NAT)")
	sign := fs.Bool("sign", true, "Sign join/pulse with Ed25519 keypair (recommended)")
	apiKey := fs.String("api-key", "", "API key (if hub requires one)")
	pulseInterval := fs.Duration("pulse", 30*time.Second, "Heartbeat interval")
	fs.Parse(args)

	remaining := fs.Args()
	cfg := LoadConfig()

	var hubURL string
	if len(remaining) > 0 {
		// Explicit URL provided — use it directly.
		hubURL = strings.TrimRight(remaining[0], "/")
		// Re-parse any flags that appeared after the positional URL
		// (Go's flag package stops at the first non-flag arg).
		if len(remaining) > 1 {
			fs.Parse(remaining[1:])
		}
	} else {
		// No URL provided — resolve from config.
		urls := cfg.Hub.URLs
		switch len(urls) {
		case 0:
			return fmt.Errorf("no hub URLs configured; run: mg config --set hub.urls or pass a URL")
		case 1:
			hubURL = strings.TrimRight(urls[0], "/")
			fmt.Printf("Using hub: %s\n", hubURL)
		default:
			fmt.Println("Multiple hubs configured. Select one to join:")
			for i, u := range urls {
				fmt.Printf("  [%d] %s\n", i+1, u)
			}
			fmt.Print("Enter number: ")
			var choice int
			scanner := bufio.NewScanner(os.Stdin)
			if scanner.Scan() {
				fmt.Sscanf(strings.TrimSpace(scanner.Text()), "%d", &choice)
			}
			if choice < 1 || choice > len(urls) {
				return fmt.Errorf("invalid selection")
			}
			hubURL = strings.TrimRight(urls[choice-1], "/")
		}
	}

	if *operatorID == "" {
		*operatorID = cfg.OperatorID
	}
	if *ollamaURL == "" {
		*ollamaURL = cfg.Agent.OllamaURL
	}
	if *name == "" {
		h, _ := os.Hostname()
		*name = h
	}
	if *apiKey == "" {
		*apiKey = cfg.Hub.APIKey
	}

	// Resolve supported model list
	modelList := probeOllamaModels(*ollamaURL)
	if len(modelList) == 0 {
		// Fall back to declared models
		if *models != "" {
			for _, m := range strings.Split(*models, ",") {
				modelList = append(modelList, strings.TrimSpace(m))
			}
		} else {
			modelList = []string{*model}
		}
	}
	// Merge --model into list if not present
	found := false
	for _, m := range modelList {
		if m == *model {
			found = true
			break
		}
	}
	if !found && *model != "" {
		modelList = append([]string{*model}, modelList...)
	}

	// Baseline model check — llama3.2 is required for full grid compatibility.
	hasBaseline := false
	for _, m := range modelList {
		base := strings.TrimSuffix(m, ":latest")
		if base == "llama3.2" {
			hasBaseline = true
			break
		}
	}
	if !hasBaseline {
		return fmt.Errorf(
			"baseline model 'llama3.2' not found in Ollama — run: ollama pull llama3.2\n" +
				"  advertised models: %s", strings.Join(modelList, ", "))
	}

	gpus := probeGPUs()
	agentID := "agent-" + uuid.New().String()[:8]
	fmt.Printf("Agent ID    : %s\n", agentID)
	fmt.Printf("Operator    : %s\n", *operatorID)
	fmt.Printf("Hub         : %s\n", hubURL)
	fmt.Printf("Models      : %s\n", strings.Join(modelList, ", "))
	fmt.Printf("Pull mode   : %v\n", *pullMode)
	fmt.Printf("Signed      : %v\n", *sign)
	fmt.Println()

	// Load or generate identity keypair
	var ident *identity.Identity
	if *sign {
		home, _ := os.UserHomeDir()
		idDir := filepath.Join(home, ".igrid")
		var err error
		ident, err = identity.LoadOrCreate(idDir)
		if err != nil {
			return fmt.Errorf("identity: %w", err)
		}
		fmt.Printf("Identity    : %s\n", idDir+"/agent_key.pem")
		fmt.Printf("Public key  : %s…\n\n", ident.PublicKeyB64()[:20])
	}

	// Build and POST JoinRequest
	req := schema.JoinRequest{
		OperatorID:      *operatorID,
		AgentID:         agentID,
		Host:            detectLANIP(),
		Port:            9010,
		Name:            *name,
		GPUs:            gpus,
		SupportedModels: modelList,
		PullMode:        *pullMode,
		APIKey:          *apiKey,
	}
	if ident != nil {
		req.Timestamp = identity.TimestampNow()
		req.PublicKey = ident.PublicKeyB64()
		req.Signature = ident.Sign(identity.MakeChallenge(agentID, req.Timestamp))
	}

	ack, err := postJoin(hubURL, req)
	if err != nil {
		return fmt.Errorf("join failed: %w", err)
	}
	if !ack.Accepted {
		return fmt.Errorf("hub rejected join: %s", ack.Message)
	}

	fmt.Printf("Joined grid  hub=%s  tier=%s  status=%s\n", ack.HubID, ack.Tier, ack.Status)
	if ack.Message != "" {
		fmt.Printf("Message: %s\n", ack.Message)
	}

	// Persist the hub URL so subsequent commands (mg tasks, mg agents, etc.)
	// use the same hub without requiring --hub-url every time.
	cfg.Hub.URLs = []string{hubURL}
	if err := SaveConfig(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not save hub URL to config: %v\n", err)
	} else {
		fmt.Printf("Hub URL saved to ~/.igrid/config.yaml\n")
	}
	fmt.Println("\nPulsing... (Ctrl+C to leave)")

	// Pulse loop — runs until SIGINT/SIGTERM
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	if *pullMode {
		// Pull mode: connect to hub SSE stream and process tasks.
		go pullTaskLoop(hubURL, agentID, *ollamaURL, sig)
	} else {
		// Push mode: start HTTP server so hub can POST tasks to /run.
		agentAddr := fmt.Sprintf("%s:%d", cfg.Agent.Host, cfg.Agent.Port)
		go startAgentServer(agentAddr, *ollamaURL)
		fmt.Printf("  listening for pushed tasks on %s\n", agentAddr)
	}

	ticker := time.NewTicker(*pulseInterval)
	defer ticker.Stop()

	for {
		select {
		case <-sig:
			fmt.Println("\nLeaving grid...")
			postLeave(hubURL, agentID, *operatorID)
			return nil

		case <-ticker.C:
			gpuUtil, vramUsed := sampleGPUStats()
			pulse := schema.PulseReport{
				OperatorID:        *operatorID,
				AgentID:           agentID,
				Status:            schema.StatusOnline,
				GPUUtilizationPct: gpuUtil,
				VramUsedGB:        vramUsed,
			}
			if ident != nil {
				pulse.Timestamp = identity.TimestampNow()
				pulse.Signature = ident.Sign(identity.MakeChallenge(agentID, pulse.Timestamp))
			}
			if err := sendPulse(hubURL, pulse); err != nil {
				fmt.Fprintf(os.Stderr, "pulse error: %v\n", err)
			} else {
				fmt.Printf("  pulse  agent=%s  ts=%s\n", agentID[:12], time.Now().Format("15:04:05"))
			}
		}
	}
}

// startAgentServer starts a minimal HTTP server for push-mode task delivery.
func startAgentServer(addr, ollamaURL string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/run", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var task schema.TaskRequest
		body, err := io.ReadAll(r.Body)
		if err != nil || json.Unmarshal(body, &task) != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		t0 := time.Now()
		result, err := callOllama(ollamaURL, task)
		if err != nil {
			result = &schema.TaskResult{
				TaskID: task.TaskID,
				State:  schema.StateFailed,
				Error:  err.Error(),
			}
		} else {
			result.LatencyMs = float64(time.Since(t0).Milliseconds())
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	})
	if err := http.ListenAndServe(addr, mux); err != nil {
		fmt.Fprintf(os.Stderr, "agent server error: %v\n", err)
	}
}

// pullTaskLoop connects to the hub's SSE task stream and processes tasks via Ollama.
func pullTaskLoop(hubURL, agentID, ollamaURL string, sig chan os.Signal) {
	streamURL := fmt.Sprintf("%s/task-stream/%s", hubURL, agentID)
	for {
		select {
		case <-sig:
			return
		default:
		}

		if err := connectAndProcess(streamURL, hubURL, ollamaURL); err != nil {
			fmt.Fprintf(os.Stderr, "  pull stream error: %v — reconnecting in 5s\n", err)
			time.Sleep(5 * time.Second)
		}
	}
}

func connectAndProcess(streamURL, hubURL, ollamaURL string) error {
	client := &http.Client{Timeout: 0} // no timeout for SSE
	resp, err := client.Get(streamURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("stream returned %d", resp.StatusCode)
	}

	fmt.Printf("  pull stream connected: %s\n", streamURL)
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		var task schema.TaskRequest
		if err := json.Unmarshal([]byte(payload), &task); err != nil {
			fmt.Fprintf(os.Stderr, "  pull: bad task payload: %v\n", err)
			continue
		}
		fmt.Printf("  pull task=%s  model=%s\n", task.TaskID, task.Model)
		go runAndReport(task, hubURL, ollamaURL)
	}
	return scanner.Err()
}

// runAndReport sends a task to Ollama and POSTs the result back to the hub.
func runAndReport(task schema.TaskRequest, hubURL, ollamaURL string) {
	t0 := time.Now()
	result, err := callOllama(ollamaURL, task)
	if err != nil {
		result = &schema.TaskResult{
			TaskID: task.TaskID,
			State:  schema.StateFailed,
			Error:  err.Error(),
		}
		fmt.Fprintf(os.Stderr, "  ollama error task=%s: %v\n", task.TaskID, err)
	} else {
		result.LatencyMs = float64(time.Since(t0).Milliseconds())
		fmt.Printf("  completed task=%s  tokens=%d  latency=%.0fms\n",
			task.TaskID, result.OutputTokens, result.LatencyMs)
	}
	if _, postErr := postJSON(hubURL+"/results", result); postErr != nil {
		fmt.Fprintf(os.Stderr, "  result post error task=%s: %v\n", task.TaskID, postErr)
	}
}

// callOllama sends a generate request to the local Ollama instance.
func callOllama(ollamaURL string, task schema.TaskRequest) (*schema.TaskResult, error) {
	prompt := task.Prompt
	if task.System != "" {
		prompt = task.System + "\n\n" + prompt
	}
	reqBody := map[string]interface{}{
		"model":  task.Model,
		"prompt": prompt,
		"stream": false,
	}
	if task.MaxTokens > 0 {
		reqBody["options"] = map[string]interface{}{"num_predict": task.MaxTokens}
	}

	body, _ := json.Marshal(reqBody)
	timeout := time.Duration(task.TimeoutS+10) * time.Second
	if timeout < 120*time.Second {
		timeout = 120 * time.Second
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Post(ollamaURL+"/api/generate", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ollama %d: %s", resp.StatusCode, string(b))
	}

	var out struct {
		Response           string `json:"response"`
		PromptEvalCount    int    `json:"prompt_eval_count"`
		EvalCount          int    `json:"eval_count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &schema.TaskResult{
		TaskID:       task.TaskID,
		State:        schema.StateComplete,
		Content:      out.Response,
		Model:        task.Model,
		InputTokens:  out.PromptEvalCount,
		OutputTokens: out.EvalCount,
	}, nil
}

// probeGPUs queries nvidia-smi for local GPU info.
// Returns nil if nvidia-smi is not available (non-NVIDIA or no driver).
func probeGPUs() []schema.GPUInfo {
	out, err := exec.Command("nvidia-smi",
		"--query-gpu=index,name,memory.total",
		"--format=csv,noheader,nounits").Output()
	if err != nil {
		return nil
	}
	var gpus []schema.GPUInfo
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, ", ", 3)
		if len(parts) != 3 {
			continue
		}
		var idx int
		var vramMiB float64
		fmt.Sscanf(strings.TrimSpace(parts[0]), "%d", &idx)
		fmt.Sscanf(strings.TrimSpace(parts[2]), "%f", &vramMiB)
		gpus = append(gpus, schema.GPUInfo{
			Index:  idx,
			Model:  strings.TrimSpace(parts[1]),
			VramGB: vramMiB / 1024.0,
		})
	}
	return gpus
}

// sampleGPUStats queries nvidia-smi for the current GPU utilization and VRAM usage
// of GPU 0. Returns zeros if nvidia-smi is unavailable.
func sampleGPUStats() (utilPct, vramUsedGB float64) {
	out, err := exec.Command("nvidia-smi",
		"--query-gpu=utilization.gpu,memory.used",
		"--format=csv,noheader,nounits",
		"--id=0").Output()
	if err != nil {
		return 0, 0
	}
	line := strings.TrimSpace(string(out))
	parts := strings.SplitN(line, ", ", 2)
	if len(parts) != 2 {
		return 0, 0
	}
	fmt.Sscanf(strings.TrimSpace(parts[0]), "%f", &utilPct)
	var vramMiB float64
	fmt.Sscanf(strings.TrimSpace(parts[1]), "%f", &vramMiB)
	vramUsedGB = vramMiB / 1024.0
	return utilPct, vramUsedGB
}

// probeOllamaModels queries Ollama's local API for installed models.
// Returns nil if Ollama is not reachable (agent still joins with declared models).
func probeOllamaModels(ollamaURL string) []string {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(ollamaURL + "/api/tags")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	var result struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil
	}
	names := make([]string, 0, len(result.Models))
	for _, m := range result.Models {
		names = append(names, m.Name)
	}
	return names
}

func postJoin(hubURL string, req schema.JoinRequest) (*schema.JoinAck, error) {
	data, err := postJSON(hubURL+"/join", req)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(data)
	var ack schema.JoinAck
	if err := json.Unmarshal(b, &ack); err != nil {
		return nil, err
	}
	return &ack, nil
}

func sendPulse(hubURL string, pulse schema.PulseReport) error {
	_, err := postJSON(hubURL+"/pulse", pulse)
	return err
}

func postLeave(hubURL, agentID, operatorID string) {
	postJSON(hubURL+"/leave", map[string]string{ //nolint:errcheck
		"agent_id":    agentID,
		"operator_id": operatorID,
	})
}
