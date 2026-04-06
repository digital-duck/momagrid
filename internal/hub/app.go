package hub

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/digital-duck/momagrid/internal/identity"
	"github.com/digital-duck/momagrid/internal/schema"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
)

// HubConfig holds configuration for the hub server.
type HubConfig struct {
	HubID              string
	OperatorID         string
	DBPath             string
	HubURL             string
	APIKey             string
	AdminMode          bool
	MaxConcurrentTasks int
	MaxRetries         int // max requeue attempts for transient errors (default 3)
	AllowedCountries   []string
	MaxPromptChars     int // soft limit; HTTP 413 if exceeded (default 50000)
	MaxQueueDepth      int // HTTP 503 if PENDING tasks >= this (default 1000)
	RateLimit          int // max requests per minute per IP (default 60)
	BurstThreshold     int // max requests per 10s before flood watchlist (default 200)
}

// App is the hub HTTP application.
type App struct {
	Config      HubConfig
	State       *GridState
	Cluster     *ClusterManager
	SSEQueues   *SSEManager
	RateLimiter *RateLimiter
	Notifier    *Notifier
	Router      chi.Router
	stopCh      chan struct{}
}

// NewApp creates and wires up the hub application.
func NewApp(cfg HubConfig) (*App, error) {
	if cfg.HubID == "" {
		cfg.HubID = "hub-" + uuid.New().String()[:8]
	}
	if cfg.OperatorID == "" {
		cfg.OperatorID = "duck"
	}
	if cfg.DBPath == "" {
		cfg.DBPath = ".igrid/hub.sqlite3"
	}
	if cfg.MaxConcurrentTasks == 0 {
		cfg.MaxConcurrentTasks = 3
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 3
	}
	if cfg.MaxPromptChars == 0 {
		cfg.MaxPromptChars = 50000
	}
	if cfg.MaxQueueDepth == 0 {
		cfg.MaxQueueDepth = 1000
	}
	if cfg.RateLimit == 0 {
		cfg.RateLimit = 60
	}
	if cfg.BurstThreshold == 0 {
		cfg.BurstThreshold = 200
	}

	db, err := InitDB(cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("init db: %w", err)
	}

	driver := "sqlite"
	if strings.HasPrefix(cfg.DBPath, "postgres://") || strings.HasPrefix(cfg.DBPath, "postgresql://") {
		driver = "postgres"
	}

	// Use specific logic for initial config insertion
	if driver == "postgres" {
		for _, kv := range [][2]string{{"hub_id", cfg.HubID}, {"operator_id", cfg.OperatorID}, {"hub_url", cfg.HubURL}} {
			db.Exec("INSERT INTO hub_config(key,value) VALUES ($1,$2) ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value", kv[0], kv[1])
		}
	} else {
		for _, kv := range [][2]string{{"hub_id", cfg.HubID}, {"operator_id", cfg.OperatorID}, {"hub_url", cfg.HubURL}} {
			db.Exec("INSERT OR REPLACE INTO hub_config(key,value) VALUES (?,?)", kv[0], kv[1])
		}
	}

	state := &GridState{DB: db, HubID: cfg.HubID, OperatorID: cfg.OperatorID, Driver: driver, MaxRetries: cfg.MaxRetries}
	cluster := &ClusterManager{State: state, ThisHubURL: cfg.HubURL}
	sseQueues := NewSSEManager()
	rl := NewRateLimiter(cfg.RateLimit, 60, cfg.BurstThreshold, 10)
	notifier := &Notifier{}

	app := &App{
		Config:      cfg,
		State:       state,
		Cluster:     cluster,
		SSEQueues:   sseQueues,
		RateLimiter: rl,
		Notifier:    notifier,
		stopCh:      make(chan struct{}),
	}

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	// Routes
	r.Get("/health", app.handleHealth)
	r.With(app.checkAPIKey).Post("/join", app.handleJoin)
	r.Post("/leave", app.handleLeave)
	r.Post("/pulse", app.handlePulse)
	r.Post("/tasks", app.handleSubmitTask)
	r.Get("/tasks", app.handleListTasks)
	r.Get("/tasks/{taskID}", app.handleGetTask)
	r.Post("/jobs", app.handleSubmitJob)
	r.Get("/jobs", app.handleListJobs)
	r.Get("/jobs/{jobID}", app.handleGetJob)
	r.Get("/agents", app.handleListAgents)
	r.Get("/agents/pending", app.handleListPendingAgents)
	r.Post("/agents/{agentID}/approve", app.handleApproveAgent)
	r.Post("/agents/{agentID}/reject", app.handleRejectAgent)
	r.Get("/rewards", app.handleRewards)
	r.Get("/logs", app.handleLogs)
	r.Post("/cluster/handshake", app.handleClusterHandshake)
	r.Post("/cluster/capabilities", app.handleClusterCapabilities)
	r.Post("/cluster/peers", app.handleAddPeer)
	r.Get("/cluster/status", app.handleClusterStatus)
	r.Get("/task-stream/{agentID}", app.handleTaskStream)
	r.Post("/results", app.handleResults)
	r.Post("/cluster/result", app.handleClusterResult)
	r.Get("/watchlist", app.handleListWatchlist)
	r.Delete("/watchlist/{entityID}", app.handleUnblock)

	app.Router = r
	return app, nil
}

