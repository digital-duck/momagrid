package hub

import "github.com/digital-duck/momagrid/internal/schema"

// DispatchBackend abstracts task and job lifecycle management.
// Implementations:
//   - NativeBackend  — default, wraps existing dispatcher/monitor goroutines
//   - DBOSBackend    — opt-in via --backend dbos, durable DBOS workflows
//
// All other Hub concerns (agent handshake, rate limiting, SSE, cluster peering)
// remain in App and are shared by both backends.
type DispatchBackend interface {
	// Start launches background goroutines or the DBOS runtime.
	// Called once after NewApp, before http.ListenAndServe.
	Start() error

	// Stop gracefully shuts down backend-owned resources.
	// App.Stop closes stopCh (for goroutines) and the DB connection separately.
	Stop() error

	// Task lifecycle — sole source of truth for task state under each backend.
	SubmitTask(req schema.TaskRequest) error
	GetTaskStatus(taskID string) (*schema.TaskStatusResponse, error)
	ListTasks(limit int) ([]map[string]interface{}, error)
	CountPendingTasks() (int, error)

	// Job lifecycle — sole source of truth for job state under each backend.
	SubmitJob(req schema.JobRequest) error
	GetJobStatus(jobID string) (*schema.JobStatusResponse, error)
	ListJobs(limit int) ([]map[string]interface{}, error)
}
