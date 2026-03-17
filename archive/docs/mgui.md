# mgui — Unified Web UI Documentation

`mgui` is the web-based command center for the Momagrid network. It is designed to be a lightweight, single-binary deployment that provides a unified interface for multiple LLM providers and a simplified onboarding flow for new GPU agents.

## Architecture Overview

Following the "Pure Go" philosophy, `mgui` uses **zero frontend frameworks**. It is built with:
- **Backend:** Go (standard library `net/http` + `chi` router)
- **Frontend:** Vanilla JavaScript (ES6+), Vanilla CSS, and HTML5
- **Portability:** All static assets are embedded into the Go binary using `go:embed`.

## Directory Structure

```text
cmd/mgui/
├── main.go           # Entry point & flag parsing
├── handler.go        # API handlers & Hub Proxy logic
├── provider/         # Provider interface & implementations (OpenAI, Momagrid, etc.)
└── static/           # Frontend assets (Embedded)
    ├── index.html    # Layout & UI structure
    ├── style.css     # Modern, responsive styling
    └── app.js        # UI logic, SSE handling, & API calls
```

## Key Components

### 1. Unified Provider Interface (`/provider`)
The backend defines a Go `Provider` interface. This allows `mgui` to treat the local Momagrid Hub, OpenAI, Anthropic, and others identically.
- **Sync Mode:** Returns the LLM response directly.
- **Async Mode:** Submits a `Job` to the Momagrid Hub and returns a `job_id` for tracking.

### 2. Simplified Onboarding (The "Probe" Flow)
To make joining the grid easy for non-technical users, `mgui` implements an automated probe:
1. **Detection:** When the user clicks the "Join Grid" tab, JS calls `/api/probe`.
2. **Backend Check:** The Go handler attempts to contact Ollama on `localhost:11434`.
3. **Guidance:** If Ollama is missing or has no models, the UI renders a "Redactor" guide with specific installation commands.
4. **Registration:** Once validated, a one-click button initiates an **SSE (Server-Sent Events)** stream that shows real-time registration progress.

### 3. The Hub Proxy
To avoid CORS (Cross-Origin Resource Sharing) issues and keep API keys secure, `mgui` acts as a reverse proxy. 
- Requests to `/api/hub/*` are automatically forwarded by the Go backend to the actual Momagrid Hub URL.
- This allows the frontend to talk to the Hub as if it were on the same port.

### 4. Plain JS UI Logic (`app.js`)
The frontend uses a simple "State-on-DOM" approach:
- **Tab Switching:** Hidden/Show logic via CSS classes.
- **Polling:** Uses `setInterval` to refresh the "Grid Status" tables every 10 seconds.
- **Persistence:** User preferences (email, sync/async mode) are saved in the browser's `localStorage`.

## Configuration & Deployment

`mgui` is included in the unified build but can be run independently:

```bash
# Build
go build -o mgui ./cmd/mgui

# Run pointing to a remote hub
./mgui --hub http://192.168.0.177:9000 --port 9080
```

## Backend Logic Deep Dive

### 1. The Provider Abstraction (`/provider`)
To support multiple LLM backends (OpenAI, Anthropic, Momagrid) without cluttering the handlers, we use a Go `interface`. This is the core of `mgui`'s extensibility.

```go
type Provider interface {
    Name() string
    ListModels() ([]Model, error)
    Submit(req ChatRequest) (ChatResponse, error)
    AuthType() AuthType
}
```

- **OpenAI/Anthropic Adapters**: These transform our internal `ChatRequest` into the specific JSON format required by their respective REST APIs.
- **Momagrid Adapter**: This implementation acts as a client to the local Hub. If the user selects "Async Mode" in settings, it bypasses the standard `Submit` flow and hits the Hub's `/jobs` endpoint instead.

### 2. The Hub Proxy (`handler.go`)
Because the Hub and `mgui` often run on different ports, browsers would block requests due to **CORS**. We solve this on the backend using Go's `httputil.ReverseProxy`.

```go
func (h *Handler) HubProxy(w http.ResponseWriter, r *http.Request) {
    target, _ := url.Parse(h.HubURL)
    proxy := httputil.NewSingleHostReverseProxy(target)
    
    // Rewrite path: /api/hub/agents -> /agents
    r.URL.Path = strings.TrimPrefix(r.URL.Path, "/api/hub")
    proxy.ServeHTTP(w, r)
}
```
This pattern allows the frontend to simply fetch `/api/hub/agents`, and the Go backend handles the network heavy lifting to the actual Hub URL.

### 3. Server-Sent Events (SSE) Registration
The "Join Grid" wizard uses **SSE** instead of standard REST. This allows the backend to "push" status updates to the UI as it performs the multi-step onboarding process.

```go
func (h *Handler) Join(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "text/event-stream")
    
    // Step 1: Detect hardware
    fmt.Fprintf(w, "data: {\"step\": \"Detecting GPU...\"}\n\n")
    w.(http.Flusher).Flush()
    
    // Step 2: Call Hub /join API
    // ... logic ...
}
```

### 4. Integration with the Async Job Queue
When a user submits a prompt in **Async Mode**, the backend logic flow is:
1. `handler.Chat` receives the request.
2. It detects the `async` preference from the payload.
3. It routes the request to `MomagridProvider.SubmitJob`.
4. The Hub returns a `job_id` immediately.
5. The `JobLoop` (background goroutine) in the Hub picks it up, while the `mgui` Status tab polls `/api/hub/jobs` to show the user the "QUEUED" -> "IN_FLIGHT" -> "COMPLETE" transition.

## Developer Notes (Backend Focus)
- **Adding a Route:** Add the handler method to the `Handler` struct in `handler.go` and register it in the `http.NewServeMux()` in `main.go`.
- **Environment Variables:** `mgui` looks for `IGRID_SMTP_*` variables to handle the email notification logic implemented in the Hub's `Notifier`.
- **JSON Handling:** We use Go's standard `encoding/json`. For the dynamic nature of LLM responses, we often use `map[string]interface{}` to avoid rigid struct definitions for third-party APIs.
