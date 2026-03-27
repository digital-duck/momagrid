package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type OpenAIProvider struct {
	APIKey string
}

func (p *OpenAIProvider) Name() string {
	return "OpenAI"
}

func (p *OpenAIProvider) AuthType() AuthType {
	return AuthTypeAPIKey
}

func (p *OpenAIProvider) ListModels() ([]Model, error) {
	return []Model{
		{ID: "gpt-4o", Name: "GPT-4o", Description: "Most advanced model"},
		{ID: "gpt-4-turbo", Name: "GPT-4 Turbo", Description: "High intelligence"},
		{ID: "gpt-3.5-turbo", Name: "GPT-3.5 Turbo", Description: "Fast and cost-effective"},
	}, nil
}

func (p *OpenAIProvider) Submit(req ChatRequest) (ChatResponse, error) {
	start := time.Now()

	messages := []map[string]string{}
	if req.System != "" {
		messages = append(messages, map[string]string{"role": "system", "content": req.System})
	}
	for _, m := range req.History {
		messages = append(messages, map[string]string{"role": m.Role, "content": m.Content})
	}
	messages = append(messages, map[string]string{"role": "user", "content": req.Prompt})

	payload := map[string]interface{}{
		"model":       req.Model,
		"messages":    messages,
		"max_tokens":  req.MaxTokens,
		"temperature": req.Temperature,
	}

	body, _ := json.Marshal(payload)
	httpReq, _ := http.NewRequest("POST", "https://api.openai.com/v1/chat/completions", bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.APIKey)

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return ChatResponse{}, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return ChatResponse{Error: fmt.Sprintf("OpenAI error: %d %s", resp.StatusCode, string(respBody))}, nil
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}

	if err := json.Unmarshal(respBody, &result); err != nil {
		return ChatResponse{}, err
	}

	if len(result.Choices) == 0 {
		return ChatResponse{Error: "OpenAI returned no choices"}, nil
	}

	return ChatResponse{
		Content:      result.Choices[0].Message.Content,
		Model:        req.Model,
		Provider:     p.Name(),
		Latency:      time.Since(start),
		InputTokens:  result.Usage.PromptTokens,
		OutputTokens: result.Usage.CompletionTokens,
	}, nil
}