// Start launches background goroutines and the HTTP server.
func (a *App) Start(addr string) error {
	go AgentMonitor(a.State, a.stopCh)
	go ClusterMonitor(a.State, a.Cluster, a.stopCh)
	go DispatchLoop(a.State, a.SSEQueues, a.Config.MaxConcurrentTasks, a.stopCh)
	go JobLoop(a.State, a.SSEQueues, a.Notifier, a.Config.MaxConcurrentTasks, a.stopCh)

	log.Printf("hub %s started  url=%s  db=%s  admin=%v  max_concurrent=%d",
		a.Config.HubID, a.Config.HubURL, a.Config.DBPath,
		a.Config.AdminMode, a.Config.MaxConcurrentTasks)

	return http.ListenAndServe(addr, a.Router)
}

// Stop signals background goroutines to exit.
func (a *App) Stop() {
	close(a.stopCh)
	a.State.DB.Close()
}

// ── Middleware ───────────────────────────────────────────────────────

// clientIP extracts the client IP from X-Forwarded-For or RemoteAddr.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.SplitN(xff, ",", 2)
		return strings.TrimSpace(parts[0])
	}
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i != -1 {
		return host[:i]
	}
	return host
}

// checkRateLimit checks the rate limiter and watchlist for the request IP.
// Returns false and writes the HTTP error if the request should be rejected.
func (a *App) checkRateLimit(w http.ResponseWriter, r *http.Request) bool {
	ip := clientIP(r)
	if a.State.IsWatchlisted(ip) {
		writeJSON(w, 403, map[string]string{"detail": "IP is blocked"})
		return false
	}
	allowed, isFlood := a.RateLimiter.Check(ip)
	if isFlood {
		expiry := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339)
		a.State.AddToWatchlist("ip", ip, "flood detected", "SUSPENDED", expiry)
		log.Printf("rate limiter: flood from %s — auto-suspended 24h", ip)
		writeJSON(w, 429, map[string]string{"detail": "flood detected — IP suspended for 24h"})
		return false
	}
	if !allowed {
		writeJSON(w, 429, map[string]string{"detail": "rate limit exceeded"})
		return false
	}
	return true
}

