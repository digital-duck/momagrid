package hub

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/digital-duck/momagrid/internal/schema"
)

const agentTimeoutS = 90

// GridState holds shared state backed by SQLite or Postgres.
type GridState struct {
	DB         *sql.DB
	HubID      string
	OperatorID string
	Driver     string // "sqlite" or "postgres"
	MaxRetries int    // max requeue attempts for transient errors
}

// q returns the correct placeholder for the current driver.
func (s *GridState) q(n int) string {
	if s.Driver == "postgres" {
		return fmt.Sprintf("$%d", n)
	}
	return "?"
}

func (s *GridState) now() interface{} {
	if s.Driver == "postgres" {
		return time.Now().UTC()
	}
	return time.Now().UTC().Format(time.RFC3339)
}

// RegisterAgent registers or re-registers an agent. Returns the initial status.
func (s *GridState) RegisterAgent(req schema.JoinRequest, tier schema.ComputeTier, adminMode bool) (string, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	_, err = tx.Exec(fmt.Sprintf("INSERT INTO operators(operator_id) VALUES (%s) ON CONFLICT DO NOTHING", s.q(1)), req.OperatorID)
	if err != nil {
		return "", err
	}

	gpusJSON, _ := json.Marshal(req.GPUs)
	modelsJSON, _ := json.Marshal(req.SupportedModels)
	now := s.now()
	pullMode := 0
	if req.PullMode {
		pullMode = 1
	}

	initialStatus := string(schema.StatusOnline)
	if adminMode {
		var existing sql.NullString
		tx.QueryRow(fmt.Sprintf("SELECT status FROM agents WHERE agent_id=%s", s.q(1)), req.AgentID).Scan(&existing)
		if existing.Valid && existing.String == string(schema.StatusOnline) {
			initialStatus = string(schema.StatusOnline)
		} else {
			initialStatus = string(schema.StatusPendingApproval)
		}
	}

	onConflict := ""
	if s.Driver == "postgres" {
		onConflict = `ON CONFLICT(agent_id) DO UPDATE SET
			name=EXCLUDED.name, host=EXCLUDED.host, port=EXCLUDED.port, status=CASE WHEN agents.status = 'ONLINE' THEN 'ONLINE' ELSE EXCLUDED.status END,
			tier=EXCLUDED.tier, gpus=EXCLUDED.gpus, cpu_cores=EXCLUDED.cpu_cores, ram_gb=EXCLUDED.ram_gb,
			supported_models=EXCLUDED.supported_models, pull_mode=EXCLUDED.pull_mode, last_pulse=EXCLUDED.last_pulse,
			public_key=CASE WHEN EXCLUDED.public_key != '' THEN EXCLUDED.public_key ELSE agents.public_key END`
	} else {
		onConflictStatus := "'ONLINE'"
		if adminMode {
			onConflictStatus = "CASE WHEN agents.status = 'ONLINE' THEN 'ONLINE' ELSE excluded.status END"
		}
		onConflict = fmt.Sprintf(`ON CONFLICT(agent_id) DO UPDATE SET
			name=excluded.name, host=excluded.host, port=excluded.port, status=%s, tier=excluded.tier,
			gpus=excluded.gpus, cpu_cores=excluded.cpu_cores, ram_gb=excluded.ram_gb,
			supported_models=excluded.supported_models, pull_mode=excluded.pull_mode, last_pulse=excluded.last_pulse,
			public_key=CASE WHEN excluded.public_key != '' THEN excluded.public_key ELSE agents.public_key END`,
			onConflictStatus)
	}

	query := fmt.Sprintf(`
		INSERT INTO agents
			(agent_id, operator_id, name, host, port, status, tier, gpus, cpu_cores, ram_gb, supported_models, pull_mode, joined_at, last_pulse, public_key)
		VALUES (%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s)
		%s
	`, s.q(1), s.q(2), s.q(3), s.q(4), s.q(5), s.q(6), s.q(7), s.q(8), s.q(9), s.q(10), s.q(11), s.q(12), s.q(13), s.q(14), s.q(15), onConflict)

	_, err = tx.Exec(query,
		req.AgentID, req.OperatorID, req.Name, req.Host, req.Port,
		initialStatus, string(tier), string(gpusJSON), req.CPUCores, req.RamGB, string(modelsJSON),
		pullMode, now, now, req.PublicKey)
	if err != nil {
		return "", err
	}
	return initialStatus, tx.Commit()
}

