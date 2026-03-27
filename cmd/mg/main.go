package main

import (
	"fmt"
	"os"

	"github.com/digital-duck/momagrid/internal/cli"
)

const usage = `mg — momagrid unified CLI

Usage:
  mg <command> [flags]

Hub commands:
  hub up        Start the hub server
  hub pending   List agents awaiting approval (--admin mode)
  hub approve   Approve a pending agent
  hub reject    Reject a pending agent
  hub migrate   Migrate SQLite → PostgreSQL

Client commands:
  status        Hub health and agent count
  agents        List online agents
  tasks         List recent tasks
  submit        Submit a prompt
  rewards       Reward summary by operator
  logs          Agent pulse history
  export        Export task results to JSON
  join          Start an agent and join the grid

Cluster commands:
  peer add      Register a peer hub
  peer list     List peer hubs

Other commands:
  config        Show or set config (~/.igrid/config.yaml)
  test          Run batch test suite
  run           Execute an SPL recipe
  watchlist     Show rate-limited / blocked IPs
  unblock       Remove an entity from the watchlist

Run "mg <command> --help" for flag details.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(0)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "hub":
		err = cli.Hub(args)
	case "status":
		err = cli.Status(args)
	case "agents":
		err = cli.Agents(args)
	case "tasks":
		err = cli.Tasks(args)
	case "submit":
		err = cli.Submit(args)
	case "rewards":
		err = cli.Rewards(args)
	case "logs":
		err = cli.Logs(args)
	case "export":
		err = cli.Export(args)
	case "join":
		err = cli.Join(args)
	case "peer":
		err = cli.Peer(args)
	case "config":
		err = cli.Config(args)
	case "test":
		err = cli.Test(args)
	case "run":
		err = cli.Run(args)
	case "watchlist":
		err = cli.Watchlist(args)
	case "unblock":
		err = cli.Unblock(args)
	case "--help", "-h", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\nRun 'mg --help' for usage.\n", cmd)
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
