package hub

import (
	"math/rand"

	"github.com/digital-duck/momagrid/internal/schema"

	"github.com/google/uuid"
)

// verificationPrompt holds a test prompt with minimum expected tokens.
type verificationPrompt struct {
	Prompt    string
	MinTokens int
}

var verificationPrompts = []verificationPrompt{
	{"List the planets in our solar system in order from the sun.", 20},
	{"Explain what photosynthesis is in two sentences.", 15},
	{"Write a Python function that returns the sum of a list.", 10},
	{"What are the three states of matter?", 10},
	{"Translate 'hello world' into French, Spanish, and German.", 10},
	{"Name five programming languages and one use case for each.", 15},
	{"What is the speed of light in meters per second?", 5},
	{"Describe how a binary search algorithm works.", 15},
}

const verifyTaskPrefix = "verify-"

// PickVerificationTask creates a random verification task for an agent.
func PickVerificationTask(agentID, model string) schema.TaskRequest {
	entry := verificationPrompts[rand.Intn(len(verificationPrompts))]
	return schema.TaskRequest{
		TaskID:      verifyTaskPrefix + uuid.New().String()[:12],
		Model:       model,
		Prompt:      entry.Prompt,
		System:      "Answer concisely.",
		MaxTokens:   256,
		Temperature: 0.7,
		MinTier:     schema.TierBronze,
		TimeoutS:    120,
		Priority:    0,
	}
}

// CheckVerificationResult validates a verification response.
func CheckVerificationResult(result *schema.TaskResult, elapsedMs float64) bool {
	if result.Content == "" {
		return false
	}
	if result.OutputTokens <= 0 {
		return false
	}
	if elapsedMs > 120_000 {
		return false
	}
	return true
}

// ShouldSampleForReview returns true with probability rate.
func ShouldSampleForReview(rate float64) bool {
	return rand.Float64() < rate
}
