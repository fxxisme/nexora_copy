package app

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

//go:embed sentinel_node.js
var sentinelNodeScript string

type nodeSentinelSolver struct {
	client *UpstreamClient
}

func (s *nodeSentinelSolver) Identity(context.Context) (sentinelIdentity, error) {
	if _, err := findSentinelNode(); err != nil {
		return sentinelIdentity{}, err
	}
	zone := localTimezoneName()
	return sentinelIdentity{
		UserAgent:           s.client.userAgent,
		Timezone:            zone,
		DateString:          (&UpstreamClient{timezone: zone}).powDate(),
		ScreenSize:          1400,
		HeapLimit:           4395630592,
		Language:            "zh-CN",
		Languages:           "zh-CN,zh;q=0.9,en;q=0.8",
		HardwareConcurrency: 8,
		Vendor:              "Google Inc.",
	}, nil
}

func findSentinelNode() (string, error) {
	bin := strings.TrimSpace(os.Getenv("CHATGPT2API_NODE_BIN"))
	if bin == "" {
		bin = "node"
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		return "", fmt.Errorf("Node.js is required for browserless Sentinel solving; set CHATGPT2API_NODE_BIN: %w", err)
	}
	return path, nil
}

func (s *nodeSentinelSolver) Turnstile(ctx context.Context, _ map[string]any, p, dx string) (string, error) {
	if dx == "" {
		return "", errors.New("missing turnstile challenge")
	}
	bin, err := findSentinelNode()
	if err != nil {
		return "", err
	}
	input, err := json.Marshal(map[string]any{
		"dx": dx, "p": p, "user_agent": s.client.userAgent,
		"scripts": s.client.scriptSources, "vm_source": sentinelVMScript,
	})
	if err != nil {
		return "", err
	}
	runCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	command := exec.CommandContext(runCtx, bin, "-e", sentinelNodeScript)
	command.Env = append(os.Environ(), "TZ="+localTimezoneName())
	command.Stdin = bytes.NewReader(input)
	var output bytes.Buffer
	command.Stdout = &output
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("Node.js turnstile VM: %w: %s", err, truncateText(strings.TrimSpace(stderr.String()), 300))
	}
	var result struct {
		Token string `json:"token"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		return "", fmt.Errorf("decode Node.js turnstile result: %w", err)
	}
	if result.Error != "" {
		return "", fmt.Errorf("Node.js turnstile VM: %s", result.Error)
	}
	if reason := invalidSentinelToken(result.Token, p); reason != "" {
		return "", fmt.Errorf("Node.js turnstile VM: %s", reason)
	}
	return result.Token, nil
}
