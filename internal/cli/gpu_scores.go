package cli

import (
	"flag"
	"fmt"
)

// GPUScores implements "mg gpu-scores" — lists the historical dispatch_score
// average per GPU model (see docs/DEV/task-dispatch.md), highest first.
// Pass --refresh to force an on-demand recompute before listing instead of
// waiting for the hub's 10-minute background timer.
func GPUScores(args []string) error {
	fs := flag.NewFlagSet("gpu-scores", flag.ExitOnError)
	hubURL := fs.String("hub-url", "", "Hub URL")
	refresh := fs.Bool("refresh", false, "Recompute scores from current agent data before listing")
	fs.Parse(args)

	url := ResolveHubURL(*hubURL)

	if *refresh {
		if _, err := postJSON(fmt.Sprintf("%s/gpu-scores/refresh", url), nil); err != nil {
			return err
		}
	}

	data, err := getJSON(fmt.Sprintf("%s/gpu-scores", url))
	if err != nil {
		return err
	}
	rows := items(data, "gpu_scores")
	if len(rows) == 0 {
		fmt.Println("No GPU score history yet — scores accumulate as agents complete tasks.")
		return nil
	}
	fmt.Printf("%-40s %10s %8s %-20s\n", "GPU_MODEL", "AVG_SCORE", "SAMPLES", "UPDATED_AT")
	for _, r := range rows {
		fmt.Printf("%-40s %10.2f %8.0f %-20s\n",
			str(r, "gpu_model"), num(r, "avg_score"), num(r, "sample_count"), str(r, "updated_at"))
	}
	return nil
}
