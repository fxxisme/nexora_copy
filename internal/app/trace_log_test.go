package app

import (
	"strings"
	"testing"
)

func TestSentinelTraceBodyRedactsChallengeValues(t *testing.T) {
	preview := traceBodyPreview(map[string]any{
		"p":           "sensitive-requirements-seed",
		"proofofwork": "sensitive-proof-value",
		"turnstile":   "sensitive-turnstile-token",
		"model":       "auto",
	})
	for _, value := range []string{"sensitive-requirements-seed", "sensitive-proof-value", "sensitive-turnstile-token"} {
		if strings.Contains(preview, value) {
			t.Fatalf("trace preview contains challenge value: %q", preview)
		}
	}
	if !strings.Contains(preview, `"model":"auto"`) {
		t.Fatalf("trace preview lost nonsensitive context: %q", preview)
	}
}
