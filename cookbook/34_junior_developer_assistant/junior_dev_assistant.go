package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

const (
	// Sample code to analyze (simulating a real codebase file)
	sampleCode = `package main

import (
	"fmt"
	"strconv"
	"strings"
)

// UserData stores user information
type UserData struct {
	Name string
	Age  int
	Email string
}

func processUser(name string, age string, email string) *UserData {
	ageInt, err := strconv.Atoi(age)
	if err != nil {
		ageInt = 0
	}

	// Basic validation
	if len(name) == 0 {
		name = "Unknown"
	}

	if !strings.Contains(email, "@") {
		email = ""
	}

	user := &UserData{
		Name: name,
		Age: ageInt,
		Email: email,
	}

	return user
}

func printUserInfo(user *UserData) {
	fmt.Printf("User: %s, Age: %d, Email: %s\n", user.Name, user.Age, user.Email)
}

func main() {
	users := []string{
		"John,25,john@email.com",
		"Jane,30,jane@email.com",
		"Bob,invalid,bob@email.com",
	}

	for _, userStr := range users {
		parts := strings.Split(userStr, ",")
		if len(parts) != 3 {
			continue
		}

		user := processUser(parts[0], parts[1], parts[2])
		printUserInfo(user)
	}
}`

	codeReviewPrompt = `You are a senior Go developer performing a thorough code review.

Analyze this Go code and provide:
1. **Code Quality Issues**: Identify bugs, inefficiencies, or poor practices
2. **Security Concerns**: Point out potential security vulnerabilities
3. **Go Best Practices**: Suggest improvements following Go idioms
4. **Performance Optimizations**: Identify bottlenecks or optimization opportunities

Be specific and actionable. Focus on critical issues first.

Code to review:
%s`

	refactorPrompt = `You are an experienced software architect specializing in code maintainability.

Analyze this Go code and identify refactoring opportunities:

1. **Function Decomposition**: Functions that are too large or do too many things
2. **Code Duplication**: Repeated logic that could be extracted
3. **Naming Improvements**: Better variable/function names for clarity
4. **Structure Optimization**: Better organization of types, interfaces, or packages
5. **Error Handling**: More robust error handling patterns
6. **Testability**: Changes to make code more testable

Provide specific suggestions with brief code examples where helpful.

Code to analyze:
%s`

	documentationPrompt = `You are a technical writer creating development documentation.

Based on the code review and refactoring suggestions provided, create a comprehensive summary document:

1. **Executive Summary**: High-level overview of the code analysis
2. **Critical Issues Found**: Priority list of problems that need immediate attention
3. **Refactoring Roadmap**: Step-by-step plan for code improvements
4. **Implementation Notes**: Practical guidance for developers
5. **Quality Metrics**: Measurable improvements expected after changes

Make it clear, actionable, and suitable for both junior and senior developers.

Original Code:
%s

Code Review Results:
%s

Refactoring Suggestions:
%s`
)

type analysisResult struct {
	Stage        string  `json:"stage"`
	Model        string  `json:"model"`
	State        string  `json:"state"`
	Content      string  `json:"content"`
	OutputTokens float64 `json:"output_tokens"`
	LatencyMs    float64 `json:"latency_ms"`
	AgentID      string  `json:"agent_id"`
	Error        string  `json:"error,omitempty"`
}

type devAssistantReport struct {
	Timestamp     string           `json:"timestamp"`
	CodeReview    analysisResult   `json:"code_review"`
	Refactoring   analysisResult   `json:"refactoring"`
	Documentation analysisResult   `json:"documentation"`
	TotalLatencyS float64          `json:"total_latency_s"`
	Summary       string           `json:"summary"`
}

var client = &http.Client{Timeout: 60 * time.Second}

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
		return cfg.Hub.URLs[0]
	}
	return fmt.Sprintf("http://localhost:%d", cfg.Hub.Port)
}

func submitTask(hubURL, model, system, prompt string) (*analysisResult, error) {
	start := time.Now()
	taskID := uuid.New().String()

	payload := map[string]interface{}{
		"task_id":     taskID,
		"model":       model,
		"system":      system,
		"prompt":      prompt,
		"max_tokens":  1500,
		"temperature": 0.1,
	}

	body, _ := json.Marshal(payload)
	resp, err := http.Post(hubURL+"/tasks", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	resp.Body.Close()

	// Poll for completion
	for i := 0; i < 90; i++ {
		time.Sleep(2 * time.Second)

		resp, err := http.Get(fmt.Sprintf("%s/tasks/%s", hubURL, taskID))
		if err != nil {
			continue
		}

		var status map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&status)
		resp.Body.Close()

		state := status["state"].(string)
		if state == "COMPLETE" {
			res := status["result"].(map[string]interface{})
			return &analysisResult{
				Model:        model,
				State:        "COMPLETE",
				Content:      res["content"].(string),
				OutputTokens: res["output_tokens"].(float64),
				LatencyMs:    float64(time.Since(start).Milliseconds()),
				AgentID:      res["agent_id"].(string),
			}, nil
		}
		if state == "FAILED" {
			return &analysisResult{
				Model: model,
				State: "FAILED",
				Error: fmt.Sprintf("Task failed: %v", status["error"]),
			}, nil
		}
	}

	return &analysisResult{
		Model: model,
		State: "TIMEOUT",
		Error: "Task timeout after 3 minutes",
	}, nil
}

