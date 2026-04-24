package hub

// DBOS backend for Momagrid Hub.
//
// To activate:  mg hub up --backend dbos --postgres "postgres://user:pass@host/db"
//
// Prerequisites (already handled by go.mod):
//   go get github.com/dbos-inc/dbos-transact-golang@latest
//
// Design:
//   - Task lifecycle is a durable DBOS workflow: pick agent → deliver → reward.
//   - Job lifecycle is a durable DBOS workflow: optional sleep → task dispatch → notify.
//   - Agent eviction runs as a scheduled DBOS workflow (every minute) instead of a goroutine.
//   - ClusterMonitor goroutine remains in App (unchanged) — peer forwarding is backend-agnostic.
//   - Pull-mode (SSE) agents are excluded in v1; push-only agents are selected.
//   - No dual-write: hub.tasks and hub.jobs tables are NOT written under this backend.
//     GET /tasks/{id} and GET /jobs/{id} read from DBOS workflow state instead.

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/digital-duck/momagrid/internal/schema"
)

// dbosBackend is the singleton used by package-level workflow functions.
// Package-level functions are required for stable workflow naming during DBOS replay.
var dbosBackend *DBOSBackend

// DBOSBackend implements DispatchBackend using DBOS durable workflows.
type DBOSBackend struct {
	dbosCtx       dbos.DBOSContext
	state         *GridState  // agent queries, reward recording (Hub DB)
	sseQueues     *SSEManager // pull-mode detection
	notifier      *Notifier
	maxConcurrent int
	maxRetries    int
	taskQueueName string
	jobQueueName  string
}

func NewDBOSBackend(postgresDSN string, state *GridState, sseQueues *SSEManager, notifier *Notifier, maxConcurrent, maxRetries int) (*DBOSBackend, error) {
	if postgresDSN == "" {
		return nil, fmt.Errorf("--postgres DSN is required for --backend dbos")
	}

	ctx, err := dbos.NewDBOSContext(context.Background(), dbos.Config{
		DatabaseURL: postgresDSN,
	})
	if err != nil {
		return nil, fmt.Errorf("dbos context: %w", err)
	}

	const taskQueueName = "task-dispatch"
	const jobQueueName = "job-dispatch"

	b := &DBOSBackend{
		dbosCtx:       ctx,
		state:         state,
		sseQueues:     sseQueues,
		notifier:      notifier,
		maxConcurrent: maxConcurrent,
		maxRetries:    maxRetries,
		taskQueueName: taskQueueName,
		jobQueueName:  jobQueueName,
	}

	// Set global before registering workflows — they reference dbosBackend.
	dbosBackend = b

	// Register workflow functions. Must be package-level functions for stable replay names.
	dbos.RegisterWorkflow(ctx, dbosTaskWorkflow)
	dbos.RegisterWorkflow(ctx, dbosJobWorkflow)
	// Agent eviction on a 1-minute cron (standard 5-field syntax).
	// Replaces the AgentMonitor goroutine; single invocation across replicas.
	// NOTE: If sub-minute precision is needed, check whether the installed DBOS SDK
	// supports 6-field cron or "@every Xs" notation.
	dbos.RegisterWorkflow(ctx, dbosAgentEvictionWorkflow, dbos.WithSchedule("* * * * *"))

	// Register queues. WorkerConcurrency caps parallel dispatches per Hub process.
	// Per-agent limits are enforced inside pickPushAgent via maxConcurrent.
	dbos.NewWorkflowQueue(ctx, taskQueueName, dbos.WithWorkerConcurrency(50))
	dbos.NewWorkflowQueue(ctx, jobQueueName, dbos.WithWorkerConcurrency(10))

	return b, nil
}

func (b *DBOSBackend) Start() error {
	return b.dbosCtx.Launch()
}

func (b *DBOSBackend) Stop() error {
	b.dbosCtx.Shutdown(10 * time.Second)
	return nil
}

// ── Task lifecycle ────────────────────────────────────────────────────────────

func (b *DBOSBackend) SubmitTask(req schema.TaskRequest) error {
	_, err := dbos.RunWorkflow(b.dbosCtx, dbosTaskWorkflow, req,
		dbos.WithWorkflowID(req.TaskID),
		dbos.WithQueue(b.taskQueueName),
	)
	return err
}