func (a *App) checkAPIKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.Config.APIKey != "" && r.Header.Get("X-API-Key") != a.Config.APIKey {
			writeJSON(w, 401, map[string]string{"detail": "Invalid API key"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ── Handlers ────────────────────────────────────────────────────────

func (a *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	agents, _ := a.State.ListAgents()
	online := 0
	for _, ag := range agents {
		if s, _ := ag["status"].(string); s != "OFFLINE" {
			online++
		}
	}
	writeJSON(w, 200, map[string]interface{}{
		"hub_id":        a.State.HubID,
		"operator_id":   a.State.OperatorID,
		"status":        "ok",
		"agents_online": online,
		"time":          time.Now().UTC().Format(time.RFC3339),
	})
}

func (a *App) handleJoin(w http.ResponseWriter, r *http.Request) {
	if !a.checkRateLimit(w, r) {
		return
	}
	var req schema.JoinRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, 400, map[string]string{"detail": err.Error()})
		return
	}

	// Ed25519: if the agent presents a public key + signature, verify before registering.
	// Agents without a key are accepted (backward-compat / trusted LAN mode).
	if req.PublicKey != "" && req.Signature != "" {
		challenge := string(identity.MakeChallenge(req.AgentID, req.Timestamp))
		if err := identity.Verify(req.PublicKey, challenge, req.Signature); err != nil {
			log.Printf("agent %s join rejected: bad signature: %v", req.AgentID, err)
			writeJSON(w, 401, map[string]string{"detail": "signature verification failed"})
			return
		}
		log.Printf("agent %s identity verified (Ed25519)", req.AgentID)
	}

	// Classify initial tier from VRAM if GPU info provided; fall back to BRONZE.
	initialTier := schema.TierBronze
	for _, gpu := range req.GPUs {
		if t := schema.TierFromVRAM(gpu.VramGB); schema.TierOrder[t] < schema.TierOrder[initialTier] {
			initialTier = t
		}
	}
	initialStatus, err := a.State.RegisterAgent(req, initialTier, a.Config.AdminMode)
	if err != nil {
		writeJSON(w, 500, map[string]string{"detail": err.Error()})
		return
	}
	log.Printf("agent %s joined  status=%s  signed=%v  admin_mode=%v",
		req.AgentID, initialStatus, req.PublicKey != "", a.Config.AdminMode)

	ack := schema.JoinAck{
		Accepted:   true,
		HubID:      a.State.HubID,
		OperatorID: req.OperatorID,
		AgentID:    req.AgentID,
		Tier:       initialTier,
		Status:     initialStatus,
		Message:    "Welcome to the grid.",
	}
	if initialStatus == string(schema.StatusPendingApproval) {
		ack.Message = "Pending verification. A benchmark task has been sent."
		go a.verifyAgent(req)
	}
	writeJSON(w, 200, ack)
}

func (a *App) handleLeave(w http.ResponseWriter, r *http.Request) {
	var req schema.LeaveRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, 400, map[string]string{"detail": err.Error()})
		return
	}
	a.State.RemoveAgent(req.AgentID)
	writeJSON(w, 200, schema.LeaveAck{OK: true})
}

func (a *App) handlePulse(w http.ResponseWriter, r *http.Request) {
	var report schema.PulseReport
	if err := readJSON(r, &report); err != nil {
		writeJSON(w, 400, map[string]string{"detail": err.Error()})
		return
	}

	// Ed25519: verify pulse signature if the agent registered a public key.
	// If the agent has a key on file but sends no signature, reject (key downgrade attack).
	storedKey := a.State.AgentPublicKey(report.AgentID)
	if storedKey != "" {
		if report.Signature == "" {
			writeJSON(w, 401, map[string]string{"detail": "signed agent must include pulse signature"})
			return
		}
		challenge := string(identity.MakeChallenge(report.AgentID, report.Timestamp))
		if err := identity.Verify(storedKey, challenge, report.Signature); err != nil {
			log.Printf("pulse from %s rejected: bad signature: %v", report.AgentID, err)
			writeJSON(w, 401, map[string]string{"detail": "pulse signature verification failed"})
			return
		}
	}

	a.State.RecordPulse(report.AgentID, report.Status, report.GPUUtilizationPct,
		report.VramUsedGB, report.CurrentTPS, report.TasksCompleted)
	writeJSON(w, 200, schema.PulseAck{OK: true, HubTime: time.Now().UTC().Format(time.RFC3339)})
}