func (s *GridState) RemoveAgent(agentID string) error {
	now := s.now()
	s.DB.Exec(fmt.Sprintf("UPDATE agents SET status=%s WHERE agent_id=%s", s.q(1), s.q(2)), string(schema.StatusOffline), agentID)
	s.DB.Exec(fmt.Sprintf(`UPDATE tasks SET state=%s, agent_id=NULL, updated_at=%s
		WHERE agent_id=%s AND state IN (%s,%s)`, s.q(1), s.q(2), s.q(3), s.q(4), s.q(5)),
		string(schema.StatePending), now, agentID,
		string(schema.StateDispatched), string(schema.StateInFlight))
	return nil
}

func (s *GridState) ApproveAgent(agentID string) (bool, error) {
	res, err := s.DB.Exec(fmt.Sprintf("UPDATE agents SET status=%s WHERE agent_id=%s", s.q(1), s.q(2)),
		string(schema.StatusOnline), agentID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *GridState) RejectAgent(agentID string) (bool, error) {
	res, err := s.DB.Exec(fmt.Sprintf("UPDATE agents SET status=%s WHERE agent_id=%s", s.q(1), s.q(2)),
		string(schema.StatusOffline), agentID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *GridState) RecordPulse(agentID string, status schema.AgentStatus, gpuUtil, vramUsed, tps float64, tasksDone int) error {
	now := s.now()
	var tierVal *string
	if tps > 0 {
		t := string(schema.TierFromTPS(tps))
		tierVal = &t
	}
	s.DB.Exec(fmt.Sprintf(`UPDATE agents SET status=%s, current_tps=%s, tasks_completed=%s, last_pulse=%s, tier=COALESCE(%s,tier) WHERE agent_id=%s`, 
		s.q(1), s.q(2), s.q(3), s.q(4), s.q(5), s.q(6)),
		string(status), tps, tasksDone, now, tierVal, agentID)
	
	s.DB.Exec(fmt.Sprintf(`INSERT INTO pulse_log (agent_id, status, gpu_util_pct, vram_used_gb, current_tps, tasks_completed, logged_at)
		VALUES (%s,%s,%s,%s,%s,%s,%s)`, s.q(1), s.q(2), s.q(3), s.q(4), s.q(5), s.q(6), s.q(7)),
		agentID, string(status), gpuUtil, vramUsed, tps, tasksDone, now)
	return nil
}

func (s *GridState) ListAgents() ([]map[string]interface{}, error) {
	return queryMaps(s.DB, "SELECT * FROM agents WHERE status != 'OFFLINE' ORDER BY tier")
}

func (s *GridState) ListPendingAgents() ([]map[string]interface{}, error) {
	return queryMaps(s.DB, fmt.Sprintf("SELECT * FROM agents WHERE status=%s ORDER BY joined_at", s.q(1)),
		string(schema.StatusPendingApproval))
}

func (s *GridState) EvictStaleAgents() (int, error) {
	cutoffTime := time.Now().UTC().Add(-time.Duration(agentTimeoutS) * time.Second)
	var cutoff interface{} = cutoffTime.Format(time.RFC3339)
	if s.Driver == "postgres" {
		cutoff = cutoffTime
	}

	rows, err := s.DB.Query(fmt.Sprintf("SELECT agent_id FROM agents WHERE status='ONLINE' AND last_pulse < %s", s.q(1)), cutoff)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var stale []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		stale = append(stale, id)
	}
	for _, id := range stale {
		s.RemoveAgent(id)
	}
	return len(stale), nil
}

func (s *GridState) SubmitTask(req schema.TaskRequest) error {
	now := s.now()
	// callback_url persisted so a restarted hub can re-fire the webhook if needed.
	query := fmt.Sprintf(`INSERT INTO tasks
		(task_id, state, model, prompt, system, max_tokens, temperature, min_tier, min_vram_gb, timeout_s, priority, callback_url, created_at, updated_at)
		VALUES (%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s)`,
		s.q(1), s.q(2), s.q(3), s.q(4), s.q(5), s.q(6), s.q(7), s.q(8), s.q(9), s.q(10), s.q(11), s.q(12), s.q(13), s.q(14))

	_, err := s.DB.Exec(query,
		req.TaskID, string(schema.StatePending), req.Model, req.Prompt, req.System,
		req.MaxTokens, req.Temperature, string(req.MinTier), req.MinVramGB, req.TimeoutS, req.Priority,
		req.CallbackURL, now, now)
	return err
}

func (s *GridState) GetTask(taskID string) (map[string]interface{}, error) {
	query := fmt.Sprintf(`SELECT t.*, a.name as agent_name, a.host as agent_host
		FROM tasks t
		LEFT JOIN agents a ON t.agent_id = a.agent_id
		WHERE t.task_id=%s`, s.q(1))
	rows, err := queryMaps(s.DB, query, taskID)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return rows[0], nil
}

func (s *GridState) ClaimTask(taskID, agentID string) (bool, error) {
	now := s.now()
	res, err := s.DB.Exec(fmt.Sprintf(`UPDATE tasks SET state=%s, agent_id=%s, updated_at=%s
		WHERE task_id=%s AND state=%s`, s.q(1), s.q(2), s.q(3), s.q(4), s.q(5)),
		string(schema.StateDispatched), agentID, now, taskID, string(schema.StatePending))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *GridState) MarkInFlight(taskID string) error {
	_, err := s.DB.Exec(fmt.Sprintf("UPDATE tasks SET state=%s, updated_at=%s WHERE task_id=%s", s.q(1), s.q(2), s.q(3)),
		string(schema.StateInFlight), s.now(), taskID)
	return err
}

func (s *GridState) CompleteTask(taskID string, result schema.TaskResult) error {
	_, err := s.DB.Exec(fmt.Sprintf(`UPDATE tasks SET state=%s, content=%s, input_tokens=%s, output_tokens=%s, latency_ms=%s, error=%s, updated_at=%s
		WHERE task_id=%s`, s.q(1), s.q(2), s.q(3), s.q(4), s.q(5), s.q(6), s.q(7), s.q(8)),
		string(result.State), result.Content, result.InputTokens, result.OutputTokens,
		result.LatencyMs, result.Error, s.now(), taskID)
	return err
}

func (s *GridState) FailTask(taskID, errMsg string) error {
	var retries int
	err := s.DB.QueryRow(fmt.Sprintf("SELECT retries FROM tasks WHERE task_id=%s", s.q(1)), taskID).Scan(&retries)
	if err != nil {
		return err
	}
	retries++
	maxRetries := s.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 3
	}
	newState := schema.StatePending
	if retries >= maxRetries {
		newState = schema.StateFailed
	}
	_, err = s.DB.Exec(fmt.Sprintf(`UPDATE tasks SET state=%s, retries=%s, error=%s, agent_id=NULL, updated_at=%s WHERE task_id=%s`,
		s.q(1), s.q(2), s.q(3), s.q(4), s.q(5)),
		string(newState), retries, errMsg, s.now(), taskID)
	return err
}

