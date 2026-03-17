package schema

// ComputeTier represents agent performance tiers based on TPS.
type ComputeTier string

const (
	TierPlatinum ComputeTier = "PLATINUM" // >= 60 TPS
	TierGold     ComputeTier = "GOLD"     // >= 30 TPS
	TierSilver   ComputeTier = "SILVER"   // >= 15 TPS
	TierBronze   ComputeTier = "BRONZE"   // <  15 TPS
)

// TierFromTPS returns the compute tier for a given TPS value.
func TierFromTPS(tps float64) ComputeTier {
	switch {
	case tps >= 60:
		return TierPlatinum
	case tps >= 30:
		return TierGold
	case tps >= 15:
		return TierSilver
	default:
		return TierBronze
	}
}

// TierFromVRAM returns an initial compute tier based on GPU VRAM.
// Used at join time before any TPS data is available.
// Thresholds: Platinum >= 16 GB, Gold >= 10 GB, Silver >= 6 GB, Bronze < 6 GB.
func TierFromVRAM(vramGB float64) ComputeTier {
	switch {
	case vramGB >= 16:
		return TierPlatinum
	case vramGB >= 10:
		return TierGold
	case vramGB >= 6:
		return TierSilver
	default:
		return TierBronze
	}
}

// TierOrder maps tiers to ordinal values (lower = better).
var TierOrder = map[ComputeTier]int{
	TierPlatinum: 0,
	TierGold:     1,
	TierSilver:   2,
	TierBronze:   3,
}

// TaskState represents the lifecycle state of a task.
type TaskState string

const (
	StatePending    TaskState = "PENDING"
	StateDispatched TaskState = "DISPATCHED"
	StateInFlight   TaskState = "IN_FLIGHT"
	StateForwarded  TaskState = "FORWARDED"
	StateComplete   TaskState = "COMPLETE"
	StateFailed     TaskState = "FAILED"
)

// AgentStatus represents the operational status of an agent.
type AgentStatus string

const (
	StatusOnline          AgentStatus = "ONLINE"
	StatusBusy            AgentStatus = "BUSY"
	StatusOffline         AgentStatus = "OFFLINE"
	StatusPendingApproval AgentStatus = "PENDING_APPROVAL"
)