func (b *DBOSBackend) GetTaskStatus(taskID string) (*schema.TaskStatusResponse, error) {
	handle, err := dbos.RetrieveWorkflow[schema.TaskResult](b.dbosCtx, taskID)
	if err != nil {
		// DBOS returns an error when the workflow ID is not found.
		return nil, nil
	}

	status, err := handle.GetStatus()
	if err != nil {
		return nil, nil
	}

	resp := &schema.TaskStatusResponse{
		TaskID: taskID,
		State:  dbosToTaskState(status.Status),
	}

	switch status.Status {
	case dbos.WorkflowStatusSuccess:
		result, rerr := handle.GetResult()
		if rerr == nil {
			r := result
			resp.Result = &r
		}
	case dbos.WorkflowStatusError, dbos.WorkflowStatusCancelled, dbos.WorkflowStatusMaxRecoveryAttemptsExceeded:
		errMsg := ""
		if status.Error != nil {
			errMsg = status.Error.Error()
		}
		resp.Result = &schema.TaskResult{
			TaskID: taskID,
			State:  schema.StateFailed,
			Error:  errMsg,
		}
	}

	return resp, nil
}

func (b *DBOSBackend) ListTasks(limit int) ([]map[string]interface{}, error) {
	statuses, err := dbos.ListWorkflows(b.dbosCtx,
		dbos.WithName("dbosTaskWorkflow"),
		dbos.WithLimit(limit),
	)
	if err != nil {
		log.Printf("dbos: list task workflows: %v", err)
		return []map[string]interface{}{}, nil
	}
	rows := make([]map[string]interface{}, 0, len(statuses))
	for _, s := range statuses {
		rows = append(rows, map[string]interface{}{
			"task_id":    s.ID,
			"state":      string(dbosToTaskState(s.Status)),
			"created_at": s.CreatedAt.Format(time.RFC3339),
			"updated_at": s.UpdatedAt.Format(time.RFC3339),
		})
	}
	return rows, nil
}

func (b *DBOSBackend) CountPendingTasks() (int, error) {
	// DBOS manages queue depth internally. Return 0 so handleSubmitTask never
	// rejects with 503 — concurrency is enforced by the DBOS queue.
	// TODO: Query DBOS system tables for actual enqueued count if needed.
	return 0, nil
}

// ── Job lifecycle ─────────────────────────────────────────────────────────────

func (b *DBOSBackend) SubmitJob(req schema.JobRequest) error {
	_, err := dbos.RunWorkflow(b.dbosCtx, dbosJobWorkflow, req,
		dbos.WithWorkflowID(req.JobID),
		dbos.WithQueue(b.jobQueueName),
	)
	return err
}

func (b *DBOSBackend) GetJobStatus(jobID string) (*schema.JobStatusResponse, error) {
	handle, err := dbos.RetrieveWorkflow[schema.TaskResult](b.dbosCtx, jobID)
	if err != nil {
		return nil, nil
	}

	status, err := handle.GetStatus()
	if err != nil {
		return nil, nil
	}

	resp := &schema.JobStatusResponse{
		JobID:     jobID,
		State:     dbosToJobState(status.Status),
		CreatedAt: status.CreatedAt,
		UpdatedAt: status.UpdatedAt,
	}

	switch status.Status {
	case dbos.WorkflowStatusSuccess:
		result, rerr := handle.GetResult()
		if rerr == nil {
			r := result
			resp.Result = &r
		}
	case dbos.WorkflowStatusError, dbos.WorkflowStatusCancelled, dbos.WorkflowStatusMaxRecoveryAttemptsExceeded:
		errMsg := ""
		if status.Error != nil {
			errMsg = status.Error.Error()
		}
		resp.Result = &schema.TaskResult{
			State: schema.StateFailed,
			Error: errMsg,
		}
	}

	return resp, nil
}

func (b *DBOSBackend) ListJobs(limit int) ([]map[string]interface{}, error) {
	statuses, err := dbos.ListWorkflows(b.dbosCtx,
		dbos.WithName("dbosJobWorkflow"),
		dbos.WithLimit(limit),
	)
	if err != nil {
		log.Printf("dbos: list job workflows: %v", err)
		return []map[string]interface{}{}, nil
	}
	rows := make([]map[string]interface{}, 0, len(statuses))
	for _, s := range statuses {
		rows = append(rows, map[string]interface{}{
			"job_id":     s.ID,
			"state":      string(dbosToJobState(s.Status)),
			"created_at": s.CreatedAt.Format(time.RFC3339),
			"updated_at": s.UpdatedAt.Format(time.RFC3339),
		})
	}
	return rows, nil
}