func (s *GridState) MarkForwarded(taskID, peerHubID string) error {
	_, err := s.DB.Exec(fmt.Sprintf("UPDATE tasks SET state=%s, peer_hub_id=%s, updated_at=%s WHERE task_id=%s",
		s.q(1), s.q(2), s.q(3), s.q(4)),
		string(schema.StateForwarded), peerHubID, s.now(), taskID)
	return err
}

func (s *GridState) SubmitJob(req schema.JobRequest) error {
	now := s.now()
	notifyJSON, _ := json.Marshal(req.Notify)
	var d interface{}
	if req.Deadline.IsZero() {
		d = nil
	} else if s.Driver == "postgres" {
		d = req.Deadline
	} else {
		d = req.Deadline.Format(time.RFC3339)
	}
	
	query := fmt.Sprintf(`INSERT INTO jobs
		(job_id, state, model, prompt, system, max_tokens, min_tier, deadline, notify, max_retries, created_at, updated_at)
		VALUES (%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s)`,
		s.q(1), s.q(2), s.q(3), s.q(4), s.q(5), s.q(6), s.q(7), s.q(8), s.q(9), s.q(10), s.q(11), s.q(12))

	_, err := s.DB.Exec(query,
		req.JobID, string(schema.JobQueued), req.Model, req.Prompt, req.System,
		req.MaxTokens, string(req.MinTier), d, string(notifyJSON), req.MaxRetries,
		now, now)
	return err
}

