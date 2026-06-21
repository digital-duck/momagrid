package cli

import (
	"flag"
	"fmt"
	"strings"
)

// Agent dispatches "mg agent <subcommand>" commands.
func Agent(args []string) error {
	if len(args) == 0 {
		fmt.Println("Usage: mg agent <tier> [flags]")
		fmt.Println()
		fmt.Println("Subcommands:")
		fmt.Println("  tier <agent_id> <TIER>   Set the compute tier of an agent")
		fmt.Println("                           Valid tiers: PLATINUM, GOLD, SILVER, BRONZE")
		return nil
	}
	switch args[0] {
	case "tier":
		return agentSetTier(args[1:])
	default:
		return fmt.Errorf("unknown agent command: %s", args[0])
	}
}

func agentSetTier(args []string) error {
	fs := flag.NewFlagSet("agent tier", flag.ExitOnError)
	hubURL := fs.String("hub-url", "", "Hub URL (default: from config)")
	fs.Parse(args)

	remaining := fs.Args()
	if len(remaining) < 2 {
		return fmt.Errorf("usage: mg agent tier <agent_id> <TIER>")
	}
	agentID := remaining[0]
	tier := strings.ToUpper(remaining[1])

	switch tier {
	case "PLATINUM", "GOLD", "SILVER", "BRONZE":
		// valid
	default:
		return fmt.Errorf("invalid tier %q — must be one of: PLATINUM, GOLD, SILVER, BRONZE", tier)
	}

	url := ResolveHubURL(*hubURL)
	body := map[string]string{"tier": tier}
	resp, err := patchJSON(fmt.Sprintf("%s/agents/%s", url, agentID), body)
	if err != nil {
		return fmt.Errorf("tier update failed: %w", err)
	}
	fmt.Printf("Agent %s tier set to %s\n", agentID, resp["tier"])
	return nil
}
