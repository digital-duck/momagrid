package cli

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/digital-duck/momagrid/internal/hub"
)

// Hub dispatches hub subcommands: up, pending, approve, reject.
func Hub(args []string) error {
	if len(args) == 0 {
		fmt.Println("Usage: mg hub <up|pending|approve|reject> [flags]")
		return nil
	}
	switch args[0] {
	case "up":
		return hubUp(args[1:])
	case "pending":
		return hubPending(args[1:])
	case "approve":
		return hubApprove(args[1:])
	case "reject":
		return hubReject(args[1:])
	case "migrate":
		return Migrate(args[1:])
	default:
		return fmt.Errorf("unknown hub command: %s", args[0])
	}
}

func hubUp(args []string) error {
	cfg := LoadConfig()
	fs := flag.NewFlagSet("hub up", flag.ExitOnError)
	host := fs.String("host", cfg.Hub.Host, "Listen address")
	port := fs.Int("port", cfg.Hub.Port, "Listen port")
	hubURL := fs.String("hub-url", "", "Public hub URL (default: auto-detect)")
	dbPath := fs.String("db", cfg.Hub.DBPath, "SQLite database path")
	operatorID := fs.String("operator-id", cfg.OperatorID, "Operator ID")
	apiKey := fs.String("api-key", cfg.Hub.APIKey, "API key for agent registration")
	admin := fs.Bool("admin", false, "Enable admin mode: agents require verification")
	maxConcurrent := fs.Int("max-concurrent", 3, "Max concurrent tasks per agent")
	maxRetries := fs.Int("max-retries", 3, "Max requeue attempts for transient agent errors (EOF, connection refused)")
	maxPromptChars := fs.Int("max-prompt-chars", 50000, "Soft prompt size limit (HTTP 413 if exceeded)")
	maxQueueDepth := fs.Int("max-queue-depth", 1000, "Max PENDING tasks in queue (HTTP 503 if exceeded)")
	rateLimit := fs.Int("rate-limit", 300, "Max requests per minute per IP")
	burstThreshold := fs.Int("burst-threshold", 200, "Requests per 10s before flood auto-block")
	fs.Parse(args)

	// Persist any explicitly-provided flags back to config.
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "host":
			cfg.Hub.Host = *host
		case "port":
			cfg.Hub.Port = *port
		case "db":
			cfg.Hub.DBPath = *dbPath
		case "operator-id":
			cfg.OperatorID = *operatorID
		case "api-key":
			cfg.Hub.APIKey = *apiKey
		}
	})
	_ = SaveConfig(cfg)

	if *hubURL == "" {
		*hubURL = fmt.Sprintf("http://%s:%d", detectLANIP(), *port)
	}

	hubCfg := hub.HubConfig{
		OperatorID:         *operatorID,
		DBPath:             *dbPath,
		HubURL:             *hubURL,
		APIKey:             *apiKey,
		AdminMode:          *admin,
		MaxConcurrentTasks: *maxConcurrent,
		MaxRetries:         *maxRetries,
		MaxPromptChars:     *maxPromptChars,
		MaxQueueDepth:      *maxQueueDepth,
		RateLimit:          *rateLimit,
		BurstThreshold:     *burstThreshold,
	}

	app, err := hub.NewApp(hubCfg)
	if err != nil {
		return fmt.Errorf("failed to create hub: %w", err)
	}

	// Graceful shutdown
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		log.Println("shutting down...")
		app.Stop()
		os.Exit(0)
	}()

	modeLabel := "OPEN (any agent can join)"
	if *admin {
		modeLabel = "ADMIN (agents require verification)"
	}

	addr := fmt.Sprintf("%s:%d", *host, *port)
	fmt.Printf("Starting hub on %s\n", addr)
	fmt.Printf("  Mode: %s\n", modeLabel)
	fmt.Printf("  Max concurrent tasks per agent: %d\n", *maxConcurrent)
	fmt.Println()
	fmt.Printf("  Other machines can join with:\n")
	fmt.Printf("    mg join %s\n", *hubURL)
	fmt.Println()

	return app.Start(addr)
}

func hubPending(args []string) error {
	fs := flag.NewFlagSet("hub pending", flag.ExitOnError)
	hubURL := fs.String("hub-url", "", "Hub URL")
	fs.Parse(args)

	url := ResolveHubURL(*hubURL)
	data, err := getJSON(fmt.Sprintf("%s/agents/pending", url))
	if err != nil {
		return err
	}
	agents := items(data, "agents")
	if len(agents) == 0 {
		fmt.Println("No agents pending approval.")
		return nil
	}
	fmt.Printf("%-38s %-15s %-25s\n", "AGENT_ID", "OPERATOR", "JOINED_AT")
	fmt.Println(repeat('-', 78))
	for _, a := range agents {
		fmt.Printf("%-38s %-15s %-25s\n", str(a, "agent_id"), str(a, "operator_id"), str(a, "joined_at"))
	}
	return nil
}

func hubApprove(args []string) error {
	fs := flag.NewFlagSet("hub approve", flag.ExitOnError)
	hubURL := fs.String("hub-url", "", "Hub URL")
	fs.Parse(args)

	remaining := fs.Args()
	if len(remaining) == 0 {
		return fmt.Errorf("usage: mg hub approve <agent_id>")
	}
	agentID := remaining[0]
	url := ResolveHubURL(*hubURL)
	_, err := postJSON(fmt.Sprintf("%s/agents/%s/approve", url, agentID), nil)
	if err != nil {
		return err
	}
	fmt.Printf("Agent %s approved.\n", agentID)
	return nil
}

func hubReject(args []string) error {
	fs := flag.NewFlagSet("hub reject", flag.ExitOnError)
	hubURL := fs.String("hub-url", "", "Hub URL")
	fs.Parse(args)

	remaining := fs.Args()
	if len(remaining) == 0 {
		return fmt.Errorf("usage: mg hub reject <agent_id>")
	}
	agentID := remaining[0]
	url := ResolveHubURL(*hubURL)
	_, err := postJSON(fmt.Sprintf("%s/agents/%s/reject", url, agentID), nil)
	if err != nil {
		return err
	}
	fmt.Printf("Agent %s rejected.\n", agentID)
	return nil
}

func detectLANIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "127.0.0.1"
	}
	defer conn.Close()
	addr := conn.LocalAddr().(*net.UDPAddr)
	return addr.IP.String()
}

func repeat(ch byte, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = ch
	}
	return string(b)
}
