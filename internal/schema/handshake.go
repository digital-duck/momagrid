package schema

// GPUInfo describes a single GPU on an agent.
type GPUInfo struct {
	Index  int     `json:"index"`
	Model  string  `json:"model"`
	VramGB float64 `json:"vram_gb"`
}

// JoinRequest is sent by an agent to register with the hub.
//
// Ed25519 identity fields (optional — hub accepts unsigned joins for
// backward-compat, but will reject pulse signatures from unsigned agents):
//   - PublicKey: base64-encoded Ed25519 public key (32 bytes → 44 chars).
//   - Signature: base64-encoded signature of MakeChallenge(AgentID, Timestamp).
//   - Timestamp: RFC3339 UTC time the signature was created (replay protection).
type JoinRequest struct {
	OperatorID      string    `json:"operator_id"`
	AgentID         string    `json:"agent_id"`
	Host            string    `json:"host"`
	Port            int       `json:"port"`
	Name            string    `json:"name"`
	GPUs            []GPUInfo `json:"gpus"`
	CPUCores        int       `json:"cpu_cores"`
	RamGB           float64   `json:"ram_gb"`
	SupportedModels []string  `json:"supported_models"`
	CachedModels    []string  `json:"cached_models"`
	PullMode        bool      `json:"pull_mode"`
	APIKey          string    `json:"api_key"`
	// Identity fields (Ed25519)
	PublicKey string `json:"public_key,omitempty"`
	Signature string `json:"signature,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
}

// JoinAck is the hub's response to a JoinRequest.
type JoinAck struct {
	Accepted   bool        `json:"accepted"`
	HubID      string      `json:"hub_id"`
	OperatorID string      `json:"operator_id"`
	AgentID    string      `json:"agent_id"`
	Tier       ComputeTier `json:"tier"`
	Name       string      `json:"name"`
	Message    string      `json:"message"`
	Status     string      `json:"status"`
}

// LeaveRequest is sent by an agent to deregister.
type LeaveRequest struct {
	OperatorID string `json:"operator_id"`
	AgentID    string `json:"agent_id"`
}

// LeaveAck is the hub's response to a LeaveRequest.
type LeaveAck struct {
	OK bool `json:"ok"`
}