func (a *App) handleSubmitTask(w http.ResponseWriter, r *http.Request) {
	if !a.checkRateLimit(w, r) {
		return
	}
	var req schema.TaskRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, 400, map[string]string{"detail": err.Error()})
		return
	}
	// Prompt size enforcement (spec §2.4): HTTP 413 if > MaxPromptChars
	if len(req.Prompt) > a.Config.MaxPromptChars {
		writeJSON(w, 413, map[string]string{"detail": fmt.Sprintf(
			"prompt exceeds limit (%d > %d chars)", len(req.Prompt), a.Config.MaxPromptChars)})
		return
	}
	// Queue depth enforcement (spec §2.4): HTTP 503 if too many PENDING tasks
	if pending, _ := a.State.CountPendingTasks(); pending >= a.Config.MaxQueueDepth {
		writeJSON(w, 503, map[string]string{"detail": "queue full — try again later"})
		return
	}
	if req.TaskID == "" {
		req.TaskID = uuid.New().String()
	}
	req.ApplyDefaults()
	if err := a.State.SubmitTask(req); err != nil {
		writeJSON(w, 500, map[string]string{"detail": err.Error()})
		return
	}
	writeJSON(w, 202, map[string]interface{}{"task_id": req.TaskID, "state": string(schema.StatePending)})
}

func (a *App) handleGetTask(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskID")
	row, err := a.State.GetTask(taskID)
	if err != nil || row == nil {
		writeJSON(w, 404, map[string]string{"detail": "Task not found"})
		return
	}
	state := fmt.Sprint(row["state"])
	resp := schema.TaskStatusResponse{TaskID: taskID, State: schema.TaskState(state)}
	if state == string(schema.StateComplete) || state == string(schema.StateFailed) {
		resp.Result = &schema.TaskResult{
			TaskID:       taskID,
			State:        schema.TaskState(state),
			Content:      strVal(row["content"]),
			Model:        strVal(row["model"]),
			InputTokens:  toInt(row["input_tokens"]),
			OutputTokens: toInt(row["output_tokens"]),
			LatencyMs:    toFloat(row["latency_ms"]),
			AgentID:      strVal(row["agent_id"]),
			AgentName:    strVal(row["agent_name"]),
			AgentHost:    strVal(row["agent_host"]),
			CompletedAt:  strVal(row["updated_at"]),
			Error:        strVal(row["error"]),
		}
	}
	writeJSON(w, 200, resp)
}

func (a *App) handleListTasks(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			limit = n
		}
	}
	tasks, _ := a.State.ListTasks(limit)
	writeJSON(w, 200, map[string]interface{}{"tasks": tasks})
}

func (a *App) handleListAgents(w http.ResponseWriter, r *http.Request) {
	agents, _ := a.State.ListAgents()
	writeJSON(w, 200, map[string]interface{}{"agents": agents})
}

func (a *App) handleListPendingAgents(w http.ResponseWriter, r *http.Request) {
	agents, _ := a.State.ListPendingAgents()
	writeJSON(w, 200, map[string]interface{}{"agents": agents})
}

func (a *App) handleApproveAgent(w http.ResponseWriter, r *http.Request) {
	agentID := chi.URLParam(r, "agentID")
	ok, err := a.State.ApproveAgent(agentID)
	if err != nil || !ok {
		writeJSON(w, 404, map[string]string{"detail": "Agent not found"})
		return
	}
	log.Printf("agent %s manually approved", agentID)
	writeJSON(w, 200, map[string]interface{}{"ok": true, "agent_id": agentID, "status": "ONLINE"})
}

func (a *App) handleRejectAgent(w http.ResponseWriter, r *http.Request) {
	agentID := chi.URLParam(r, "agentID")
	ok, err := a.State.RejectAgent(agentID)
	if err != nil || !ok {
		writeJSON(w, 404, map[string]string{"detail": "Agent not found"})
		return
	}
	log.Printf("agent %s rejected", agentID)
	writeJSON(w, 200, map[string]interface{}{"ok": true, "agent_id": agentID, "status": "OFFLINE"})
}

