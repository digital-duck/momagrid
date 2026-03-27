package hub

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/digital-duck/momagrid/internal/schema"
)

// TierIndex returns the ordinal for a tier string (lower = better).
func TierIndex(tier string) int {
	if idx, ok := schema.TierOrder[schema.ComputeTier(tier)]; ok {
		return idx
	}
	return 4
}

// agentCandidate pairs an agent row with its active task count.
type agentCandidate struct {
	agent       map[string]interface{}
	activeCount int
}

// normalizeModel strips the ":latest" suffix so "llama3" matches "llama3:latest".
func normalizeModel(m string) string {
	return strings.TrimSuffix(m, ":latest")
}

// PickAgent selects the best eligible agent for a task.
// Active-task counts are pre-fetched in a single GROUP BY query (O(1) DB round-trips)
// instead of one query per agent candidate.
func PickAgent(state *GridState, req schema.TaskRequest, maxConcurrent int) (map[string]interface{}, error) {
	agents, err := state.ListAgents()
	if err != nil {
		return nil, err
	}
	minIdx := TierIndex(string(req.MinTier))

	// Pre-fetch active task counts for all agents in one query.
	activeTaskCounts := map[string]int{}
	if maxConcurrent > 0 {
		q := fmt.Sprintf(`SELECT agent_id, COUNT(*) FROM tasks WHERE state IN (%s,%s) GROUP BY agent_id`,
			state.q(1), state.q(2))
		rows, qErr := state.DB.Query(q, string(schema.StateDispatched), string(schema.StateInFlight))
		if qErr == nil {
			defer rows.Close()
			for rows.Next() {
				var aid string
				var cnt int
				rows.Scan(&aid, &cnt)
				activeTaskCounts[aid] = cnt
			}
		}
	}

	reqModel := normalizeModel(req.Model)

	var candidates []agentCandidate
	for _, a := range agents {
		status, _ := a["status"].(string)
		if status == "OFFLINE" || status == "PENDING_APPROVAL" {
			continue
		}
		tierStr, _ := a["tier"].(string)
		if TierIndex(tierStr) > minIdx {
			continue
		}

		// VRAM filter
		if req.MinVramGB > 0 {
			gpusStr, _ := a["gpus"].(string)
			var gpus []schema.GPUInfo
			json.Unmarshal([]byte(gpusStr), &gpus)
			if len(gpus) == 0 || gpus[0].VramGB < req.MinVramGB {
				continue
			}
		}

		// Model filter — normalize :latest so "llama3" matches "llama3:latest"
		modelsStr, _ := a["supported_models"].(string)
		var models []string
		json.Unmarshal([]byte(modelsStr), &models)
		if len(models) > 0 && req.Model != "" {
			found := false
			for _, m := range models {
				if normalizeModel(m) == reqModel {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}

		// Rate limiting — use pre-fetched count
		agentID, _ := a["agent_id"].(string)
		activeCount := activeTaskCounts[agentID]
		if maxConcurrent > 0 && activeCount >= maxConcurrent {
			continue
		}

		candidates = append(candidates, agentCandidate{agent: a, activeCount: activeCount})
	}

	if len(candidates) == 0 {
		return nil, nil
	}

	// Shuffle first so that equally-ranked agents (same status, tier, load)
	// are selected randomly rather than always by DB insertion order.
	// This prevents all tasks from piling onto the first registered agent
	// when tasks arrive one at a time (e.g. sequential SPL runs).
	rand.Shuffle(len(candidates), func(i, j int) {
		candidates[i], candidates[j] = candidates[j], candidates[i]
	})

	// Then pick the best by: ONLINE > tier > least loaded
	best := candidates[0]
	for _, c := range candidates[1:] {
		if isBetter(c, best) {
			best = c
		}
	}
	return best.agent, nil
}

func isBetter(a, b agentCandidate) bool {
	aOnline := 0
	if s, _ := a.agent["status"].(string); s != "ONLINE" {
		aOnline = 1
	}
	bOnline := 0
	if s, _ := b.agent["status"].(string); s != "ONLINE" {
		bOnline = 1
	}
	if aOnline != bOnline {
		return aOnline < bOnline
	}
	aTier := TierIndex(fmt.Sprint(a.agent["tier"]))
	bTier := TierIndex(fmt.Sprint(b.agent["tier"]))
	if aTier != bTier {
		return aTier < bTier
	}
	return a.activeCount < b.activeCount
}

// DeliverTask sends a task to an agent via HTTP POST.
func DeliverTask(agent map[string]interface{}, req schema.TaskRequest) (*schema.TaskResult, error) {
	host, _ := agent["host"].(string)
	port := toInt(agent["port"])
	url := fmt.Sprintf("http://%s:%d/run", host, port)

	body, _ := json.Marshal(req)
	// Add 10s grace over the task timeout; floor at 120s so zero/unset timeouts
	// don't kill Ollama requests before the model has had a chance to respond.
	timeout := time.Duration(req.TimeoutS+10) * time.Second
	if timeout < 120*time.Second {
		timeout = 120 * time.Second
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("agent returned %d: %s", resp.StatusCode, string(b))
	}
	var result schema.TaskResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

// DispatchPending matches pending tasks to available agents.
// DispatchLoop runs DispatchPending on a 2-second ticker until stopCh is closed.
func DispatchLoop(state *GridState, sseQueues *SSEManager, maxConcurrent int, stopCh <-chan struct{}) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			DispatchPending(state, sseQueues, maxConcurrent)
		}
	}
}

