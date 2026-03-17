package cli

import (
	"flag"
	"fmt"
)

// Peer dispatches peer subcommands: add, list.
func Peer(args []string) error {
	if len(args) == 0 {
		fmt.Println("Usage: mg peer <add|list> [flags]")
		return nil
	}
	switch args[0] {
	case "add":
		return peerAdd(args[1:])
	case "list":
		return peerList(args[1:])
	default:
		return fmt.Errorf("unknown peer command: %s", args[0])
	}
}

func peerAdd(args []string) error {
	fs := flag.NewFlagSet("peer add", flag.ExitOnError)
	hubURL := fs.String("hub-url", "", "Hub URL")
	fs.Parse(args)

	remaining := fs.Args()
	if len(remaining) == 0 {
		return fmt.Errorf("usage: mg peer add <peer_url>")
	}
	peerURL := remaining[0]
	url := ResolveHubURL(*hubURL)

	data, err := postJSON(fmt.Sprintf("%s/cluster/peers", url), map[string]string{"url": peerURL})
	if err != nil {
		return err
	}
	accepted, _ := data["accepted"].(bool)
	if accepted {
		fmt.Printf("Peer %s added\n", str(data, "hub_id"))
	} else {
		fmt.Printf("Peer rejected: %s\n", str(data, "message"))
	}
	return nil
}

func peerList(args []string) error {
	fs := flag.NewFlagSet("peer list", flag.ExitOnError)
	hubURL := fs.String("hub-url", "", "Hub URL")
	fs.Parse(args)

	url := ResolveHubURL(*hubURL)
	data, err := getJSON(fmt.Sprintf("%s/cluster/status", url))
	if err != nil {
		return err
	}
	fmt.Printf("This hub: %s\n", str(data, "this_hub_id"))
	peers := items(data, "peers")
	if len(peers) == 0 {
		fmt.Println("No peer hubs.")
		return nil
	}
	for _, p := range peers {
		fmt.Printf("  %s %s [%s]\n", str(p, "hub_id"), str(p, "hub_url"), str(p, "status"))
	}
	return nil
}
