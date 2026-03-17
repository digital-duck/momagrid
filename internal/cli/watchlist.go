package cli

import (
	"flag"
	"fmt"
	"net/http"
)

// Watchlist implements "mg watchlist" — lists watchlist entries (spec §14).
func Watchlist(args []string) error {
	fs := flag.NewFlagSet("watchlist", flag.ExitOnError)
	hubURL := fs.String("hub-url", "", "Hub URL")
	fs.Parse(args)

	url := ResolveHubURL(*hubURL)
	data, err := getJSON(fmt.Sprintf("%s/watchlist", url))
	if err != nil {
		return err
	}
	entries := items(data, "entries")
	if len(entries) == 0 {
		fmt.Println("Watchlist is empty.")
		return nil
	}
	fmt.Printf("%-8s %-10s %-36s %-12s %-26s\n", "TYPE", "ACTION", "ENTITY_ID", "REASON", "EXPIRES_AT")
	fmt.Println(repeat('-', 96))
	for _, e := range entries {
		exp := str(e, "expires_at")
		if exp == "" {
			exp = "permanent"
		}
		fmt.Printf("%-8s %-10s %-36s %-12s %-26s\n",
			str(e, "entity_type"), str(e, "action"),
			str(e, "entity_id"), truncate(str(e, "reason"), 12), exp)
	}
	return nil
}

// Unblock implements "mg unblock <entity_id>" — removes an entity from the watchlist.
func Unblock(args []string) error {
	fs := flag.NewFlagSet("unblock", flag.ExitOnError)
	hubURL := fs.String("hub-url", "", "Hub URL")
	fs.Parse(args)

	remaining := fs.Args()
	if len(remaining) == 0 {
		return fmt.Errorf("usage: mg unblock <entity_id>")
	}
	entityID := remaining[0]
	url := ResolveHubURL(*hubURL)

	req, _ := http.NewRequest(http.MethodDelete, fmt.Sprintf("%s/watchlist/%s", url, entityID), nil)
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("server returned %d", resp.StatusCode)
	}
	fmt.Printf("Unblocked: %s\n", entityID)
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
