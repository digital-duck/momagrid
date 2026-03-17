package hub

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/digital-duck/momagrid/internal/schema"
)

// ClusterManager handles hub-to-hub peering.
type ClusterManager struct {
	State      *GridState
	ThisHubURL string
}

// capabilitiesFromAgents summarizes agent capabilities by tier.
func capabilitiesFromAgents(agents []map[string]interface{}) []schema.PeerCapability {
	type tierInfo struct {
		ids    map[string]bool
		models map[string]bool
	}
	tiers := map[schema.ComputeTier]*tierInfo{}
	for _, t := range []schema.ComputeTier{schema.TierPlatinum, schema.TierGold, schema.TierSilver, schema.TierBronze} {
		tiers[t] = &tierInfo{ids: map[string]bool{}, models: map[string]bool{}}
	}
	for _, a := range agents {
		status, _ := a["status"].(string)
		if status == "OFFLINE" {
			continue
		}
		tierStr, _ := a["tier"].(string)
		tier := schema.ComputeTier(tierStr)
		info, ok := tiers[tier]
		if !ok {
			continue
		}
		agentID, _ := a["agent_id"].(string)
		info.ids[agentID] = true
		modelsStr, _ := a["supported_models"].(string)
		var models []string
		json.Unmarshal([]byte(modelsStr), &models)
		for _, m := range models {
			info.models[m] = true
		}
	}
	var caps []schema.PeerCapability
	for _, t := range []schema.ComputeTier{schema.TierPlatinum, schema.TierGold, schema.TierSilver, schema.TierBronze} {
		info := tiers[t]
		if len(info.ids) > 0 {
			var ms []string
			for m := range info.models {
				ms = append(ms, m)
			}
			caps = append(caps, schema.PeerCapability{Tier: t, Count: len(info.ids), Models: ms})
		}
	}
	return caps
}

