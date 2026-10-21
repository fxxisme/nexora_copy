package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestGeneratePKCE(t *testing.T) {
	verifier, challenge, err := generatePKCE()
	if err != nil {
		t.Fatalf("generatePKCE error: %v", err)
	}
	if len(verifier) == 0 {
		t.Fatal("verifier should not be empty")
	}
	if len(challenge) == 0 {
		t.Fatal("challenge should not be empty")
	}
	// Verify SHA256 matches
	hash := sha256.Sum256([]byte(verifier))
	expected := base64.RawURLEncoding.EncodeToString(hash[:])
	if challenge != expected {
		t.Fatalf("challenge mismatch: got %q, want %q", challenge, expected)
	}
}

func TestOAuthLoginStart(t *testing.T) {
	mgr := newOAuthLoginManager()
	res, err := mgr.start("test@example.com")
	if err != nil {
		t.Fatalf("start error: %v", err)
	}
	if res.SessionID == "" {
		t.Fatal("expected non-empty session_id")
	}
	if res.AuthorizeURL == "" {
		t.Fatal("expected non-empty authorize_url")
	}
	parsed, err := url.Parse(res.AuthorizeURL)
	if err != nil {
		t.Fatalf("parse authorize_url: %v", err)
	}
	q := parsed.Query()
	if q.Get("client_id") != openAIOAuthClientID {
		t.Fatalf("client_id = %q, want %q", q.Get("client_id"), openAIOAuthClientID)
	}
	if q.Get("login_hint") != "test@example.com" {
		t.Fatalf("login_hint = %q, want test@example.com", q.Get("login_hint"))
	}
	if q.Get("code_challenge_method") != "S256" {
		t.Fatalf("code_challenge_method = %q, want S256", q.Get("code_challenge_method"))
	}

	mgr.mu.Lock()
	sess, ok := mgr.sessions[res.SessionID]
	mgr.mu.Unlock()
	if !ok {
		t.Fatal("session not stored in manager")
	}
	if sess.codeVerifier == "" {
		t.Fatal("code_verifier should not be empty")
	}
}

func TestExtractCodeAndStateFromCallback(t *testing.T) {
	// Full callback URL
	callbackURL := "https://platform.openai.com/auth/callback?code=my-secret-code&state=sess123.nonce456"
	code, state, err := extractCodeAndStateFromCallback(callbackURL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != "my-secret-code" || state != "sess123.nonce456" {
		t.Fatalf("got code=%q, state=%q", code, state)
	}

	// Raw code
	rawCode := "my-raw-code-123"
	code, state, err = extractCodeAndStateFromCallback(rawCode)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != rawCode || state != "" {
		t.Fatalf("got code=%q, state=%q", code, state)
	}

	// Error callback
	errorURL := "https://platform.openai.com/auth/callback?error=access_denied&error_description=User+cancelled"
	_, _, err = extractCodeAndStateFromCallback(errorURL)
	if err == nil || !strings.Contains(err.Error(), "User cancelled") {
		t.Fatalf("expected User cancelled error, got %v", err)
	}
}

func TestOAuthLoginSessionValidationAndExpiration(t *testing.T) {
	mgr := newOAuthLoginManager()
	res, err := mgr.start("")
	if err != nil {
		t.Fatalf("start error: %v", err)
	}

	// Valid finish retrieval
	sess, matchedSID, err := mgr.getSessionForFinish(res.SessionID, "")
	if err != nil {
		t.Fatalf("unexpected finish error: %v", err)
	}
	if matchedSID != res.SessionID || sess.codeVerifier == "" {
		t.Fatalf("matchedSID = %q", matchedSID)
	}

	// State mismatch
	_, _, err = mgr.getSessionForFinish(res.SessionID, "different_session.nonce")
	if err == nil || !strings.Contains(err.Error(), "state 不匹配") {
		t.Fatalf("expected state mismatch error, got %v", err)
	}

	// Expired session
	mgr.mu.Lock()
	s := mgr.sessions[res.SessionID]
	s.createdAt = time.Now().Add(-11 * time.Minute)
	mgr.sessions[res.SessionID] = s
	mgr.mu.Unlock()

	_, _, err = mgr.getSessionForFinish(res.SessionID, "")
	if err == nil || !strings.Contains(err.Error(), "已过期或不存在") {
		t.Fatalf("expected expired session error, got %v", err)
	}
}

func TestHandleOAuthLoginFlow(t *testing.T) {
	tempDir := t.TempDir()
	store := NewStore(tempDir)
	s := &Server{
		store:      store,
		logSvc:     newLogService(tempDir),
		oauthLogin: newOAuthLoginManager(),
		cfg:        Config{AuthKey: "admin-secret"},
	}
	s.logSvc.store = store

	// 1. Test POST /api/accounts/oauth/start
	body := bytes.NewBufferString(`{"email_hint":"user@test.com"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/oauth/start", body)
	req.Header.Set("Authorization", "Bearer admin-secret")
	w := httptest.NewRecorder()
	s.handleOAuthLoginStart(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("start handler code = %d, body = %s", w.Code, w.Body.String())
	}
	var startRes oauthLoginStartResult
	if err := json.Unmarshal(w.Body.Bytes(), &startRes); err != nil {
		t.Fatalf("unmarshal start response: %v", err)
	}
	if startRes.SessionID == "" || !strings.Contains(startRes.AuthorizeURL, "user%40test.com") {
		t.Fatalf("startRes = %+v", startRes)
	}

	// 2. Mock OpenAI OAuth Token endpoint
	mgr := s.getOAuthLoginManager()
	mgr.mu.Lock()
	cachedSession, ok := mgr.sessions[startRes.SessionID]
	mgr.mu.Unlock()
	if !ok {
		t.Fatal("session not found")
	}

	// Verify getSessionForFinish with callback URL
	callbackURL := fmt.Sprintf("https://platform.openai.com/auth/callback?code=mock_code_123&state=%s", cachedSession.state)
	code, callbackState, err := extractCodeAndStateFromCallback(callbackURL)
	if err != nil {
		t.Fatal(err)
	}
	retrievedSess, pickedSID, err := mgr.getSessionForFinish(startRes.SessionID, callbackState)
	if err != nil {
		t.Fatal(err)
	}
	if pickedSID != startRes.SessionID || retrievedSess.codeVerifier != cachedSession.codeVerifier {
		t.Fatalf("session mismatch: %s vs %s", pickedSID, startRes.SessionID)
	}
}
