package hub

import (
	"path/filepath"
	"testing"

	"github.com/digital-duck/momagrid/internal/schema"
)

func newTestState(t *testing.T) *GridState {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "hub.sqlite3")
	db, err := InitDB(dbPath)
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return &GridState{DB: db, HubID: "test-hub", OperatorID: "test-op", Driver: "sqlite"}
}

func joinTestAgent(t *testing.T, s *GridState, agentID string, gpus []schema.GPUInfo, score float64) {
	t.Helper()
	req := schema.JoinRequest{OperatorID: "test-op", AgentID: agentID, Host: "127.0.0.1", Port: 9010, GPUs: gpus}
	if _, err := s.RegisterAgent(req, schema.TierGold, false, false, score); err != nil {
		t.Fatalf("RegisterAgent(%s): %v", agentID, err)
	}
}

// TestRecomputeGPUScoresAveragesByModel confirms two agents with the same GPU
// model are averaged together, and an agent with no completed tasks
// (tasks_completed=0) is excluded — its dispatch_score is still just an
// unmeasured join-time seed, not real data.
func TestRecomputeGPUScoresAveragesByModel(t *testing.T) {
	s := newTestState(t)
	gtx := []schema.GPUInfo{{Index: 0, Model: "NVIDIA GeForce GTX 1080 Ti", VramGB: 11}}

	joinTestAgent(t, s, "agent-a", gtx, 100)
	joinTestAgent(t, s, "agent-b", gtx, 140)
	joinTestAgent(t, s, "agent-c-unmeasured", gtx, 999) // no completed tasks — must be excluded

	s.DB.Exec("UPDATE agents SET tasks_completed=5 WHERE agent_id IN ('agent-a','agent-b')")

	n, err := s.RecomputeGPUScores()
	if err != nil {
		t.Fatalf("RecomputeGPUScores: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 distinct GPU model, got %d", n)
	}

	avg, ok := s.GPUScoreFor("NVIDIA GeForce GTX 1080 Ti")
	if !ok {
		t.Fatal("expected a score for the GTX 1080 Ti model")
	}
	if avg != 120 {
		t.Errorf("expected avg_score=120 (mean of 100,140, excluding unmeasured 999), got %v", avg)
	}
}

// TestGPUScoreForUnknownModel confirms a model the grid has never seen
// returns ok=false, so callers correctly fall back to the tier default.
func TestGPUScoreForUnknownModel(t *testing.T) {
	s := newTestState(t)
	if _, ok := s.GPUScoreFor("Some GPU Nobody Has Joined With"); ok {
		t.Error("expected ok=false for a GPU model with no history")
	}
	if _, ok := s.GPUScoreFor(""); ok {
		t.Error("expected ok=false for an empty model string (unified-memory agents)")
	}
}

// TestRegisterAgentPreservesLearnedScoreOnRejoin confirms a rejoin never
// resets dispatch_score once the hub has measured real task throughput —
// this is the core guarantee that makes GPU score history meaningful (an
// operator restarting their agent shouldn't wipe out its learned standing).
func TestRegisterAgentPreservesLearnedScoreOnRejoin(t *testing.T) {
	s := newTestState(t)
	joinTestAgent(t, s, "agent-a", nil, 40)
	if err := s.UpdateDispatchScore("agent-a", 1000, 20); err != nil {
		t.Fatalf("UpdateDispatchScore: %v", err)
	}

	var learned float64
	s.DB.QueryRow("SELECT dispatch_score FROM agents WHERE agent_id='agent-a'").Scan(&learned)
	if learned == 40 {
		t.Fatal("expected dispatch_score to have moved away from the join-time seed after a measured update")
	}

	// Rejoin with a different (lower) seed — should NOT reset the learned score.
	joinTestAgent(t, s, "agent-a", nil, 1)

	var afterRejoin float64
	s.DB.QueryRow("SELECT dispatch_score FROM agents WHERE agent_id='agent-a'").Scan(&afterRejoin)
	if afterRejoin != learned {
		t.Errorf("rejoin reset dispatch_score: had %v after learning, got %v after rejoin", learned, afterRejoin)
	}
}
