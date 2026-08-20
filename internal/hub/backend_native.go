package hub

import (
	"encoding/json"

	"github.com/digital-duck/momagrid/internal/schema"
)

// NativeBackend is the default DispatchBackend. It delegates to the existing
// GridState, DispatchLoop, JobLoop, and AgentMonitor — zero logic changes.
type NativeBackend struct {
	state         *GridState
	sseQueues     *SSEManager
	notifier      *Notifier
	maxConcurrent int
	stopCh        chan struct{}
}

func NewNativeBackend(state *GridState, sseQueues *SSEManager, notifier *Notifier, maxConcurrent int, stopCh chan struct{}) *NativeBackend {
	return &NativeBackend{
		state:         state,
		sseQueues:     sseQueues,
		notifier:      notifier,
		maxConcurrent: maxConcurrent,
		stopCh:        stopCh,
	}
}

func (b *NativeBackend) Start() error {
	go AgentMonitor(b.state, b.stopCh)
	go GPUScoreMonitor(b.state, b.stopCh)
	go DispatchLoop(b.state, b.sseQueues, b.maxConcurrent, b.stopCh)
	go JobLoop(b.state, b.sseQueues, b.notifier, b.maxConcurrent, b.stopCh)
	return nil
}

func (b *NativeBackend) Stop() error {
	// Goroutines exit when App.Stop closes stopCh.
	return nil
}

// ── Task lifecycle ────────────────────────────────────────────────────────────

func (b *NativeBackend) SubmitTask(req schema.TaskRequest) error {
	return b.state.SubmitTask(req)
}

func (b *NativeBackend) GetTaskStatus(taskID string) (*schema.TaskStatusResponse, error) {
	row, err := b.state.GetTask(taskID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, nil
	}
	st := strVal(row["state"])
	resp := &schema.TaskStatusResponse{TaskID: taskID, State: schema.TaskState(st)}
	if st == string(schema.StateComplete) || st == string(schema.StateFailed) {
		resp.Result = &schema.TaskResult{
			TaskID:       taskID,
			State:        schema.TaskState(st),
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
	return resp, nil
}

func (b *NativeBackend) ListTasks(limit int) ([]map[string]interface{}, error) {
	return b.state.ListTasks(limit)
}

func (b *NativeBackend) CountPendingTasks() (int, error) {
	return b.state.CountPendingTasks()
}

// ── Job lifecycle ─────────────────────────────────────────────────────────────

func (b *NativeBackend) SubmitJob(req schema.JobRequest) error {
	return b.state.SubmitJob(req)
}

func (b *NativeBackend) GetJobStatus(jobID string) (*schema.JobStatusResponse, error) {
	row, err := b.state.GetJob(jobID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, nil
	}
	resp := &schema.JobStatusResponse{
		JobID:     jobID,
		State:     schema.JobState(strVal(row["state"])),
		Model:     strVal(row["model"]),
		CreatedAt: parseTime(row["created_at"]),
		UpdatedAt: parseTime(row["updated_at"]),
	}
	if resStr := strVal(row["result"]); resStr != "" {
		var res schema.TaskResult
		json.Unmarshal([]byte(resStr), &res)
		resp.Result = &res
	}
	return resp, nil
}

func (b *NativeBackend) ListJobs(limit int) ([]map[string]interface{}, error) {
	return b.state.ListJobs(limit)
}
