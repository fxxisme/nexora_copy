package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestGojaSentinelSolverRunsEmbeddedVM(t *testing.T) {
	client := &UpstreamClient{userAgent: upstreamUserAgent(), scriptSources: []string{defaultPowScript}}
	solver := &gojaSentinelSolver{client: client}
	identity, err := solver.Identity(context.Background())
	if err != nil || identity.UserAgent != client.userAgent || identity.Timezone == "" || identity.DateString == "" {
		t.Fatalf("identity = %#v, err=%v", identity, err)
	}
	program, err := json.Marshal([]any{[]any{3, "OK"}})
	if err != nil {
		t.Fatal(err)
	}
	const seed = "test-seed"
	dx := base64.StdEncoding.EncodeToString([]byte(xorString(string(program), seed)))
	token, err := solver.Turnstile(context.Background(), nil, seed, dx)
	if err != nil {
		t.Fatal(err)
	}
	if token != base64.StdEncoding.EncodeToString([]byte("OK")) {
		t.Fatalf("VM token = %q", token)
	}
}