func runCodeReview(hubURL, model string) (*analysisResult, error) {
	fmt.Printf("  🔍 Running code review with %s...\n", model)

	result, err := submitTask(
		hubURL,
		model,
		"You are a senior Go developer and code reviewer. Focus on practical, actionable feedback.",
		fmt.Sprintf(codeReviewPrompt, sampleCode),
	)
	if err != nil {
		return nil, err
	}

	result.Stage = "code_review"
	fmt.Printf("      ✓ Code review completed (%dms, %s)\n", int(result.LatencyMs), result.AgentID)
	return result, nil
}

func runRefactoringAnalysis(hubURL, model string) (*analysisResult, error) {
	fmt.Printf("  🔧 Analyzing refactoring opportunities with %s...\n", model)

	result, err := submitTask(
		hubURL,
		model,
		"You are a software architect specializing in Go code maintainability and clean architecture.",
		fmt.Sprintf(refactorPrompt, sampleCode),
	)
	if err != nil {
		return nil, err
	}

	result.Stage = "refactoring"
	fmt.Printf("      ✓ Refactoring analysis completed (%dms, %s)\n", int(result.LatencyMs), result.AgentID)
	return result, nil
}

func runDocumentationGeneration(hubURL, model string, codeReview, refactoring *analysisResult) (*analysisResult, error) {
	fmt.Printf("  📝 Generating development documentation with %s...\n", model)

	result, err := submitTask(
		hubURL,
		model,
		"You are a technical writer creating clear, actionable development documentation.",
		fmt.Sprintf(documentationPrompt, sampleCode, codeReview.Content, refactoring.Content),
	)
	if err != nil {
		return nil, err
	}

	result.Stage = "documentation"
	fmt.Printf("      ✓ Documentation generated (%dms, %s)\n", int(result.LatencyMs), result.AgentID)
	return result, nil
}

func saveResults(outDir string, report *devAssistantReport) error {
	os.MkdirAll(outDir, 0755)
	ts := time.Now().Format("20060102_150405")
	filename := fmt.Sprintf("junior_dev_assistant_%s.json", ts)
	path := filepath.Join(outDir, filename)

	data, _ := json.MarshalIndent(report, "", "  ")
	return os.WriteFile(path, data, 0644)
}

func main() {
	hubURL := flag.String("hub", defaultHubURL(), "Hub URL")
	model := flag.String("model", "qwen2.5-coder:7b", "Model to use for analysis")
	outDir := flag.String("out", "cookbook/out", "Output directory")
	flag.Parse()

	fmt.Println("🦆 Junior Developer Assistant — Code Analysis Pipeline")
	fmt.Printf("   Hub: %s\n", *hubURL)
	fmt.Printf("   Model: %s\n", *model)
	fmt.Printf("   Sample code: %d lines\n", len(strings.Split(sampleCode, "\n")))
	fmt.Println()

	start := time.Now()
	var wg sync.WaitGroup
	var codeReview, refactoring *analysisResult
	var err1, err2 error

	// Run code review and refactoring analysis in parallel
	wg.Add(2)

	go func() {
		defer wg.Done()
		codeReview, err1 = runCodeReview(*hubURL, *model)
	}()

	go func() {
		defer wg.Done()
		refactoring, err2 = runRefactoringAnalysis(*hubURL, *model)
	}()

	wg.Wait()

	if err1 != nil {
		fmt.Printf("❌ Code review failed: %v\n", err1)
		return
	}
	if err2 != nil {
		fmt.Printf("❌ Refactoring analysis failed: %v\n", err2)
		return
	}

	// Run documentation generation sequentially (depends on previous results)
	documentation, err := runDocumentationGeneration(*hubURL, *model, codeReview, refactoring)
	if err != nil {
		fmt.Printf("❌ Documentation generation failed: %v\n", err)
		return
	}

	totalLatency := time.Since(start).Seconds()

	// Generate summary
	summary := fmt.Sprintf("Code analysis completed: %s reviewed %d lines, identified improvement opportunities, generated documentation (%d tokens total)",
		*model,
		len(strings.Split(sampleCode, "\n")),
		int(codeReview.OutputTokens+refactoring.OutputTokens+documentation.OutputTokens))

	report := &devAssistantReport{
		Timestamp:     time.Now().Format("2006-01-02 15:04:05"),
		CodeReview:    *codeReview,
		Refactoring:   *refactoring,
		Documentation: *documentation,
		TotalLatencyS: totalLatency,
		Summary:       summary,
	}

	// Save results
	if err := saveResults(*outDir, report); err != nil {
		fmt.Printf("⚠️  Failed to save results: %v\n", err)
	}

	fmt.Println("\n📊 Analysis Complete!")
	fmt.Printf("   Code Review: %s (%d tokens)\n", codeReview.State, int(codeReview.OutputTokens))
	fmt.Printf("   Refactoring: %s (%d tokens)\n", refactoring.State, int(refactoring.OutputTokens))
	fmt.Printf("   Documentation: %s (%d tokens)\n", documentation.State, int(documentation.OutputTokens))
	fmt.Printf("   Total Time: %.1fs\n", totalLatency)
	fmt.Printf("   Summary: %s\n", summary)

	// Print brief excerpts for verification
	fmt.Println("\n📋 Sample Outputs:")
	fmt.Printf("   Code Review: %s...\n", truncate(codeReview.Content, 100))
	fmt.Printf("   Refactoring: %s...\n", truncate(refactoring.Content, 100))
	fmt.Printf("   Documentation: %s...\n", truncate(documentation.Content, 100))
}

func truncate(s string, maxLen int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}