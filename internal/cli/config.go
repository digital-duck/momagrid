package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// HubCfg holds hub server settings.
type HubCfg struct {
	Host   string   `yaml:"host"`
	Port   int      `yaml:"port"`
	DBPath string   `yaml:"db_path"`
	APIKey string   `yaml:"api_key"`
	URLs   []string `yaml:"urls"`
}

// AgentCfg holds agent settings.
type AgentCfg struct {
	Host      string `yaml:"host"`
	Port      int    `yaml:"port"`
	ID        string `yaml:"id"`
	Name      string `yaml:"name"`
	OllamaURL string `yaml:"ollama_url"`
}

// MguiCfg holds settings for the mgui unified web UI.
// API keys are stored here and should be treated as secrets.
// A future enhancement (see enhancements.md §6) will encrypt them with AES-GCM.
type MguiCfg struct {
	Host          string   `yaml:"host"`
	Port          int      `yaml:"port"`
	FallbackChain []string `yaml:"fallback_chain"` // ordered provider preference
	OpenAIKey     string   `yaml:"openai_api_key"`
	AnthropicKey  string   `yaml:"anthropic_api_key"`
	GoogleKey     string   `yaml:"google_api_key"`
	OpenRouterKey string   `yaml:"openrouter_api_key"`
}

// AppConfig is the top-level ~/.igrid/config.yaml structure.
type AppConfig struct {
	OperatorID string   `yaml:"operator_id"`
	Hub        HubCfg   `yaml:"hub"`
	Agent      AgentCfg `yaml:"agent"`
	Mgui       MguiCfg  `yaml:"mgui"`
}

var defaultConfig = AppConfig{
	OperatorID: "duck",
	Hub: HubCfg{
		Host:   "0.0.0.0",
		Port:   9000,
		DBPath: ".igrid/hub.db",
		APIKey: "",
		URLs:   []string{}, // empty → falls back to http://localhost:{port} in HubURL()
	},
	Agent: AgentCfg{
		Host:      "0.0.0.0",
		Port:      9010,
		OllamaURL: "http://localhost:11434",
	},
	Mgui: MguiCfg{
		Host:          "127.0.0.1", // localhost-only by default; set 0.0.0.0 for LAN access
		Port:          9080,
		FallbackChain: []string{"momagrid"}, // local grid first; add openrouter/openai as fallbacks
	},
}

func configDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".igrid")
}

func configFile() string {
	return filepath.Join(configDir(), "config.yaml")
}

// LoadConfig reads ~/.igrid/config.yaml with defaults applied.
func LoadConfig() AppConfig {
	cfg := defaultConfig
	data, err := os.ReadFile(configFile())
	if err != nil {
		return cfg
	}
	_ = yaml.Unmarshal(data, &cfg)
	if cfg.OperatorID == "" {
		cfg.OperatorID = defaultConfig.OperatorID
	}
	if cfg.Hub.Port == 0 {
		cfg.Hub.Port = defaultConfig.Hub.Port
	}
	if cfg.Hub.Host == "" {
		cfg.Hub.Host = defaultConfig.Hub.Host
	}
	if len(cfg.Hub.URLs) == 0 {
		cfg.Hub.URLs = defaultConfig.Hub.URLs
	}
	if cfg.Hub.DBPath == "" {
		cfg.Hub.DBPath = defaultConfig.Hub.DBPath
	}
	if cfg.Agent.Port == 0 {
		cfg.Agent.Port = defaultConfig.Agent.Port
	}
	if cfg.Agent.Host == "" {
		cfg.Agent.Host = defaultConfig.Agent.Host
	}
	if cfg.Agent.OllamaURL == "" {
		cfg.Agent.OllamaURL = defaultConfig.Agent.OllamaURL
	}
	if cfg.Mgui.Port == 0 {
		cfg.Mgui.Port = defaultConfig.Mgui.Port
	}
	if cfg.Mgui.Host == "" {
		cfg.Mgui.Host = defaultConfig.Mgui.Host
	}
	if len(cfg.Mgui.FallbackChain) == 0 {
		cfg.Mgui.FallbackChain = defaultConfig.Mgui.FallbackChain
	}
	return cfg
}

