package schema

// PulseReport is a heartbeat sent by an agent.
//
// Signed pulses include Signature = Sign(MakeChallenge(AgentID, Timestamp)).
// The hub verifies this against the public key stored at join time.
// Unsigned pulses (Signature == "") are accepted when the agent has no stored key.
type PulseReport struct {
	OperatorID        string      `json:"operator_id"`
	AgentID           string      `json:"agent_id"`
	Status            AgentStatus `json:"status"`
	GPUUtilizationPct float64     `json:"gpu_utilization_pct"`
	VramUsedGB        float64     `json:"vram_used_gb"`
	TasksCompleted    int         `json:"tasks_completed"`
	CurrentTPS        float64     `json:"current_tps"`
	// Identity fields (Ed25519) — omitempty for backward compat
	Signature string `json:"signature,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
}

// PulseAck is the hub's response to a PulseReport.
type PulseAck struct {
	OK      bool   `json:"ok"`
	HubTime string `json:"hub_time"`
}
