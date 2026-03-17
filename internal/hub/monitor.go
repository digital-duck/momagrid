package hub

import (
	"fmt"
	"log"
	"time"

	"github.com/digital-duck/momagrid/internal/schema"
)

// AgentMonitor periodically evicts stale agents.
func AgentMonitor(state *GridState, stop <-chan struct{}) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			evicted, err := state.EvictStaleAgents()
			if err != nil {
				log.Printf("agent monitor error: %v", err)
			} else if evicted > 0 {
				log.Printf("evicted %d stale agent(s)", evicted)
			}
		}
	}
}

// ClusterMonitor periodically pushes capabilities and forwards pending tasks
// that have no eligible local agent to peer hubs (spec §6.3, §10).
func ClusterMonitor(state *GridState, cluster *ClusterManager, stop <-chan struct{}) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if err := cluster.PushCapabilities(); err != nil {
				log.Printf("cluster monitor: push capabilities: %v", err)
			}
			forwardUnroutableTasks(state, cluster)
		}
	}
}

// forwardUnroutableTasks fetches up to 20 PENDING tasks for which no local
// agent is eligible and forwards them to a peer hub.
func forwardUnroutableTasks(state *GridState, cluster *ClusterManager) {
	peers, err := state.ListPeers()
	if err != nil || len(peers) == 0 {
		return
	}

	query := fmt.Sprintf("SELECT * FROM tasks WHERE state=%s ORDER BY priority DESC, created_at LIMIT 20", state.q(1))
	rows, err := queryMaps(state.DB, query, string(schema.StatePending))
	if err != nil || len(rows) == 0 {
		return
	}

	for _, row := range rows {
		req := taskFromRow(row)
		// Skip if a local agent can handle it
		agent, err := PickAgent(state, req, 0)
		if err != nil || agent != nil {
			continue
		}
		// No local agent — forward to a peer
		go func(r schema.TaskRequest) {
			result, err := cluster.ForwardTask(r)
			if err != nil {
				log.Printf("cluster forward task %s: %v", r.TaskID, err)
				return
			}
			if result != nil {
				state.CompleteTask(r.TaskID, *result)
				log.Printf("cluster forward task %s complete via peer", r.TaskID)
			}
		}(req)
	}
}


