package schema

import "time"

// JobState represents the lifecycle state of a long-running job.
type JobState string

const (
	JobQueued    JobState = "QUEUED"
	JobDispatched JobState = "DISPATCHED"
	JobInFlight   JobState = "IN_FLIGHT"
	JobComplete   JobState = "COMPLETE"
	JobFailed     JobState = "FAILED"
	JobExpired    JobState = "EXPIRED"
	JobCancelled  JobState = "CANCELLED"
)

// JobRequest represents a long-running job submission.
type JobRequest struct {
	JobID      string      `json:"job_id"`
	Model      string      `json:"model"`
	Prompt     string      `json:"prompt"`
	System     string      `json:"system"`
	MaxTokens  int         `json:"max_tokens"`
	MinTier    ComputeTier `json:"min_tier"`
	Deadline   time.Time   `json:"deadline,omitempty"`
	Notify     JobNotify   `json:"notify,omitempty"`
	MaxRetries int         `json:"max_retries"`
}

type JobNotify struct {
	WebhookURL string `json:"webhook_url,omitempty"`
	Email      string `json:"email,omitempty"`
}

// JobStatusResponse wraps job state.
type JobStatusResponse struct {
	JobID      string      `json:"job_id"`
	Model      string      `json:"model"`
	State      JobState    `json:"state"`
	Position   int         `json:"position,omitempty"`
	Progress   float64     `json:"progress,omitempty"` // 0.0 to 1.0 or tokens generated
	Result     *TaskResult `json:"result,omitempty"`
	CreatedAt  time.Time   `json:"created_at"`
	UpdatedAt  time.Time   `json:"updated_at"`
	ElapsedS   float64     `json:"elapsed_s"`
}
