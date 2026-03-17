package hub

import (
	"sync"

	"github.com/digital-duck/momagrid/internal/schema"
)

// SSEManager manages per-agent SSE task queues.
type SSEManager struct {
	mu     sync.RWMutex
	queues map[string]chan schema.TaskRequest
}

// NewSSEManager creates a new SSE manager.
func NewSSEManager() *SSEManager {
	return &SSEManager{queues: make(map[string]chan schema.TaskRequest)}
}

// Register creates a queue for an agent and returns it.
func (m *SSEManager) Register(agentID string) chan schema.TaskRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	ch := make(chan schema.TaskRequest, 10)
	m.queues[agentID] = ch
	return ch
}

// Unregister removes an agent's queue.
func (m *SSEManager) Unregister(agentID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ch, ok := m.queues[agentID]; ok {
		close(ch)
		delete(m.queues, agentID)
	}
}

// Get returns the queue for an agent, or nil if not registered.
func (m *SSEManager) Get(agentID string) chan schema.TaskRequest {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.queues[agentID]
}
