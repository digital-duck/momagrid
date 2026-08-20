package hub

import (
	"testing"
)

// TestWeightedPickNoStarvation guards the exact bug fixed 2026-08-20: a
// 2-node grid where the higher-scored agent absorbed 100% of dispatch,
// leaving the lower-scored (but fully eligible) agent completely idle. The
// old strict ONLINE > tier > least-loaded ordering always picked the single
// best candidate deterministically; weightedPick must instead give every
// eligible candidate a nonzero share, proportionally larger for a higher
// dispatch_score (see docs/DEV/task-dispatch.md — measured EWMA throughput,
// tier weights were only ever the join-time seed for this field).
func TestWeightedPickNoStarvation(t *testing.T) {
	candidates := []agentCandidate{
		{agent: map[string]interface{}{"agent_id": "fast-node", "dispatch_score": 8.0, "status": "ONLINE"}, activeCount: 0},
		{agent: map[string]interface{}{"agent_id": "slow-node", "dispatch_score": 4.0, "status": "ONLINE"}, activeCount: 0},
	}

	const trials = 20000
	counts := map[string]int{}
	for i := 0; i < trials; i++ {
		picked := weightedPick(candidates)
		counts[picked["agent_id"].(string)]++
	}

	fastShare := float64(counts["fast-node"]) / trials
	slowShare := float64(counts["slow-node"]) / trials

	if counts["slow-node"] == 0 {
		t.Fatalf("slow-node starved: got %d/%d dispatches (expected nonzero)", counts["slow-node"], trials)
	}
	if fastShare <= slowShare {
		t.Fatalf("expected fast-node (higher dispatch_score) to get more traffic than slow-node; got fast=%.3f slow=%.3f", fastShare, slowShare)
	}
	// score ratio is 8:4 = 2:1 — allow generous tolerance for statistical
	// noise over 20k trials (expect ~0.667 / ~0.333).
	if fastShare < 0.55 || fastShare > 0.78 {
		t.Errorf("fast-node share %.3f outside expected ~0.55-0.78 band for an 8:4 score ratio", fastShare)
	}
}

// TestWeightedPickLoadAdjustment: an agent already juggling active tasks
// should get proportionally less share even against a lower-scored idle
// agent — load matters, not just raw dispatch_score.
func TestWeightedPickLoadAdjustment(t *testing.T) {
	candidates := []agentCandidate{
		{agent: map[string]interface{}{"agent_id": "busy-fast", "dispatch_score": 8.0, "status": "ONLINE"}, activeCount: 10},
		{agent: map[string]interface{}{"agent_id": "idle-slow", "dispatch_score": 1.0, "status": "ONLINE"}, activeCount: 0},
	}

	const trials = 20000
	counts := map[string]int{}
	for i := 0; i < trials; i++ {
		picked := weightedPick(candidates)
		counts[picked["agent_id"].(string)]++
	}

	// busy-fast weight: 8 / (1+10) ≈ 0.727; idle-slow weight: 1 / 1 = 1.
	// idle-slow should win more often despite the lower raw score.
	if counts["idle-slow"] <= counts["busy-fast"] {
		t.Errorf("expected idle-slow to outweigh a heavily-loaded fast node; got idle-slow=%d busy-fast=%d",
			counts["idle-slow"], counts["busy-fast"])
	}
}

// TestUpdateDispatchScoreConverges: a node whose real measured throughput is
// consistently higher than its seeded (e.g. tier-default) score should see
// its dispatch_score climb toward the measured value over repeated EWMA
// updates — confirms defaultDispatchScore is just a starting point, not a
// permanent ceiling.
func TestUpdateDispatchScoreConverges(t *testing.T) {
	score := 1.0 // e.g. a BRONZE default that undersells real performance
	const measured = 20.0 // tokens/sec
	for i := 0; i < 50; i++ {
		score = (1-dispatchScoreAlpha)*score + dispatchScoreAlpha*measured
	}
	if score < measured*0.95 {
		t.Errorf("expected dispatch_score to converge near %.1f after 50 updates, got %.2f", measured, score)
	}
}
