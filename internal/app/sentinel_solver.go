package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func (c *UpstreamClient) solverRequest(ctx context.Context, method, path string, body any, timeout time.Duration) (*http.Response, error) {
	if c.solverURL == "" {
		return nil, fmt.Errorf("sentinel solver is not configured (CHATGPT2API_SENTINEL_SOLVER_URL or sentinel_solver_url)")
	}
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.solverURL+path, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.solverKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.solverKey)
	}
	return (&http.Client{Timeout: timeout}).Do(req)
}

func (c *UpstreamClient) sentinelUserAgent(ctx context.Context) (string, error) {
	if c.solverURL == "" {
		if c.localSolver != nil {
			identity, err := c.localSolver.Identity(ctx)
			if err != nil {
				return "", fmt.Errorf("local sentinel solver: %w", err)
			}
			c.timezone = identity.Timezone
			c.browserDate = identity.DateString
			return identity.UserAgent, nil
		}
		return c.userAgent, nil
	}
	resp, err := c.solverRequest(ctx, http.MethodGet, "/health", nil, 10*time.Second)
	if err != nil {
		return "", fmt.Errorf("sentinel solver health: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("sentinel solver health: HTTP %d", resp.StatusCode)
	}
	var health struct {
		Ready      bool   `json:"ready"`
		UserAgent  string `json:"user_agent"`
		Timezone   string `json:"timezone"`
		DateString string `json:"date_string"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		return "", fmt.Errorf("sentinel solver health: %w", err)
	}
	if !health.Ready || strings.TrimSpace(health.UserAgent) == "" {
		return "", fmt.Errorf("sentinel solver page is not ready")
	}
	if health.Timezone != "" {
		if _, err := time.LoadLocation(health.Timezone); err != nil {
			return "", fmt.Errorf("sentinel solver timezone %q: %w", health.Timezone, err)
		}
		c.timezone = health.Timezone
	}
	c.browserDate = health.DateString
	return health.UserAgent, nil
}

func (c *UpstreamClient) solveSentinelTurnstile(ctx context.Context, dx, p string, requirements map[string]any) (string, error) {
	if c.solverURL == "" && c.localSolver != nil {
		token, err := c.localSolver.Turnstile(ctx, requirements, p, dx)
		if err != nil {
			return "", fmt.Errorf("local sentinel turnstile: %w", err)
		}
		if reason := invalidSentinelToken(token, p); reason != "" {
			return "", fmt.Errorf("local sentinel turnstile: %s", reason)
		}
		return token, nil
	}
	resp, err := c.solverRequest(ctx, http.MethodPost, "/v1/turnstile", map[string]any{
		"dx": dx, "p": p, "requirements": requirements,
	}, 90*time.Second)
	if err != nil {
		return "", fmt.Errorf("sentinel turnstile solver: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return "", fmt.Errorf("sentinel turnstile solver: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var result struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("sentinel turnstile solver: %w", err)
	}
	if strings.TrimSpace(result.Token) == "" {
		return "", fmt.Errorf("sentinel turnstile solver returned an empty token")
	}
	if result.Token == p {
		return "", fmt.Errorf("sentinel turnstile solver echoed the requirements token")
	}
	return result.Token, nil
}
