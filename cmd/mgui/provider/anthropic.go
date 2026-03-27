package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type AnthropicProvider struct {
	APIKey string
}

func (p *AnthropicProvider) Name() string {
	return "Anthropic"
}

func (p *AnthropicProvider) AuthType() AuthType {
	return AuthTypeAPIKey
}

func (p *AnthropicProvider) ListModels() ([]Model, error) {
	return []Model{
		{ID: "claude-3-opus-20240229", Name: "Claude 3 Opus", Description: "Highest performance"},
		{ID: "claude-3-sonnet-20240229", Name: "Claude 3 Sonnet", Description: "Balanced"},
		{ID: "claude-3-haiku-20240307", Name: "Claude 3 Haiku", Description: "Fast and light"},
	}, nil
}

func (p *AnthropicProvider) Submit(req ChatRequest) (ChatResponse, error) {
	start := time.Now()

	messages := []map[string]string{}
	for _, m := range req.History {
		messages = append(messages, map[string]string{"role": m.Role, "content": m.Content})
	}
	messages = append(messages, map[string]string{"role": "user", "content": req.Prompt})

	payload := map[string]interface{}{
		"model":      req.Model,
		"messages":   messages,
		"max_tokens": req.MaxTokens,
	}
	if req.System != "" {
		payload["system"] = req.System
	}

	body, _ := json.Marshal(payload)
	httpReq, _ := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-API-Key", p.APIKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return ChatResponse{}, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return ChatResponse{Error: fmt.Sprintf("Anthropic error: %d %s", resp.StatusCode, string(respBody))}, nil
	}

	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}

	if err := json.Unmarshal(respBody, &result); err != nil {
		return ChatResponse{}, err
	}

	if len(result.Content) == 0 {
		return ChatResponse{Error: "Anthropic returned no content"}, nil
	}

	return ChatResponse{
		Content:      result.Content[0].Text,
		Model:        req.Model,
		Provider:     p.Name(),
		Latency:      time.Since(start),
		InputTokens:  result.Usage.InputTokens,
		OutputTokens: result.Usage.OutputTokens,
	}, nil
}
