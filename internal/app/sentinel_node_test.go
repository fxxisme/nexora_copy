package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

func TestSentinelSolverModeSelection(t *testing.T) {
	t.Setenv("CHATGPT2API_SENTINEL_SOLVER_URL", "")
	t.Setenv("CHATGPT2API_SENTINEL_MODE", "")
	client, err := NewUpstreamClient("", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := client.localSolver.(*gojaSentinelSolver); !ok {
		t.Fatalf("default solver = %T, want goja", client.localSolver)
	}
	t.Setenv("CHATGPT2API_SENTINEL_MODE", "browser")
	browserClient, err := NewUpstreamClient("", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := browserClient.localSolver.(*browserSentinelSolver); !ok {
		t.Fatalf("browser solver = %T", browserClient.localSolver)
	}
	t.Setenv("CHATGPT2API_SENTINEL_MODE", "node")
	nodeClient, err := NewUpstreamClient("", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := nodeClient.localSolver.(*nodeSentinelSolver); !ok {
		t.Fatalf("node solver = %T", nodeClient.localSolver)
	}
	t.Setenv("CHATGPT2API_SENTINEL_MODE", "goja")
	gojaClient, err := NewUpstreamClient("", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := gojaClient.localSolver.(*gojaSentinelSolver); !ok {
		t.Fatalf("goja solver = %T", gojaClient.localSolver)
	}
}

func TestNodeSentinelSolverRunsEmbeddedVM(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node.js is unavailable")
	}
	t.Setenv("CHATGPT2API_NODE_BIN", "")
	client := &UpstreamClient{userAgent: upstreamUserAgent(), scriptSources: []string{defaultPowScript}}
	solver := &nodeSentinelSolver{client: client}
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

func TestNodeSentinelSolverReportsMissingNode(t *testing.T) {
	t.Setenv("CHATGPT2API_NODE_BIN", "/nonexistent/chatgpt2api-node")
	solver := &nodeSentinelSolver{client: &UpstreamClient{userAgent: upstreamUserAgent()}}
	_, err := solver.Identity(context.Background())
	if err == nil || !strings.Contains(err.Error(), "CHATGPT2API_NODE_BIN") {
		t.Fatalf("missing runtime error = %v", err)
	}
}
