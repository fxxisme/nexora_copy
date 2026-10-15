package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestAutoConversationModelNotFound(t *testing.T) {
	err := errors.New(`POST /backend-api/conversation failed: status=404 body={"detail": "Model not found"}`)
	if !isAutoConversationModelNotFound(err, "auto") || !isAutoConversationModelNotFound(err, "") {
		t.Fatal("auto conversation 404 should be treated as an account failure")
	}
	if isAutoConversationModelNotFound(err, "gpt-6-mini") {
		t.Fatal("an unavailable named model should not invalidate the account")
	}
	if isAutoConversationModelNotFound(errors.New(`POST /backend-api/f/conversation failed: status=404 body={"detail":"Model not found"}`), "auto") {
		t.Fatal("image conversation errors must not be classified as text account failures")
	}
	if isAutoConversationModelNotFound(errors.New("POST /backend-api/conversation failed: status=404 body="), "auto") {
		t.Fatal("an empty 404 body is inconclusive")
	}
}

func TestAutoConversation404QuarantinesAccount(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.SaveAccounts([]Account{{AccessToken: "bad", Status: "正常", Quota: 5}, {AccessToken: "good", Status: "正常", Quota: 5}}); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: store, cfg: Config{AutoRemoveInvalidAccounts: true}}
	err := errors.New(`POST /backend-api/conversation failed: status=404 body={"detail":"Model not found"}`)
	s.markAccountFailure("bad", err, false, "auto")
	accounts := store.LoadAccounts()
	if len(accounts) != 2 || accounts[0].Status != "异常" || accounts[0].Quota != 5 || accounts[0].Fail != 1 {
		t.Fatalf("unexpected account state after auto 404: %+v", accounts)
	}
	if token, err := newAccountPool(&s.cfg).pickTextToken(accounts, ""); err != nil || token != "good" {
		t.Fatalf("text pool selected %q after auto 404: %v", token, err)
	}
	mergeRefreshedAccount(&accounts[0], Account{Status: "正常", Type: "free", Quota: 5})
	if accounts[0].Status != "异常" {
		t.Fatal("metadata refresh revived an account with a confirmed conversation failure")
	}
}

func TestTextPoolPrefersLoginTokenWithoutDroppingSignupFallback(t *testing.T) {
	signup := "e30." + base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"is_signup":true}}`)) + ".sig"
	login := "e30." + base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{}}`)) + ".sig"
	if !isSignupAccessToken(signup) || isSignupAccessToken(login) || isSignupAccessToken("invalid") {
		t.Fatal("signup claim detection failed")
	}
	accounts := []Account{{AccessToken: signup, Type: "free", Status: "正常"}, {AccessToken: login, Type: "free", Status: "正常"}}
	pool := newAccountPool(&Config{})
	if token, err := pool.pickTextToken(accounts, "free"); err != nil || token != login {
		t.Fatalf("expected login token first, got %q: %v", token, err)
	}
	if token, err := pool.pickTextTokenExcluding(accounts, "free", map[string]bool{login: true}); err != nil || token != signup {
		t.Fatalf("expected signup token as fallback, got %q: %v", token, err)
	}
}

func TestImagePoolPrefersLoginTokenWithoutDroppingSignupFallback(t *testing.T) {
	signup := "e30." + base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"is_signup":true}}`)) + ".sig"
	login := "e30." + base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{}}`)) + ".sig"
	accounts := []Account{{AccessToken: signup, Type: "free", Status: "正常", Quota: 5}, {AccessToken: login, Type: "free", Status: "正常", Quota: 5}}
	pool := newAccountPool(&Config{})
	if token, err := pool.pickToken(accounts, false); err != nil || token != login {
		t.Fatalf("expected login token first, got %q: %v", token, err)
	}
	pool.releaseToken(login)
	if token, err := pool.pickTokenExcluding(accounts, false, "", map[string]bool{login: true}); err != nil || token != signup {
		t.Fatalf("expected signup token as fallback, got %q: %v", token, err)
	}
}

func TestLiveAutoConversationPrefersLoginToken(t *testing.T) {
	if os.Getenv("CHATGPT2API_LIVE_404_POOL") != "1" {
		t.Skip("set CHATGPT2API_LIVE_404_POOL=1 for upstream pool verification")
	}
	failingEmail := os.Getenv("CHATGPT2API_LIVE_404_ACCOUNT_EMAIL")
	workingEmail := os.Getenv("CHATGPT2API_LIVE_WORKING_ACCOUNT_EMAIL")
	if failingEmail == "" || workingEmail == "" {
		t.Skip("set CHATGPT2API_LIVE_404_ACCOUNT_EMAIL and CHATGPT2API_LIVE_WORKING_ACCOUNT_EMAIL")
	}
	configData, err := os.ReadFile("../../config.json")
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := json.Unmarshal(configData, &cfg); err != nil {
		t.Fatal(err)
	}
	accountsData, err := os.ReadFile("../../data/accounts.json")
	if err != nil {
		t.Fatal(err)
	}
	var all []Account
	if err := json.Unmarshal(accountsData, &all); err != nil {
		t.Fatal(err)
	}
	var selected []Account
	for _, email := range []string{failingEmail, workingEmail} {
		found := false
		for _, account := range all {
			if ptrString(account.Email) == email {
				selected = append(selected, account)
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("live test account %s was not found", email)
		}
	}
	if !isSignupAccessToken(selected[0].AccessToken) || isSignupAccessToken(selected[1].AccessToken) {
		t.Fatal("live test requires a signup token followed by a login token")
	}
	store := NewStore(t.TempDir())
	if err := store.SaveAccounts(selected); err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: cfg, store: store, accountPool: newAccountPool(&cfg)}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	events, errs := s.streamTextWithRetry(ctx, []map[string]any{{"role": "user", "content": "Reply with exactly: OK"}}, "auto", "", "")
	var content strings.Builder
	for event := range events {
		content.WriteString(event.Delta)
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToUpper(content.String()), "OK") {
		t.Fatalf("pool did not return the requested acknowledgement: response_len=%d", content.Len())
	}
	got := store.LoadAccounts()
	if got[0].Status != selected[0].Status || got[0].Fail != selected[0].Fail || got[1].Success != selected[1].Success+1 {
		t.Fatalf("unexpected pool states: first=%s second=%s", got[0].Status, got[1].Status)
	}
}
