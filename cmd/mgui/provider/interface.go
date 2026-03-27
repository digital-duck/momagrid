package provider

import "time"

type AuthType string

const (
	AuthTypeNone    AuthType = "None"
	AuthTypeAPIKey  AuthType = "APIKey"
	AuthTypeKeyPair AuthType = "KeyPair"
)

type Model struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type ChatRequest struct {
	Model       string    `json:"model"`
	Prompt      string    `json:"prompt"`
	System      string    `json:"system"`
	MaxTokens   int       `json:"max_tokens"`
	Temperature float64   `json:"temperature"`
	History     []Message `json:"history"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatResponse struct {
	Content      string        `json:"content"`
	Model        string        `json:"model"`
	Provider     string        `json:"provider"`
	Latency      time.Duration `json:"latency"`
	InputTokens  int           `json:"input_tokens"`
	OutputTokens int           `json:"output_tokens"`
	Error        string        `json:"error,omitempty"`
}

type Provider interface {
	Name() string
	ListModels() ([]Model, error)
	Submit(req ChatRequest) (ChatResponse, error)
	AuthType() AuthType
}
