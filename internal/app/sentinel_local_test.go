package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

type fakeSentinelLocalSolver struct {
	identity sentinelIdentity
	token    string
	err      error
}

func (f fakeSentinelLocalSolver) Identity(context.Context) (sentinelIdentity, error) {
	return f.identity, f.err
}

func (f fakeSentinelLocalSolver) Turnstile(context.Context, map[string]any, string, string) (string, error) {
	return f.token, f.err
}

func TestLocalSentinelIdentityAndTokenValidation(t *testing.T) {
	identity := sentinelIdentity{UserAgent: "Chrome/146", Timezone: "Asia/Shanghai", DateString: "Fri Oct 16 2026 12:00:00 GMT+0800 (China Standard Time)"}
	client := &UpstreamClient{localSolver: fakeSentinelLocalSolver{identity: identity, token: "valid-token"}}
	ua, err := client.sentinelUserAgent(context.Background())
	if err != nil || ua != identity.UserAgent || client.timezone != identity.Timezone || client.browserDate != identity.DateString {
		t.Fatalf("identity: ua=%q timezone=%q date=%q err=%v", ua, client.timezone, client.browserDate, err)
	}
	if token, err := client.solveSentinelTurnstile(context.Background(), "dx", "p", nil); err != nil || token != "valid-token" {
		t.Fatalf("token=%q err=%v", token, err)
	}
	client.localSolver = fakeSentinelLocalSolver{token: "p"}
	if _, err := client.solveSentinelTurnstile(context.Background(), "dx", "p", nil); err == nil || !strings.Contains(err.Error(), "echoed") {
		t.Fatalf("echoed token error=%v", err)
	}
	client.localSolver = fakeSentinelLocalSolver{err: errors.New("browser unavailable")}
	if _, err := client.solveSentinelTurnstile(context.Background(), "dx", "p", nil); err == nil || !strings.Contains(err.Error(), "browser unavailable") {
		t.Fatalf("browser error=%v", err)
	}
}

func TestLiveLocalSentinelHandshake(t *testing.T) {
	if os.Getenv("CHATGPT2API_LIVE_SENTINEL") != "1" {
		t.Skip("set CHATGPT2API_LIVE_SENTINEL=1 for an upstream handshake")
	}
	data, err := os.ReadFile("../../config.json")
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Proxy string `json:"proxy"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	client, err := NewUpstreamClient("", config.Proxy, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer CloseSentinelSolvers()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	identity, err := client.localSolver.Identity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, char := range identity.DateString {
		if char > 127 {
			t.Fatal("browser Date fingerprint must use an ASCII timezone name")
		}
	}
	t.Logf("browser ready: timezone=%s user-agent=%s", identity.Timezone, identity.UserAgent)
	requirements, err := client.chatRequirements(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if requirements.Token == "" {
		t.Fatal("empty finalized requirements token")
	}
	t.Logf("handshake succeeded: proof=%t turnstile=%t", requirements.ProofToken != "", requirements.TurnstileToken != "")
}

func TestLiveLocalSentinelConversation(t *testing.T) {
	email := os.Getenv("CHATGPT2API_LIVE_ACCOUNT_EMAIL")
	if email == "" {
		t.Skip("set CHATGPT2API_LIVE_ACCOUNT_EMAIL for an upstream conversation")
	}
	configData, err := os.ReadFile("../../config.json")
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Proxy string `json:"proxy"`
	}
	if err := json.Unmarshal(configData, &config); err != nil {
		t.Fatal(err)
	}
	accountsData, err := os.ReadFile("../../data/accounts.json")
	if err != nil {
		t.Fatal(err)
	}
	var accounts []Account
	if err := json.Unmarshal(accountsData, &accounts); err != nil {
		t.Fatal(err)
	}
	var selected *Account
	for i := range accounts {
		if accounts[i].Email != nil && *accounts[i].Email == email {
			selected = &accounts[i]
			break
		}
	}
	if selected == nil || selected.AccessToken == "" {
		t.Fatal("requested test account was not found with an access token")
	}
	client, err := NewUpstreamClientForAccount(*selected, config.Proxy, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer CloseSentinelSolvers()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	events, errs := client.StreamTextConversationEvents(ctx, []map[string]any{{"role": "user", "content": "Reply with exactly: OK"}}, "auto", "")
	var gotContent bool
	for event := range events {
		gotContent = gotContent || event.Delta != ""
	}
	for err := range errs {
		if err != nil {
			t.Fatalf("conversation failed: %v", err)
		}
	}
	if !gotContent {
		t.Fatal("conversation completed without response content")
	}
	t.Log("conversation received response content")
}