func (a *App) handleRewards(w http.ResponseWriter, r *http.Request) {
	summary, _ := a.State.RewardSummary()
	writeJSON(w, 200, map[string]interface{}{"summary": summary})
}

func (a *App) handleLogs(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			limit = n
		}
	}
	logs, _ := a.State.RecentPulseLogs(limit)
	writeJSON(w, 200, map[string]interface{}{"logs": logs})
}

func (a *App) handleClusterHandshake(w http.ResponseWriter, r *http.Request) {
	var hs schema.PeerHandshake
	if err := readJSON(r, &hs); err != nil {
		writeJSON(w, 400, map[string]string{"detail": err.Error()})
		return
	}
	a.State.AddPeer(hs.HubID, hs.HubURL, hs.OperatorID)
	agents, _ := a.State.ListAgents()
	caps := CapabilitiesFromAgents(agents)
	writeJSON(w, 200, schema.PeerHandshakeAck{
		Accepted:     true,
		HubID:        a.State.HubID,
		HubURL:       a.Config.HubURL,
		Capabilities: caps,
	})
}

func (a *App) handleClusterCapabilities(w http.ResponseWriter, r *http.Request) {
	var update schema.PeerCapabilityUpdate
	if err := readJSON(r, &update); err != nil {
		writeJSON(w, 400, map[string]string{"detail": err.Error()})
		return
	}
	a.State.MarkPeerSeen(update.HubID)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (a *App) handleAddPeer(w http.ResponseWriter, r *http.Request) {
	var body map[string]string
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, 400, map[string]string{"detail": err.Error()})
		return
	}
	peerURL := body["url"]
	if peerURL == "" {
		writeJSON(w, 400, map[string]string{"detail": "url is required"})
		return
	}
	ack, err := a.Cluster.AddPeer(peerURL)
	if err != nil {
		writeJSON(w, 502, map[string]string{"detail": err.Error()})
		return
	}
	writeJSON(w, 200, ack)
}

func (a *App) handleClusterStatus(w http.ResponseWriter, r *http.Request) {
	peers, _ := a.State.ListPeers()
	writeJSON(w, 200, schema.ClusterStatus{ThisHubID: a.State.HubID, Peers: peers})
}

