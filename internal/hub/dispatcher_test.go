package hub

import (
	"testing"
)

// TestWeightedPickNoStarvation guards the exact bug fixed 2026-08-20: a
// 2-node grid where the higher-tier agent absorbed 100% of dispatch,
// leaving the lower-tier (but fully eligible) agent completely idle. The
// old strict ONLINE > tier > least-loaded ordering always picked the
// single best candidate deterministically; weightedPick must instead give
// every eligible candidate a nonzero share, proportionally larger for
// higher tiers.
func TestWeightedPickNoStarvation(t *testing.T) {
	candidates := []agentCandidate{
		{agent: map[string]interface{}{"agent_id": "platinum-node", "tier": "PLATINUM", "status": "ONLINE"}, activeCount: 0},
		{agent: map[string]interface{}{"agent_id": "gold-node", "tier": "GOLD", "status": "ONLINE"}, activeCount: 0},
	}

	const trials = 20000
	counts := map[string]int{}
	for i := 0; i < trials; i++ {
		picked := weightedPick(candidates)
		counts[picked["agent_id"].(string)]++
	}

	platinumShare := float64(counts["platinum-node"]) / trials
	goldShare := float64(counts["gold-node"]) / trials

	if counts["gold-node"] == 0 {
		t.Fatalf("gold-node starved: got %d/%d dispatches (expected nonzero)", counts["gold-node"], trials)
	}
	if platinumShare <= goldShare {
		t.Fatalf("expected platinum-node (higher tier) to get more traffic than gold-node; got platinum=%.3f gold=%.3f", platinumShare, goldShare)
	}
	// tierWeight ratio is 8:4 = 2:1 — allow generous tolerance for
	// statistical noise over 20k trials (expect ~0.667 / ~0.333).
	if platinumShare < 0.55 || platinumShare > 0.78 {
		t.Errorf("platinum share %.3f outside expected ~0.55-0.78 band for an 8:4 weight ratio", platinumShare)
	}
}

// TestWeightedPickLoadAdjustment: an agent already juggling active tasks
// should get proportionally less share within its tier, even against a
// lower-tier idle agent — load matters, not just tier.
func TestWeightedPickLoadAdjustment(t *testing.T) {
	candidates := []agentCandidate{
		{agent: map[string]interface{}{"agent_id": "busy-platinum", "tier": "PLATINUM", "status": "ONLINE"}, activeCount: 10},
		{agent: map[string]interface{}{"agent_id": "idle-bronze", "tier": "BRONZE", "status": "ONLINE"}, activeCount: 0},
	}

	const trials = 20000
	counts := map[string]int{}
	for i := 0; i < trials; i++ {
		picked := weightedPick(candidates)
		counts[picked["agent_id"].(string)]++
	}

	// busy-platinum weight: 8 / (1+10) ≈ 0.727; idle-bronze weight: 1 / 1 = 1.
	// idle-bronze should win more often despite the lower tier.
	if counts["idle-bronze"] <= counts["busy-platinum"] {
		t.Errorf("expected idle-bronze to outweigh a heavily-loaded platinum node; got idle-bronze=%d busy-platinum=%d",
			counts["idle-bronze"], counts["busy-platinum"])
	}
}