// ── Package-level workflow functions ─────────────────────────────────────────
// Must be package-level (not methods) so DBOS can identify them by a stable
// function name for durable replay across Hub restarts.

// dbosTaskWorkflow is the durable task lifecycle:
//
//	pick push-mode agent → deliver via HTTP → record reward
//
// All non-deterministic operations are wrapped in RunAsStep so DBOS can
// replay completed steps from the system DB without re-executing them.
func dbosTaskWorkflow(ctx dbos.DBOSContext, req schema.TaskRequest) (schema.TaskResult, error) {
	b := dbosBackend

	// Step 1: Pick an eligible push-mode agent.
	// Retried with backoff if no agent is currently available.
	agent, err := dbos.RunAsStep(ctx, func(_ context.Context) (map[string]interface{}, error) {
		a, pickErr := pickPushAgent(b.state, req, b.maxConcurrent)
		if pickErr != nil {
			return nil, pickErr
		}
		if a == nil {
			return nil, fmt.Errorf("no eligible push-mode agent for task %s (will retry)", req.TaskID)
		}
		return a, nil
	}, dbos.WithStepName("pick-agent"),
		dbos.WithStepMaxRetries(5),
		dbos.WithBaseInterval(2*time.Second))
	if err != nil {
		log.Printf("dbos task %s: pick-agent exhausted retries: %v", req.TaskID, err)
		return schema.TaskResult{State: schema.StateFailed, Error: err.Error()}, err
	}

	// Step 2: Deliver task to agent via HTTP POST.
	// On transient network errors DBOS retries automatically up to maxRetries.
	result, err := dbos.RunAsStep(ctx, func(_ context.Context) (schema.TaskResult, error) {
		res, deliverErr := DeliverTask(agent, req)
		if deliverErr != nil {
			return schema.TaskResult{}, deliverErr
		}
		return *res, nil
	}, dbos.WithStepName("deliver"),
		dbos.WithStepMaxRetries(b.maxRetries),
		dbos.WithBaseInterval(2*time.Second))
	if err != nil {
		log.Printf("dbos task %s: deliver failed: %v", req.TaskID, err)
		return schema.TaskResult{State: schema.StateFailed, Error: err.Error()}, err
	}

	// Step 3: Record reward. Exactly-once: DBOS will not re-execute this step
	// on replay even if the Hub crashes immediately after delivery.
	_, _ = dbos.RunAsStep(ctx, func(_ context.Context) (struct{}, error) {
		agentID, _ := agent["agent_id"].(string)
		operatorID := "unknown"
		if oid, ok := agent["operator_id"].(string); ok {
			operatorID = oid
		}
		rewardErr := b.state.RecordReward(operatorID, agentID, req.TaskID,
			result.OutputTokens, float64(result.OutputTokens)/1000.0)
		return struct{}{}, rewardErr
	}, dbos.WithStepName("record-reward"))

	result.State = schema.StateComplete
	log.Printf("dbos task %s complete  tokens=%d", req.TaskID, result.OutputTokens)
	return result, nil
}

// dbosJobWorkflow is the durable job lifecycle:
//
//	optional durable sleep until deadline → dispatch as child task → notify
//
// The Sleep survives Hub restarts — if the Hub crashes mid-sleep, DBOS resumes
// the remaining wait on the next start rather than re-sleeping from scratch.
func dbosJobWorkflow(ctx dbos.DBOSContext, req schema.JobRequest) (schema.TaskResult, error) {
	b := dbosBackend

	// Durable sleep until the job's deadline (if set).
	if !req.Deadline.IsZero() {
		if wait := time.Until(req.Deadline); wait > 0 {
			log.Printf("dbos job %s: sleeping %.0fs until deadline", req.JobID, wait.Seconds())
			dbos.Sleep(ctx, wait) //nolint:errcheck // Sleep error is non-fatal
		}
	}

	// Dispatch as a child task workflow.
	// WithWorkflowID ensures idempotency: if this job workflow restarts,
	// it won't launch a second task workflow for the same job.
	taskReq := jobReqToTaskReq(req)
	childHandle, err := dbos.RunWorkflow(ctx, dbosTaskWorkflow, taskReq,
		dbos.WithWorkflowID(req.JobID+":task"))
	if err != nil {
		return schema.TaskResult{}, err
	}

	// Block until the child task workflow completes.
	result, taskErr := childHandle.GetResult()

	// Notify via webhook/email regardless of success or failure.
	// Wrapped in a step so the notification is exactly-once even on replay.
	notifyState := schema.JobComplete
	if taskErr != nil {
		notifyState = schema.JobFailed
	}
	notifyResult := result
	_, _ = dbos.RunAsStep(ctx, func(_ context.Context) (struct{}, error) {
		b.notifier.Notify(schema.JobStatusResponse{
			JobID:     req.JobID,
			State:     notifyState,
			Model:     req.Model,
			Result:    &notifyResult,
			UpdatedAt: time.Now().UTC(),
		}, req.Notify)
		return struct{}{}, nil
	}, dbos.WithStepName("notify"))

	return result, taskErr
}