// handleClusterResult receives a webhook callback from a peer hub when a
// forwarded task completes. The peer fires POST /cluster/result with the
// task result; this hub records it, unblocking the waiting ForwardTask call.
//
// Flow: Hub A forwards task → Hub B → Hub B agent completes → Hub B POSTs
//
//	result here → Hub A's waitForResult loop sees COMPLETE and returns.
func (a *App) handleClusterResult(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		TaskID string             `json:"task_id"`
		Result *schema.TaskResult `json:"result"`
		Error  string             `json:"error"`
	}
	if err := readJSON(r, &payload); err != nil {
		writeJSON(w, 400, map[string]string{"detail": err.Error()})
		return
	}
	if payload.TaskID == "" {
		writeJSON(w, 400, map[string]string{"detail": "task_id required"})
		return
	}

	if payload.Error != "" || payload.Result == nil {
		a.State.FailTask(payload.TaskID, payload.Error)
		log.Printf("cluster result callback: task %s failed: %s", payload.TaskID, payload.Error)
	} else {
		payload.Result.TaskID = payload.TaskID
		a.State.CompleteTask(payload.TaskID, *payload.Result)
		log.Printf("cluster result callback: task %s complete via peer  tokens=%d",
			payload.TaskID, payload.Result.OutputTokens)
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (a *App) handleTaskStream(w http.ResponseWriter, r *http.Request) {
	agentID := chi.URLParam(r, "agentID")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, 500, map[string]string{"detail": "streaming not supported"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	queue := a.SSEQueues.Register(agentID)
	defer a.SSEQueues.Unregister(agentID)
	log.Printf("SSE stream opened for agent %s", agentID)

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			log.Printf("SSE stream closed for agent %s", agentID)
			return
		case task, ok := <-queue:
			if !ok {
				return
			}
			data, _ := json.Marshal(task)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		case <-time.After(15 * time.Second):
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

func (a *App) handleResults(w http.ResponseWriter, r *http.Request) {
	var result schema.TaskResult
	if err := readJSON(r, &result); err != nil {
		writeJSON(w, 400, map[string]string{"detail": err.Error()})
		return
	}
	row, _ := a.State.GetTask(result.TaskID)
	if row == nil {
		writeJSON(w, 404, map[string]string{"detail": "Task not found"})
		return
	}
	state := fmt.Sprint(row["state"])
	if state == string(schema.StateComplete) || state == string(schema.StateFailed) {
		writeJSON(w, 200, map[string]interface{}{"ok": true, "detail": "already completed"})
		return
	}
	// For transient Ollama errors (EOF, connection refused), requeue via
	// FailTask so the dispatcher retries on a different agent (up to maxRetries).
	if result.State == schema.StateFailed && isTransientOllamaError(result.Error) {
		log.Printf("transient ollama error on task %s — requeueing: %s", result.TaskID, result.Error)
		if err := a.State.FailTask(result.TaskID, result.Error); err != nil {
			log.Printf("requeue error for task %s: %v", result.TaskID, err)
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	a.State.CompleteTask(result.TaskID, result)
	if result.State == schema.StateComplete && result.OutputTokens > 0 {
		agentID := result.AgentID
		if agentID == "" {
			agentID = strVal(row["agent_id"])
		}
		operatorID := "unknown"
		if agentID != "" {
			a.State.DB.QueryRow(fmt.Sprintf("SELECT operator_id FROM agents WHERE agent_id=%s", a.State.q(1)), agentID).Scan(&operatorID)
		}
		a.State.RecordReward(operatorID, agentID, result.TaskID, result.OutputTokens, float64(result.OutputTokens)/1000.0)
	}
	log.Printf("results received for task %s from agent %s", result.TaskID, result.AgentID)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ── Verification background task ────────────────────────────────────

func (a *App) verifyAgent(req schema.JoinRequest) {
	model := "llama3"
	if len(req.CachedModels) > 0 {
		model = req.CachedModels[0]
	} else if len(req.SupportedModels) > 0 {
		model = req.SupportedModels[0]
	}
	vtask := PickVerificationTask(req.AgentID, model)

	var host string
	var port int
	err := a.State.DB.QueryRow(fmt.Sprintf("SELECT host, port FROM agents WHERE agent_id=%s", a.State.q(1)), req.AgentID).Scan(&host, &port)
	if err != nil {
		log.Printf("verify: agent %s not found in DB", req.AgentID)
		return
	}

	log.Printf("verify: sending benchmark to agent %s  model=%s  task=%s", req.AgentID, model, vtask.TaskID)

	url := fmt.Sprintf("http://%s:%d/run", host, port)
	body, _ := json.Marshal(vtask)
	client := &http.Client{Timeout: time.Duration(vtask.TimeoutS+10) * time.Second}

	t0 := time.Now()
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("verify: benchmark failed for agent %s: %v", req.AgentID, err)
		return
	}
	defer resp.Body.Close()
	elapsedMs := float64(time.Since(t0).Milliseconds())

	var result schema.TaskResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		log.Printf("verify: bad response from agent %s: %v", req.AgentID, err)
		return
	}

	if !CheckVerificationResult(&result, elapsedMs) {
		log.Printf("verify: agent %s failed benchmark (tokens=%d, elapsed=%.0fms)",
			req.AgentID, result.OutputTokens, elapsedMs)
		return
	}

	// Geo-IP check
	if len(a.Config.AllowedCountries) > 0 {
		geoClient := &http.Client{Timeout: 5 * time.Second}
		geoResp, err := geoClient.Get(fmt.Sprintf("http://ip-api.com/json/%s", host))
		if err == nil {
			defer geoResp.Body.Close()
			var geo map[string]interface{}
			json.NewDecoder(geoResp.Body).Decode(&geo)
			country, _ := geo["countryCode"].(string)
			allowed := false
			for _, c := range a.Config.AllowedCountries {
				if c == country {
					allowed = true
					break
				}
			}
			if !allowed {
				log.Printf("verify: agent %s geo-IP %s not in allowed list", req.AgentID, country)
				return
			}
		} else {
			log.Printf("verify: geo-IP check failed for agent %s: %v (proceeding)", req.AgentID, err)
		}
	}

	// Random sampling
	if ShouldSampleForReview(0.1) {
		log.Printf("verify: agent %s passed but sampled for manual review", req.AgentID)
		return
	}

	a.State.ApproveAgent(req.AgentID)
	log.Printf("verify: agent %s auto-approved", req.AgentID)
}

func (a *App) handleListWatchlist(w http.ResponseWriter, r *http.Request) {
	entries, _ := a.State.ListWatchlist()
	writeJSON(w, 200, map[string]interface{}{"entries": entries})
}

func (a *App) handleUnblock(w http.ResponseWriter, r *http.Request) {
	entityID := chi.URLParam(r, "entityID")
	if err := a.State.RemoveFromWatchlist(entityID); err != nil {
		writeJSON(w, 500, map[string]string{"detail": err.Error()})
		return
	}
	a.RateLimiter.Reset(entityID)
	log.Printf("watchlist: %s unblocked", entityID)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ── JSON helpers ────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func readJSON(r *http.Request, v interface{}) error {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, v)
}

func (a *App) handleSubmitJob(w http.ResponseWriter, r *http.Request) {
	if !a.checkRateLimit(w, r) {
		return
	}
	var req schema.JobRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, 400, map[string]string{"detail": err.Error()})
		return
	}
	if req.JobID == "" {
		req.JobID = "job-" + uuid.New().String()[:8]
	}
	if req.MaxRetries == 0 {
		req.MaxRetries = 3
	}
	if req.MaxTokens == 0 {
		req.MaxTokens = 4096
	}

	if err := a.State.SubmitJob(req); err != nil {
		writeJSON(w, 500, map[string]string{"detail": err.Error()})
		return
	}
	writeJSON(w, 202, map[string]interface{}{"job_id": req.JobID, "state": string(schema.JobQueued)})
}

func (a *App) handleGetJob(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "jobID")
	row, err := a.State.GetJob(jobID)
	if err != nil || row == nil {
		writeJSON(w, 404, map[string]string{"detail": "Job not found"})
		return
	}
	
	state := schema.JobState(strVal(row["state"]))
	resp := schema.JobStatusResponse{
		JobID:     jobID,
		State:     state,
		Model:     strVal(row["model"]),
		CreatedAt: parseTime(row["created_at"]),
		UpdatedAt: parseTime(row["updated_at"]),
	}
	
	if resStr := strVal(row["result"]); resStr != "" {
		var res schema.TaskResult
		json.Unmarshal([]byte(resStr), &res)
		resp.Result = &res
	}
	
	writeJSON(w, 200, resp)
}

func (a *App) handleListJobs(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			limit = n
		}
	}
	jobs, _ := a.State.ListJobs(limit)
	writeJSON(w, 200, map[string]interface{}{"jobs": jobs})
}

func parseTime(v interface{}) time.Time {
	s := strVal(v)
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

func strVal(v interface{}) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

// isTransientOllamaError returns true for known transient Ollama errors that
// are safe to retry on a different agent (EOF, connection refused, reset).
func isTransientOllamaError(errMsg string) bool {
	transient := []string{
		"EOF",
		"connection refused",
		"connection reset by peer",
		"broken pipe",
		"no such host",
		"dial tcp",
	}
	for _, t := range transient {
		if strings.Contains(errMsg, t) {
			return true
		}
	}
	return false
}