func DispatchPending(state *GridState, sseQueues *SSEManager, maxConcurrent int) int {
	query := fmt.Sprintf("SELECT * FROM tasks WHERE state=%s ORDER BY priority DESC, created_at LIMIT 50", state.q(1))
	rows, err := queryMaps(state.DB, query, string(schema.StatePending))
	if err != nil {
		return 0
	}

	dispatched := 0
	for _, row := range rows {
		req := taskFromRow(row)
		agent, err := PickAgent(state, req, maxConcurrent)
		if err != nil || agent == nil {
			continue
		}
		agentID, _ := agent["agent_id"].(string)
		claimed, err := state.ClaimTask(req.TaskID, agentID)
		if err != nil || !claimed {
			continue
		}
		log.Printf("dispatching task %s → agent %s", req.TaskID, agentID)

		// Deliver in background goroutine
		go deliverAndUpdate(state, agent, req, sseQueues)
		dispatched++
	}
	return dispatched
}

func deliverAndUpdate(state *GridState, agent map[string]interface{}, req schema.TaskRequest, sseQueues *SSEManager) {
	agentID, _ := agent["agent_id"].(string)

	// Pull mode: put task on SSE queue
	if sseQueues != nil {
		if q := sseQueues.Get(agentID); q != nil {
			state.MarkInFlight(req.TaskID)
			q <- req
			log.Printf("task %s queued via SSE for agent %s", req.TaskID, agentID)
			return
		}
	}

	// Push mode: HTTP POST
	state.MarkInFlight(req.TaskID)
	result, err := DeliverTask(agent, req)
	if err != nil {
		log.Printf("task %s failed on agent %s: %v", req.TaskID, agentID, err)
		state.FailTask(req.TaskID, err.Error())
		fireCallback(req.CallbackURL, req.TaskID, nil, err.Error())
		return
	}
	state.CompleteTask(req.TaskID, *result)
	operatorID := "unknown"
	if oid, ok := agent["operator_id"].(string); ok {
		operatorID = oid
	}
	state.RecordReward(operatorID, agentID, req.TaskID, result.OutputTokens, float64(result.OutputTokens)/1000.0)
	log.Printf("task %s complete  tokens=%d", req.TaskID, result.OutputTokens)
	// Notify originating hub via webhook callback (only set on forwarded tasks).
	fireCallback(req.CallbackURL, req.TaskID, result, "")
}

// fireCallback POSTs the task result to the originating hub's /cluster/result
// endpoint. It is a no-op when callbackURL is empty (non-forwarded tasks).
func fireCallback(callbackURL, taskID string, result *schema.TaskResult, errMsg string) {
	if callbackURL == "" {
		return
	}
	payload := map[string]interface{}{
		"task_id": taskID,
		"error":   errMsg,
	}
	if result != nil {
		payload["result"] = result
	}
	body, _ := json.Marshal(payload)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(callbackURL, "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("callback to %s for task %s failed: %v", callbackURL, taskID, err)
		return
	}
	resp.Body.Close()
	log.Printf("callback fired for task %s → %s", taskID, callbackURL)
}

