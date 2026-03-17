package schema

// PeerCapability describes the compute capacity of a peer hub.
type PeerCapability struct {
	Tier   ComputeTier `json:"tier"`
	Count  int         `json:"count"`
	Models []string    `json:"models"`
}

// PeerHandshake is sent to establish a hub-to-hub peering.
type PeerHandshake struct {
	HubID        string           `json:"hub_id"`
	HubURL       string           `json:"hub_url"`
	OperatorID   string           `json:"operator_id"`
	Capabilities []PeerCapability `json:"capabilities"`
}

// PeerHandshakeAck is the response to a PeerHandshake.
type PeerHandshakeAck struct {
	Accepted     bool             `json:"accepted"`
	HubID        string           `json:"hub_id"`
	HubURL       string           `json:"hub_url"`
	Capabilities []PeerCapability `json:"capabilities"`
	Message      string           `json:"message"`
}

// PeerCapabilityUpdate is a periodic capability push to peers.
type PeerCapabilityUpdate struct {
	HubID        string           `json:"hub_id"`
	Capabilities []PeerCapability `json:"capabilities"`
}

// ClusterStatus is the response for GET /cluster/status.
type ClusterStatus struct {
	ThisHubID string                   `json:"this_hub_id"`
	Peers     []map[string]interface{} `json:"peers"`
}