func (s *GridState) GetJob(jobID string) (map[string]interface{}, error) {
	rows, err := queryMaps(s.DB, fmt.Sprintf("SELECT * FROM jobs WHERE job_id=%s", s.q(1)), jobID)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return rows[0], nil
}

func (s *GridState) UpdateJobState(jobID string, state schema.JobState, result *schema.TaskResult) error {
	now := s.now()
	var resultJSON interface{}
	if result != nil {
		b, _ := json.Marshal(result)
		resultJSON = string(b)
	} else {
		resultJSON = nil
	}

	_, err := s.DB.Exec(fmt.Sprintf(`UPDATE jobs SET state=%s, result=%s, updated_at=%s WHERE job_id=%s`,
		s.q(1), s.q(2), s.q(3), s.q(4)),
		string(state), resultJSON, now, jobID)
	return err
}

func (s *GridState) ListJobs(limit int) ([]map[string]interface{}, error) {
	return queryMaps(s.DB, fmt.Sprintf("SELECT * FROM jobs ORDER BY created_at DESC LIMIT %s", s.q(1)), limit)
}

func (s *GridState) ListTasks(limit int) ([]map[string]interface{}, error) {
	return queryMaps(s.DB, fmt.Sprintf("SELECT * FROM tasks ORDER BY created_at DESC LIMIT %s", s.q(1)), limit)
}

func (s *GridState) RecordReward(operatorID, agentID, taskID string, tokens int, credits float64) error {
	s.DB.Exec(fmt.Sprintf(`INSERT INTO reward_ledger (operator_id, agent_id, task_id, tokens_generated, credits_earned)
		VALUES (%s,%s,%s,%s,%s)`, s.q(1), s.q(2), s.q(3), s.q(4), s.q(5)), operatorID, agentID, taskID, tokens, credits)
	s.DB.Exec(fmt.Sprintf(`UPDATE operators SET total_tasks=total_tasks+1, total_tokens=total_tokens+%s, total_credits=total_credits+%s
		WHERE operator_id=%s`, s.q(1), s.q(2), s.q(3)), tokens, credits, operatorID)
	return nil
}

func (s *GridState) RewardSummary() ([]map[string]interface{}, error) {
	return queryMaps(s.DB, "SELECT * FROM reward_summary")
}

func (s *GridState) AddPeer(hubID, hubURL, operatorID string) error {
	onConflict := ""
	if s.Driver == "postgres" {
		onConflict = "ON CONFLICT(hub_id) DO UPDATE SET hub_url=EXCLUDED.hub_url, status='ACTIVE', last_seen=EXCLUDED.last_seen"
	} else {
		onConflict = "ON CONFLICT(hub_id) DO UPDATE SET hub_url=excluded.hub_url, status='ACTIVE', last_seen=excluded.last_seen"
	}

	query := fmt.Sprintf(`INSERT INTO peer_hubs (hub_id, hub_url, operator_id, status, last_seen) VALUES (%s,%s,%s,'ACTIVE',%s)
		%s`, s.q(1), s.q(2), s.q(3), s.q(4), onConflict)
	_, err := s.DB.Exec(query, hubID, hubURL, operatorID, s.now())
	return err
}

func (s *GridState) ListPeers() ([]map[string]interface{}, error) {
	return queryMaps(s.DB, "SELECT * FROM peer_hubs")
}

func (s *GridState) MarkPeerSeen(hubID string) {
	s.DB.Exec(fmt.Sprintf("UPDATE peer_hubs SET last_seen=%s, status='ACTIVE' WHERE hub_id=%s", s.q(1), s.q(2)), s.now(), hubID)
}

func (s *GridState) MarkPeerUnreachable(hubID string) {
	s.DB.Exec(fmt.Sprintf("UPDATE peer_hubs SET status='UNREACHABLE' WHERE hub_id=%s", s.q(1)), hubID)
}

// ── Watchlist (spec §14) ──────────────────────────────────────────────────────

