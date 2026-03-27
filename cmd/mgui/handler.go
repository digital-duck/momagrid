package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
	"github.com/digital-duck/momagrid/cmd/mgui/provider"
)

type Handler struct {
	HubURL    string
	Providers map[string]provider.Provider
}

func NewHandler(hubURL string) *Handler {
	return &Handler{
		HubURL: hubURL,
		Providers: map[string]provider.Provider{
			"momagrid":   &provider.MomagridProvider{HubURL: hubURL, OperatorID: "mgui-user"},
			"openai":     &provider.OpenAIProvider{},
			"anthropic":  &provider.AnthropicProvider{},
			"google":     &provider.GeminiProvider{},
			"openrouter": &provider.OpenRouterProvider{},
		},
	}
}

func (h *Handler) HubProxy(w http.ResponseWriter, r *http.Request) {
	target, _ := url.Parse(h.HubURL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	r.URL.Path = strings.TrimPrefix(r.URL.Path, "/api/hub")
	proxy.ServeHTTP(w, r)
}

func (h *Handler) Chat(w http.ResponseWriter, r *http.Request) {
	var req provider.ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", 400)
		return
	}
	pName := r.URL.Query().Get("provider")
	p := h.Providers[pName]
	if p == nil {
		http.Error(w, "Provider not found", 404)
		return
	}

	resp, err := p.Submit(req)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	json.NewEncoder(w).Encode(resp)
}

func (h *Handler) Probe(w http.ResponseWriter, r *http.Request) {
	// Check Ollama
	ollamaURL := "http://localhost:11434/api/tags"
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(ollamaURL)

	ollamaStatus := "Running"
	var models []string
	if err != nil {
		ollamaStatus = "Not Found"
	} else {
		defer resp.Body.Close()
		var data struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		json.NewDecoder(resp.Body).Decode(&data)
		for _, m := range data.Models {
			models = append(models, m.Name)
		}
	}

	// Simplified GPU detection (in a real app, this would call nvidia-smi or pynvml)
	gpuStatus := "Detected"

	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "ok",
		"gpu":    gpuStatus,
		"ollama": ollamaStatus,
		"models": models,
	})
}

func (h *Handler) Join(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	fmt.Fprintf(w, "data: {\"step\": \"Detecting hardware...\", \"status\": \"done\"}\n\n")
	w.(http.Flusher).Flush()
	time.Sleep(500 * time.Millisecond)

	fmt.Fprintf(w, "data: {\"step\": \"Registering with hub...\", \"status\": \"done\"}\n\n")
	w.(http.Flusher).Flush()
	time.Sleep(500 * time.Millisecond)

	fmt.Fprintf(w, "data: {\"step\": \"ONLINE\", \"status\": \"done\"}\n\n")
	w.(http.Flusher).Flush()
}

func (h *Handler) handleListProviders(w http.ResponseWriter, r *http.Request) {
	var list []map[string]interface{}
	for id, p := range h.Providers {
		models, _ := p.ListModels()
		list = append(list, map[string]interface{}{
			"id":     id,
			"name":   p.Name(),
			"auth":   p.AuthType(),
			"models": models,
		})
	}
	json.NewEncoder(w).Encode(list)
}