// SaveConfig writes config to ~/.igrid/config.yaml.
func SaveConfig(cfg AppConfig) error {
	if err := os.MkdirAll(configDir(), 0755); err != nil {
		return err
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(configFile(), data, 0644)
}

// HubURL returns the first hub URL, with trailing slash stripped.
func (c AppConfig) HubURL() string {
	if len(c.Hub.URLs) > 0 {
		return strings.TrimRight(c.Hub.URLs[0], "/")
	}
	return fmt.Sprintf("http://localhost:%d", c.Hub.Port)
}

// ResolveHubURL picks the hub URL from flag or config.
func ResolveHubURL(flagURL string) string {
	if flagURL != "" {
		return strings.TrimRight(flagURL, "/")
	}
	return LoadConfig().HubURL()
}

// Config implements the "mg config" command.
func Config(args []string) error {
	if len(args) >= 2 && args[0] == "--set" {
		kv := args[1]
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) != 2 {
			return fmt.Errorf("use --set key=value")
		}
		cfg := LoadConfig()
		key, val := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		switch key {
		case "operator_id":
			cfg.OperatorID = val
		case "hub.host":
			cfg.Hub.Host = val
		case "hub.port":
			fmt.Sscanf(val, "%d", &cfg.Hub.Port)
		case "hub.db_path":
			cfg.Hub.DBPath = val
		case "hub.api_key":
			cfg.Hub.APIKey = val
		case "hub.urls":
			cfg.Hub.URLs = []string{val}
		case "mgui.host":
			cfg.Mgui.Host = val
		case "mgui.port":
			fmt.Sscanf(val, "%d", &cfg.Mgui.Port)
		case "mgui.openai_api_key":
			cfg.Mgui.OpenAIKey = val
		case "mgui.anthropic_api_key":
			cfg.Mgui.AnthropicKey = val
		case "mgui.google_api_key":
			cfg.Mgui.GoogleKey = val
		case "mgui.openrouter_api_key":
			cfg.Mgui.OpenRouterKey = val
		case "agent.host":
			cfg.Agent.Host = val
		case "agent.port":
			fmt.Sscanf(val, "%d", &cfg.Agent.Port)
		case "agent.ollama_url":
			cfg.Agent.OllamaURL = val
		default:
			return fmt.Errorf("unknown config key: %s", key)
		}
		if err := SaveConfig(cfg); err != nil {
			return err
		}
		fmt.Printf("Set %s = %s\n", key, val)
		return nil
	}

	cfg := LoadConfig()
	fmt.Printf("  operator_id:              %s\n", cfg.OperatorID)
	fmt.Printf("  hub.host:                 %s\n", cfg.Hub.Host)
	fmt.Printf("  hub.port:                 %d\n", cfg.Hub.Port)
	fmt.Printf("  hub.db_path:              %s\n", cfg.Hub.DBPath)
	fmt.Printf("  hub.urls:                 %v\n", cfg.Hub.URLs)
	if cfg.Hub.APIKey != "" {
		fmt.Printf("  hub.api_key:              ***\n")
	} else {
		fmt.Printf("  hub.api_key:              (not set)\n")
	}
	fmt.Printf("  agent.host:               %s\n", cfg.Agent.Host)
	fmt.Printf("  agent.port:               %d\n", cfg.Agent.Port)
	fmt.Printf("  agent.ollama_url:         %s\n", cfg.Agent.OllamaURL)
	fmt.Printf("  mgui.host:                %s\n", cfg.Mgui.Host)
	fmt.Printf("  mgui.port:                %d\n", cfg.Mgui.Port)
	fmt.Printf("  mgui.fallback_chain:      %v\n", cfg.Mgui.FallbackChain)
	maskKey := func(k string) string {
		if k != "" {
			return "***"
		}
		return "(not set)"
	}
	fmt.Printf("  mgui.openai_api_key:      %s\n", maskKey(cfg.Mgui.OpenAIKey))
	fmt.Printf("  mgui.anthropic_api_key:   %s\n", maskKey(cfg.Mgui.AnthropicKey))
	fmt.Printf("  mgui.google_api_key:      %s\n", maskKey(cfg.Mgui.GoogleKey))
	fmt.Printf("  mgui.openrouter_api_key:  %s\n", maskKey(cfg.Mgui.OpenRouterKey))
	return nil
}
