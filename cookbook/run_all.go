// run_all.go — MomaGrid Cookbook batch runner.
//
// Recipes are defined in cookbook/cookbook_catalog.json — edit that file to
// add, remove, or update recipes without touching Go code.
//
// Usage:
//
//	go run cookbook/run_all.go                           # run all active recipes
//	go run cookbook/run_all.go --hub http://host:9000    # custom hub
//	go run cookbook/run_all.go --ids 04,08,13            # run specific recipes by ID
//	go run cookbook/run_all.go --list                    # brief recipe list
//	go run cookbook/run_all.go --catalog                 # full catalog table
//	go run cookbook/run_all.go --catalog --category performance   # filter by category
//	go run cookbook/run_all.go --catalog --status new    # filter by approval status

package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ApprovalStatus values for recipe.ApprovalStatus.
const (
	StatusApproved = "approved" // tested, verified, eligible for is_active=true
	StatusNew      = "new"      // written but not yet tested
	StatusWIP      = "wip"      // incomplete / under development
	StatusDisabled = "disabled" // intentionally excluded (needs special infra, etc.)
	StatusRejected = "rejected" // known broken or out of scope
)

// Category values for recipe.Category.
const (
	CatBasics      = "basics"      // entry-level: hello, multi-cte, SPL compiler
	CatPerformance = "performance" // throughput, benchmarking, tier-dispatch
	CatResilience  = "resilience"  // failover, wake-sleep, model-health
	CatApplication = "application" // real-world tasks: RAG, translation, pipeline
	CatAIQuality   = "ai-quality"  // model evaluation: arena, olympiad, fingerprinting
	CatDeveloper   = "developer"   // code-focused: review, guardian, junior-dev
	CatResearch    = "research"    // academic / learning: paper pipeline, micro-learning
	CatOperations  = "operations"  // ops: rewards, overnight-batch, federated-search
	CatCluster     = "cluster"     // multi-hub: two-hub-cluster
)

// recipe mirrors one entry in cookbook_catalog.json.
type recipe struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	Args           []string `json:"args"`
	Dir            string   `json:"dir"`
	Log            string   `json:"log"`
	IsActive       bool     `json:"is_active"`
	ApprovalStatus string   `json:"approval_status"`
	Category       string   `json:"category"`
}

type catalog struct {
	Recipes []recipe `json:"recipes"`
}

// runResult holds the outcome of a single recipe execution.
type runResult struct {
	id      string
	name    string
	ok      bool
	elapsed time.Duration
	logPath string
}