// JobLoop runs the long-running job dispatcher every 10s.
func JobLoop(state *GridState, sseQueues *SSEManager, notifier *Notifier, maxConcurrent int, stopCh <-chan struct{}) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			DispatchPendingJobs(state, sseQueues, notifier, maxConcurrent)
		}
	}
}

// DispatchPendingJobs matches queued jobs to available agents by submitting
// them as normal tasks.
func DispatchPendingJobs(state *GridState, sseQueues *SSEManager, notifier *Notifier, maxConcurrent int) int {
	query := fmt.Sprintf("SELECT * FROM jobs WHERE state=%s ORDER BY created_at LIMIT 10", state.q(1))
	rows, err := queryMaps(state.DB, query, string(schema.JobQueued))
	if err != nil {
		return 0
	}

	dispatched := 0
	for _, row := range rows {
		jobID := fmt.Sprint(row["job_id"])
		req := taskFromJobRow(row)
		
		agent, err := PickAgent(state, req, maxConcurrent)
		if err != nil || agent == nil {
			continue
		}
		
		agentID, _ := agent["agent_id"].(string)
		state.UpdateJobState(jobID, schema.JobInFlight, nil)
		log.Printf("dispatching job %s → agent %s", jobID, agentID)

		// Parse notification config
		var notify schema.JobNotify
		json.Unmarshal([]byte(strVal(row["notify"])), &notify)

		// Submit as a normal task, but with a custom callback/completion handler
		// that updates the job state instead of just the task state.
		go func(jid string, r schema.TaskRequest, a map[string]interface{}, n schema.JobNotify) {
			result, err := DeliverTask(a, r)
			var finalState schema.JobState
			var finalRes *schema.TaskResult

			if err != nil {
				finalState = schema.JobFailed
				finalRes = &schema.TaskResult{Error: err.Error()}
			} else {
				finalState = schema.JobComplete
				finalRes = result
			}

			state.UpdateJobState(jid, finalState, finalRes)

			if notifier != nil {
				notifier.Notify(schema.JobStatusResponse{
					JobID:     jid,
					State:     finalState,
					Model:     r.Model,
					Result:    finalRes,
					UpdatedAt: time.Now().UTC(),
				}, n)
			}
		}(jobID, req, agent, notify)
		
		dispatched++
	}
	return dispatched
}

func taskFromJobRow(row map[string]interface{}) schema.TaskRequest {
	return schema.TaskRequest{
		TaskID:      "job-" + fmt.Sprint(row["job_id"]),
		Model:       fmt.Sprint(row["model"]),
		Prompt:      fmt.Sprint(row["prompt"]),
		System:      fmt.Sprint(row["system"]),
		MaxTokens:   toInt(row["max_tokens"]),
		MinTier:     schema.ComputeTier(fmt.Sprint(row["min_tier"])),
		TimeoutS:    3600, // Long timeout for jobs
		Priority:    1,
	}
}

func taskFromRow(row map[string]interface{}) schema.TaskRequest {
	return schema.TaskRequest{
		TaskID:      fmt.Sprint(row["task_id"]),
		Model:       fmt.Sprint(row["model"]),
		Prompt:      fmt.Sprint(row["prompt"]),
		System:      fmt.Sprint(row["system"]),
		MaxTokens:   toInt(row["max_tokens"]),
		Temperature: toFloat(row["temperature"]),
		MinTier:     schema.ComputeTier(fmt.Sprint(row["min_tier"])),
		MinVramGB:   toFloat(row["min_vram_gb"]),
		TimeoutS:    toInt(row["timeout_s"]),
		Priority:    toInt(row["priority"]),
		CallbackURL: fmt.Sprint(row["callback_url"]),
	}
}

func toInt(v interface{}) int {
	switch n := v.(type) {
	case int64:
		return int(n)
	case float64:
		return int(n)
	case int:
		return n
	default:
		return 0
	}
}

func toFloat(v interface{}) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int64:
		return float64(n)
	default:
		return 0
	}
}
