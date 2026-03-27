package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type OpenRouterProvider struct {
	APIKey string
}

func (p *OpenRouterProvider) Name() string {
	return "OpenRouter"
}

func (p *OpenRouterProvider) AuthType() AuthType {
	return AuthTypeAPIKey
}

func (p *OpenRouterProvider) ListModels() ([]Model, error) {
	return []Model{
		{ID: "meta-llama/llama-3-70b-instruct", Name: "Llama 3 70B", Description: "Meta's latest large model"},
		{ID: "mistralai/mixtral-8x7b-instruct", Name: "Mixtral 8x7B", Description: "Mistral's MoE model"},
		{ID: "google/gemini-pro-1.5", Name: "Gemini 1.5 Pro (OR)", Description: "Gemini via OpenRouter"},
		{ID: "anthropic/claude-3-haiku", Name: "Claude 3 Haiku (OR)", Description: "Claude via OpenRouter"},
	}, nil
}

func (p *OpenRouterProvider) Submit(req ChatRequest) (ChatResponse, error) {
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
	httpReq, _ := http.NewRequest("POST", "https://openrouter.ai/api/v1/chat/completions", bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.APIKey)
	httpReq.Header.Set("HTTP-Referer", "https://github.com/digital-duck/momagrid")
	httpReq.Header.Set("X-Title", "Momagrid UI")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return ChatResponse{}, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return ChatResponse{Error: fmt.Sprintf("OpenRouter error: %d %s", resp.StatusCode, string(respBody))}, nil
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
		return ChatResponse{Error: "OpenRouter returned no choices"}, nil
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