// AddPeer initiates a handshake with a peer hub.
func (cm *ClusterManager) AddPeer(peerURL string) (*schema.PeerHandshakeAck, error) {
	agents, _ := cm.State.ListAgents()
	caps := capabilitiesFromAgents(agents)
	hs := schema.PeerHandshake{
		HubID:        cm.State.HubID,
		HubURL:       cm.ThisHubURL,
		OperatorID:   cm.State.OperatorID,
		Capabilities: caps,
	}
	body, _ := json.Marshal(hs)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(peerURL+"/cluster/handshake", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var ack schema.PeerHandshakeAck
	json.NewDecoder(resp.Body).Decode(&ack)
	if ack.Accepted {
		cm.State.AddPeer(ack.HubID, peerURL, "")
		log.Printf("peer hub %s added", ack.HubID)
	}
	return &ack, nil
}

// PushCapabilities sends capability updates to all active peers.
func (cm *ClusterManager) PushCapabilities() error {
	peers, _ := cm.State.ListPeers()
	if len(peers) == 0 {
		return nil
	}
	agents, _ := cm.State.ListAgents()
	update := schema.PeerCapabilityUpdate{
		HubID:        cm.State.HubID,
		Capabilities: capabilitiesFromAgents(agents),
	}
	body, _ := json.Marshal(update)
	client := &http.Client{Timeout: 5 * time.Second}
	for _, peer := range peers {
		status, _ := peer["status"].(string)
		if status != "ACTIVE" {
			continue
		}
		hubURL, _ := peer["hub_url"].(string)
		hubID, _ := peer["hub_id"].(string)
		_, err := client.Post(hubURL+"/cluster/capabilities", "application/json", bytes.NewReader(body))
		if err != nil {
			log.Printf("cannot reach peer %s: %v", hubID, err)
			cm.State.MarkPeerUnreachable(hubID)
		} else {
			cm.State.MarkPeerSeen(hubID)
		}
	}
	return nil
}

// CapabilitiesFromAgents is exported for use in handlers.
func CapabilitiesFromAgents(agents []map[string]interface{}) []schema.PeerCapability {
	return capabilitiesFromAgents(agents)
}

// ForwardTask attempts to forward a task to a peer hub.
//
// It sets req.CallbackURL = thisHub/cluster/result so the peer can push the
// result back immediately on completion, avoiding the polling round-trip.
// Polling is kept as a fallback in case the callback is not delivered within
// the task timeout.
func (cm *ClusterManager) ForwardTask(req schema.TaskRequest) (*schema.TaskResult, error) {
	peers, _ := cm.State.ListPeers()
	var active []map[string]interface{}
	for _, p := range peers {
		if s, _ := p["status"].(string); s == "ACTIVE" {
			active = append(active, p)
		}
	}
	if len(active) == 0 {
		return nil, fmt.Errorf("no active peers")
	}

	// Tell the peer where to push the result when it completes.
	if cm.ThisHubURL != "" {
		req.CallbackURL = cm.ThisHubURL + "/cluster/result"
	}

	body, _ := json.Marshal(req)
	client := &http.Client{Timeout: time.Duration(req.TimeoutS+15) * time.Second}

	for _, peer := range active {
		hubURL, _ := peer["hub_url"].(string)
		hubID, _ := peer["hub_id"].(string)

		resp, err := client.Post(hubURL+"/tasks", "application/json", bytes.NewReader(body))
		if err != nil {
			log.Printf("forward to peer %s failed: %v", hubID, err)
			cm.State.MarkPeerUnreachable(hubID)
			continue
		}
		resp.Body.Close()
		cm.State.MarkForwarded(req.TaskID, hubID)

		// Wait for the webhook callback to fire (written into the result by handleClusterResult).
		// Fall back to polling if the callback never arrives.
		result := waitForResult(cm.State, req.TaskID, req.TimeoutS)
		if result == nil {
			log.Printf("callback timeout for forwarded task %s, falling back to poll", req.TaskID)
			result = pollPeer(client, hubURL, req.TaskID, req.TimeoutS)
		}
		if result != nil {
			result.TaskID = req.TaskID
			cm.State.CompleteTask(req.TaskID, *result)
			return result, nil
		}
	}
	return nil, fmt.Errorf("all peers failed")
}

// waitForResult polls the local DB until the task leaves FORWARDED state.
// This is resolved quickly when the peer fires the /cluster/result callback.
func waitForResult(state *GridState, taskID string, timeoutS int) *schema.TaskResult {
	deadline := time.Now().Add(time.Duration(timeoutS) * time.Second)
	for time.Now().Before(deadline) {
		row, err := state.GetTask(taskID)
		if err != nil || row == nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		st, _ := row["state"].(string)
		if st == string(schema.StateComplete) || st == string(schema.StateFailed) {
			// Task was resolved via callback — build result from DB row.
			res := &schema.TaskResult{
				TaskID:  taskID,
				Content: fmt.Sprint(row["result"]),
			}
			return res
		}
		time.Sleep(500 * time.Millisecond)
	}
	return nil
}

func pollPeer(client *http.Client, baseURL, taskID string, timeoutS int) *schema.TaskResult {
	deadline := time.Now().Add(time.Duration(timeoutS) * time.Second)
	interval := 2 * time.Second
	for time.Now().Before(deadline) {
		resp, err := client.Get(fmt.Sprintf("%s/tasks/%s", baseURL, taskID))
		if err != nil {
			time.Sleep(interval)
			continue
		}
		var data map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&data)
		resp.Body.Close()
		state, _ := data["state"].(string)
		if state == string(schema.StateComplete) || state == string(schema.StateFailed) {
			src := data
			if r, ok := data["result"].(map[string]interface{}); ok {
				src = r
			}
			b, _ := json.Marshal(src)
			var result schema.TaskResult
			json.Unmarshal(b, &result)
			return &result
		}
		time.Sleep(interval)
		if interval < 10*time.Second {
			interval = time.Duration(float64(interval) * 1.5)
		}
	}
	return nil
}
