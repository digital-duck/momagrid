package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
	"github.com/google/uuid"
)

type MomagridProvider struct {
	HubURL     string
	OperatorID string
}

func (p *MomagridProvider) Name() string     { return "Momagrid" }
func (p *MomagridProvider) AuthType() AuthType { return AuthTypeKeyPair }

func (p *MomagridProvider) ListModels() ([]Model, error) {
	resp, err := http.Get(p.HubURL + "/agents")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var data struct {
		Agents []map[string]interface{} `json:"agents"`
	}
	json.NewDecoder(resp.Body).Decode(&data)

	modelMap := make(map[string]bool)
	var models []Model
	for _, agent := range data.Agents {
		if sm, ok := agent["supported_models"].(string); ok {
			var amodels []string
			json.Unmarshal([]byte(sm), &amodels)
			for _, m := range amodels {
				if !modelMap[m] {
					modelMap[m] = true
					models = append(models, Model{ID: m, Name: m})
				}
			}
		}
	}
	return models, nil
}

func (p *MomagridProvider) Submit(req ChatRequest) (ChatResponse, error) {
	start := time.Now()
	taskID := uuid.New().String()
	payload := map[string]interface{}{
		"task_id":     taskID,
		"model":       req.Model,
		"prompt":      req.Prompt,
		"system":      req.System,
		"max_tokens":  req.MaxTokens,
		"temperature": req.Temperature,
	}
	body, _ := json.Marshal(payload)
	_, err := http.Post(p.HubURL+"/tasks", "application/json", bytes.NewReader(body))
	if err != nil {
		return ChatResponse{}, err
	}

	// Polling for completion
	for i := 0; i < 60; i++ {
		resp, err := http.Get(fmt.Sprintf("%s/tasks/%s", p.HubURL, taskID))
		if err != nil {
			time.Sleep(2 * time.Second)
			continue
		}
		var status map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&status)
		resp.Body.Close()

		if status["state"] == "COMPLETE" {
			res := status["result"].(map[string]interface{})
			return ChatResponse{
				Content:      res["content"].(string),
				Model:        req.Model,
				Provider:     p.Name(),
				Latency:      time.Since(start),
				InputTokens:  int(res["input_tokens"].(float64)),
				OutputTokens: int(res["output_tokens"].(float64)),
			}, nil
		}
		if status["state"] == "FAILED" {
			return ChatResponse{Error: "Task failed on grid"}, nil
		}
		time.Sleep(2 * time.Second)
	}
	return ChatResponse{Error: "Timeout waiting for grid response"}, nil
}