// AddToWatchlist inserts or replaces a watchlist entry.
// entityType: "ip" | "operator" | "agent". action: "SUSPENDED" | "BLOCKED".
// expiresAt: RFC3339 string or "" for permanent.
func (s *GridState) AddToWatchlist(entityType, entityID, reason, action, expiresAt string) error {
	if expiresAt == "" {
		if s.Driver == "postgres" {
			_, err := s.DB.Exec(fmt.Sprintf(
				`INSERT INTO watchlist(entity_type,entity_id,reason,action,created_at,expires_at)
				VALUES(%s,%s,%s,%s,%s,NULL)
				ON CONFLICT(entity_type,entity_id) DO UPDATE SET reason=EXCLUDED.reason,action=EXCLUDED.action,created_at=EXCLUDED.created_at,expires_at=NULL`,
				s.q(1), s.q(2), s.q(3), s.q(4), s.q(5)),
				entityType, entityID, reason, action, s.now())
			return err
		}
		_, err := s.DB.Exec(
			`INSERT OR REPLACE INTO watchlist(entity_type,entity_id,reason,action,created_at,expires_at)
			VALUES(?,?,?,?,?,NULL)`,
			entityType, entityID, reason, action, s.now())
		return err
	}
	if s.Driver == "postgres" {
		_, err := s.DB.Exec(fmt.Sprintf(
			`INSERT INTO watchlist(entity_type,entity_id,reason,action,created_at,expires_at)
			VALUES(%s,%s,%s,%s,%s,%s)
			ON CONFLICT(entity_type,entity_id) DO UPDATE SET reason=EXCLUDED.reason,action=EXCLUDED.action,created_at=EXCLUDED.created_at,expires_at=EXCLUDED.expires_at`,
			s.q(1), s.q(2), s.q(3), s.q(4), s.q(5), s.q(6)),
			entityType, entityID, reason, action, s.now(), expiresAt)
		return err
	}
	_, err := s.DB.Exec(
		`INSERT OR REPLACE INTO watchlist(entity_type,entity_id,reason,action,created_at,expires_at)
		VALUES(?,?,?,?,?,?)`,
		entityType, entityID, reason, action, s.now(), expiresAt)
	return err
}

// RemoveFromWatchlist removes an entity from the watchlist by entity_id.
func (s *GridState) RemoveFromWatchlist(entityID string) error {
	_, err := s.DB.Exec(fmt.Sprintf("DELETE FROM watchlist WHERE entity_id=%s", s.q(1)), entityID)
	return err
}

// IsWatchlisted returns true if the entityID is currently watchlisted and not expired.
func (s *GridState) IsWatchlisted(entityID string) bool {
	var reason string
	row := s.DB.QueryRow(fmt.Sprintf(
		`SELECT reason FROM watchlist WHERE entity_id=%s AND (expires_at IS NULL OR expires_at > %s) LIMIT 1`,
		s.q(1), s.q(2)), entityID, s.now())
	return row.Scan(&reason) == nil
}

// ListWatchlist returns all watchlist entries.
func (s *GridState) ListWatchlist() ([]map[string]interface{}, error) {
	return queryMaps(s.DB, "SELECT * FROM watchlist ORDER BY created_at DESC")
}

// CountPendingTasks returns the number of tasks currently in PENDING state.
func (s *GridState) CountPendingTasks() (int, error) {
	var n int
	err := s.DB.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM tasks WHERE state=%s", s.q(1)),
		string(schema.StatePending)).Scan(&n)
	return n, err
}

func (s *GridState) RecentPulseLogs(limit int) ([]map[string]interface{}, error) {
	return queryMaps(s.DB, fmt.Sprintf("SELECT * FROM pulse_log ORDER BY logged_at DESC LIMIT %s", s.q(1)), limit)
}

// AgentPublicKey returns the stored Ed25519 public key (base64) for an agent.
// Returns "" if the agent has not registered a public key (legacy / LAN mode).
func (s *GridState) AgentPublicKey(agentID string) string {
	var pubKey string
	s.DB.QueryRow(fmt.Sprintf("SELECT public_key FROM agents WHERE agent_id=%s", s.q(1)), agentID).Scan(&pubKey)
	return pubKey
}

func queryMaps(db *sql.DB, query string, args ...interface{}) ([]map[string]interface{}, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var result []map[string]interface{}
	for rows.Next() {
		vals := make([]interface{}, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		rows.Scan(ptrs...)
		row := make(map[string]interface{}, len(cols))
		for i, col := range cols {
			v := vals[i]
			if b, ok := v.([]byte); ok {
				v = string(b)
			}
			row[strings.ToLower(col)] = v
		}
		result = append(result, row)
	}
	if result == nil {
		result = []map[string]interface{}{}
	}
	return result, nil
}