// dbosAgentEvictionWorkflow replaces the AgentMonitor goroutine under the DBOS backend.
// DBOS schedules it every minute via the registered cron expression.
// When running multiple Hub replicas, DBOS guarantees single-invocation per tick.
func dbosAgentEvictionWorkflow(ctx dbos.DBOSContext, _ struct{}) (struct{}, error) {
	b := dbosBackend
	_, err := dbos.RunAsStep(ctx, func(_ context.Context) (struct{}, error) {
		evicted, evictErr := b.state.EvictStaleAgents()
		if evictErr != nil {
			return struct{}{}, evictErr
		}
		if evicted > 0 {
			log.Printf("dbos eviction: removed %d stale agent(s)", evicted)
		}
		return struct{}{}, nil
	}, dbos.WithStepName("evict-stale-agents"))
	return struct{}{}, err
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// pickPushAgent selects an agent, excluding pull-mode agents.
// Pull-mode (SSE) agents are not supported in DBOS v1 because the task workflow
// delivers via HTTP POST and cannot block on an SSE channel.
func pickPushAgent(state *GridState, req schema.TaskRequest, maxConcurrent int) (map[string]interface{}, error) {
	agent, err := PickAgent(state, req, maxConcurrent)
	if err != nil {
		return nil, err
	}
	if agent == nil {
		return nil, nil
	}
	if toInt(agent["pull_mode"]) == 1 {
		// Pull-mode agent selected — skip it. Workflow will retry and may
		// pick a push-mode agent on the next attempt.
		log.Printf("dbos: skipping pull-mode agent %s for task %s", agent["agent_id"], req.TaskID)
		return nil, nil
	}
	return agent, nil
}

// jobReqToTaskReq converts a JobRequest to a TaskRequest for the child task workflow.
func jobReqToTaskReq(req schema.JobRequest) schema.TaskRequest {
	return schema.TaskRequest{
		TaskID:    "job-" + req.JobID,
		Model:     req.Model,
		Prompt:    req.Prompt,
		System:    req.System,
		MaxTokens: req.MaxTokens,
		MinTier:   req.MinTier,
		TimeoutS:  3600,
		Priority:  1,
	}
}

// dbosToTaskState maps DBOS WorkflowStatusType to Momagrid TaskState.
func dbosToTaskState(s dbos.WorkflowStatusType) schema.TaskState {
	switch s {
	case dbos.WorkflowStatusSuccess:
		return schema.StateComplete
	case dbos.WorkflowStatusError, dbos.WorkflowStatusCancelled, dbos.WorkflowStatusMaxRecoveryAttemptsExceeded:
		return schema.StateFailed
	case dbos.WorkflowStatusPending:
		return schema.StateInFlight
	default: // ENQUEUED, DELAYED
		return schema.StatePending
	}
}

// dbosToJobState maps DBOS WorkflowStatusType to Momagrid JobState.
func dbosToJobState(s dbos.WorkflowStatusType) schema.JobState {
	switch s {
	case dbos.WorkflowStatusSuccess:
		return schema.JobComplete
	case dbos.WorkflowStatusError, dbos.WorkflowStatusCancelled, dbos.WorkflowStatusMaxRecoveryAttemptsExceeded:
		return schema.JobFailed
	case dbos.WorkflowStatusPending:
		return schema.JobInFlight
	default: // ENQUEUED, DELAYED
		return schema.JobQueued
	}
}
