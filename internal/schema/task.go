package schema

// TaskRequest represents a task submission.
//
// CallbackURL is set by the originating hub when forwarding a task to a peer.
// When the peer hub completes the task it POSTs the result to this URL
// (POST /cluster/result) instead of waiting to be polled.
// Polling is kept as a fallback in case the callback is not delivered.
type TaskRequest struct {
	TaskID      string      `json:"task_id"`
	Model       string      `json:"model"`
	Prompt      string      `json:"prompt"`
	System      string      `json:"system"`
	MaxTokens   int         `json:"max_tokens"`
	Temperature float64     `json:"temperature"`
	MinTier     ComputeTier `json:"min_tier"`
	MinVramGB   float64     `json:"min_vram_gb"`
	TimeoutS    int         `json:"timeout_s"`
	Priority    int         `json:"priority"`
	// CallbackURL is the originating hub's result endpoint for forwarded tasks.
	CallbackURL string `json:"callback_url,omitempty"`
}

// TaskRequestDefaults fills zero-value fields with sensible defaults.
func (r *TaskRequest) ApplyDefaults() {
	if r.MaxTokens == 0 {
		r.MaxTokens = 1024
	}
	if r.Temperature == 0 {
		r.Temperature = 0.7
	}
	if r.MinTier == "" {
		r.MinTier = TierBronze
	}
	if r.TimeoutS == 0 {
		r.TimeoutS = 300
	}
	if r.Priority == 0 {
		r.Priority = 1
	}
}

// TaskResult represents the outcome of a completed task.
type TaskResult struct {
	TaskID       string    `json:"task_id"`
	State        TaskState `json:"state"`
	Content      string    `json:"content"`
	Model        string    `json:"model"`
	InputTokens  int       `json:"input_tokens"`
	OutputTokens int       `json:"output_tokens"`
	LatencyMs    float64   `json:"latency_ms"`
	AgentID      string    `json:"agent_id"`
	AgentName    string    `json:"agent_name"`
	AgentHost    string    `json:"agent_host"`
	CompletedAt  string    `json:"completed_at"`
	Error        string    `json:"error"`
}

// TaskStatusResponse wraps task state for the GET /tasks/:id endpoint.
type TaskStatusResponse struct {
	TaskID string      `json:"task_id"`
	State  TaskState   `json:"state"`
	Result *TaskResult `json:"result,omitempty"`
}