func defaultHubURL() string {
	home, _ := os.UserHomeDir()
	data, err := os.ReadFile(filepath.Join(home, ".igrid", "config.yaml"))
	if err != nil {
		return "http://localhost:9000"
	}
	var cfg struct {
		Hub struct {
			URLs []string `yaml:"urls"`
			Port int      `yaml:"port"`
		} `yaml:"hub"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return "http://localhost:9000"
	}
	if len(cfg.Hub.URLs) > 0 {
		return strings.TrimRight(cfg.Hub.URLs[0], "/")
	}
	if cfg.Hub.Port != 0 {
		return fmt.Sprintf("http://localhost:%d", cfg.Hub.Port)
	}
	return "http://localhost:9000"
}

// loadCatalog reads cookbook_catalog.json relative to this source file.
func loadCatalog(cookbookDir string) ([]recipe, error) {
	path := filepath.Join(cookbookDir, "cookbook_catalog.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", path, err)
	}
	var c catalog
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse error in %s: %w", path, err)
	}
	return c.Recipes, nil
}

func main() {
	hubURL := flag.String("hub", defaultHubURL(), "Hub URL")
	idsFlag := flag.String("ids", "", "Comma-separated recipe IDs to run (default: all active)")
	listFlag := flag.Bool("list", false, "List recipes (brief) and exit")
	catalogFlag := flag.Bool("catalog", false, "Print full recipe catalog table and exit")
	catFilter := flag.String("category", "", "Filter catalog by category (use with --catalog or --list)")
	statusFilter := flag.String("status", "", "Filter catalog by approval_status (use with --catalog or --list)")
	flag.Parse()

	// Resolve cookbook directory regardless of working directory.
	cookbookDir, _ := filepath.Abs(filepath.Dir(os.Args[0]))
	if strings.Contains(cookbookDir, "go-build") || cookbookDir == os.TempDir() {
		cookbookDir, _ = os.Getwd()
		if _, err := os.Stat(filepath.Join(cookbookDir, "cookbook")); err == nil {
			cookbookDir = filepath.Join(cookbookDir, "cookbook")
		}
	}

	recipes, err := loadCatalog(cookbookDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading catalog: %v\n", err)
		os.Exit(1)
	}

	if *catalogFlag {
		printCatalog(recipes, *catFilter, *statusFilter)
		return
	}

	if *listFlag {
		printList(recipes, *catFilter, *statusFilter)
		return
	}

	// Build ID filter set
	filter := map[string]bool{}
	if *idsFlag != "" {
		for _, id := range strings.Split(*idsFlag, ",") {
			filter[strings.TrimSpace(id)] = true
		}
	}

	startAll := time.Now()
	ts := startAll.Format("2006-01-02 15:04:05")
	fmt.Printf("=== Momahub Go Cookbook Batch Run — %s ===\n", ts)
	fmt.Printf("    Hub: %s\n\n", *hubURL)

	var results []runResult

	for _, rec := range recipes {
		if len(filter) > 0 {
			if !filter[rec.ID] {
				continue
			}
		} else if !rec.IsActive {
			fmt.Printf("[%s] %s  (skipping — %s)\n\n", rec.ID, rec.Name,
				strings.ToUpper(rec.ApprovalStatus))
			continue
		}

		// Substitute hub placeholder in args
		args := make([]string, len(rec.Args))
		for i, a := range rec.Args {
			args[i] = strings.ReplaceAll(a, "{hub}", *hubURL)
		}

		logName := fmt.Sprintf("%s_%s.log", rec.Log, time.Now().Format("20060102_150405"))
		logPath := filepath.Join(cookbookDir, rec.Dir, logName)

		fmt.Printf("[%s] %s\n", rec.ID, rec.Name)
		fmt.Printf("     cmd : %s\n", strings.Join(args, " "))
		fmt.Printf("     log : %s\n", logPath)

		ok, elapsed := runRecipe(args, logPath, cookbookDir)
		status := "SUCCESS"
		if !ok {
			status = "FAILED"
		}
		fmt.Printf("     result: %s  (%.1fs)\n\n", status, elapsed.Seconds())

		results = append(results, runResult{
			id: rec.ID, name: rec.Name,
			ok: ok, elapsed: elapsed, logPath: logPath,
		})
	}

	// Summary table
	total := len(results)
	passed := 0
	for _, r := range results {
		if r.ok {
			passed++
		}
	}
	fmt.Printf("=== Summary: %d/%d Success  (total %.1fs) ===\n\n",
		passed, total, time.Since(startAll).Seconds())

	fmt.Printf("%-4s  %-28s  %-8s  %8s\n", "ID", "Recipe", "Status", "Elapsed")
	fmt.Println(strings.Repeat("-", 56))
	for _, r := range results {
		status := "OK"
		if !r.ok {
			status = "FAILED"
		}
		fmt.Printf("%-4s  %-28s  %-8s  %7.1fs\n",
			r.id, r.name, status, r.elapsed.Seconds())
	}
	fmt.Println()
}

// applyFilters returns only the recipes matching the given category and status
// filters (empty string = no filter).
func applyFilters(recipes []recipe, catFilter, statusFilter string) []recipe {
	var out []recipe
	for _, r := range recipes {
		if catFilter != "" && r.Category != catFilter {
			continue
		}
		if statusFilter != "" && r.ApprovalStatus != statusFilter {
			continue
		}
		out = append(out, r)
	}
	return out
}

// statusMarker returns a short visual indicator for the approval status.
func statusMarker(r recipe) string {
	if r.IsActive {
		return "✅"
	}
	switch r.ApprovalStatus {
	case StatusNew:
		return "🆕"
	case StatusWIP:
		return "🔧"
	case StatusDisabled:
		return "⏸ "
	case StatusRejected:
		return "❌"
	default:
		return "  "
	}
}

// printCatalog prints the full recipe catalog, optionally filtered.
func printCatalog(recipes []recipe, catFilter, statusFilter string) {
	now := time.Now().Format("2006-01-02 15:04:05")
	filtered := applyFilters(recipes, catFilter, statusFilter)

	// Count by status
	counts := map[string]int{}
	for _, r := range recipes {
		if r.IsActive {
			counts["active"]++
		}
		counts[r.ApprovalStatus]++
	}

	fmt.Printf("=== MomaGrid Cookbook Catalog — %s ===\n", now)
	if catFilter != "" || statusFilter != "" {
		fmt.Printf("    Filter: category=%q  status=%q  → %d/%d recipes\n\n",
			catFilter, statusFilter, len(filtered), len(recipes))
	} else {
		fmt.Printf("    Total: %d recipes  |  %d active  |  %d new  |  %d disabled\n\n",
			len(recipes), counts["active"], counts[StatusNew], counts[StatusDisabled])
	}

	fmt.Printf("%-4s  %-2s  %-28s  %-14s  %-12s  %s\n",
		"ID", "", "Name", "Category", "Status", "Description")
	fmt.Println(strings.Repeat("-", 100))

	for _, r := range filtered {
		fmt.Printf("%-4s  %s  %-28s  %-14s  %-12s  %s\n",
			r.ID, statusMarker(r), r.Name, r.Category, r.ApprovalStatus, r.Description)
	}

	fmt.Println()
	fmt.Printf("Markers: ✅ active  🆕 new  🔧 wip  ⏸  disabled  ❌ rejected\n\n")
	fmt.Printf("Run active recipes:           go run cookbook/run_all.go\n")
	fmt.Printf("Run specific recipe:          go run cookbook/run_all.go --ids 04,13\n")
	fmt.Printf("Run any (incl. inactive):     go run cookbook/run_all.go --ids 29\n")
	fmt.Printf("Filter catalog by category:   go run cookbook/run_all.go --catalog --category performance\n")
	fmt.Printf("Filter catalog by status:     go run cookbook/run_all.go --catalog --status new\n")

	// Print category summary
	catCounts := map[string]int{}
	for _, r := range recipes {
		catCounts[r.Category]++
	}
	cats := make([]string, 0, len(catCounts))
	for c := range catCounts {
		cats = append(cats, c)
	}
	sort.Strings(cats)
	fmt.Println()
	fmt.Printf("Categories: ")
	parts := make([]string, 0, len(cats))
	for _, c := range cats {
		parts = append(parts, fmt.Sprintf("%s(%d)", c, catCounts[c]))
	}
	fmt.Println(strings.Join(parts, "  "))
}

// printList prints a compact one-line-per-recipe listing, optionally filtered.
func printList(recipes []recipe, catFilter, statusFilter string) {
	filtered := applyFilters(recipes, catFilter, statusFilter)
	fmt.Printf("MomaGrid Cookbook — %d recipes", len(filtered))
	if catFilter != "" || statusFilter != "" {
		fmt.Printf(" (category=%q status=%q)", catFilter, statusFilter)
	}
	fmt.Println()
	for _, r := range filtered {
		fmt.Printf("  %-4s  %s  %-28s  %-12s  %-14s  %s\n",
			r.ID, statusMarker(r), r.Name, r.ApprovalStatus, r.Category, r.Description)
	}
}

// runRecipe executes a command, tees output to terminal + log file.
func runRecipe(args []string, logPath, cwd string) (bool, time.Duration) {
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "     mkdir error: %v\n", err)
		return false, 0
	}

	logFile, err := os.Create(logPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "     log create error: %v\n", err)
		return false, 0
	}
	defer logFile.Close()

	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = cwd

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return false, 0
	}
	cmd.Stderr = cmd.Stdout

	start := time.Now()
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "     start error: %v\n", err)
		return false, time.Since(start)
	}

	scanner := bufio.NewScanner(io.TeeReader(stdout, logFile))
	for scanner.Scan() {
		fmt.Printf("     | %s\n", scanner.Text())
	}

	err = cmd.Wait()
	elapsed := time.Since(start)
	return err == nil, elapsed
}
