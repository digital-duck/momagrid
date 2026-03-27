package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type GeminiProvider struct {
	APIKey string
}

func (p *GeminiProvider) Name() string {
	return "Google"
}

func (p *GeminiProvider) AuthType() AuthType {
	return AuthTypeAPIKey
}

func (p *GeminiProvider) ListModels() ([]Model, error) {
	return []Model{
		{ID: "gemini-1.5-pro-latest", Name: "Gemini 1.5 Pro", Description: "Most intelligent"},
		{ID: "gemini-1.5-flash-latest", Name: "Gemini 1.5 Flash", Description: "Fast and lightweight"},
		{ID: "gemini-1.0-pro", Name: "Gemini 1.0 Pro", Description: "Standard model"},
	}, nil
}

func (p *GeminiProvider) Submit(req ChatRequest) (ChatResponse, error) {
	start := time.Now()

	contents := []map[string]interface{}{}
	for _, m := range req.History {
		role := m.Role
		if role == "assistant" {
			role = "model"
		}
		contents = append(contents, map[string]interface{}{
			"role":  role,
			"parts": []map[string]string{{"text": m.Content}},
		})
	}
	contents = append(contents, map[string]interface{}{
		"role":  "user",
		"parts": []map[string]string{{"text": req.Prompt}},
	})

	payload := map[string]interface{}{
		"contents": contents,
		"generationConfig": map[string]interface{}{
			"maxOutputTokens": req.MaxTokens,
			"temperature":     req.Temperature,
		},
	}
	if req.System != "" {
		payload["system_instruction"] = map[string]interface{}{
			"parts": []map[string]string{{"text": req.System}},
		}
	}

	body, _ := json.Marshal(payload)
	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s", req.Model, p.APIKey)
	httpReq, _ := http.NewRequest("POST", url, bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return ChatResponse{}, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return ChatResponse{Error: fmt.Sprintf("Gemini error: %d %s", resp.StatusCode, string(respBody))}, nil
	}

	var result struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		UsageMetadata struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
		} `json:"usageMetadata"`
	}

	if err := json.Unmarshal(respBody, &result); err != nil {
		return ChatResponse{}, err
	}

	if len(result.Candidates) == 0 || len(result.Candidates[0].Content.Parts) == 0 {
		return ChatResponse{Error: "Gemini returned no candidates"}, nil
	}

	return ChatResponse{
		Content:      result.Candidates[0].Content.Parts[0].Text,
		Model:        req.Model,
		Provider:     p.Name(),
		Latency:      time.Since(start),
		InputTokens:  result.UsageMetadata.PromptTokenCount,
		OutputTokens: result.UsageMetadata.CandidatesTokenCount,
	}, nil
}
