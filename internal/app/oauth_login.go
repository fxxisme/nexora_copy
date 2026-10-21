package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	stdhttp "net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	http "github.com/bogdanfinn/fhttp"
	"github.com/google/uuid"
)

const (
	openAIAuthBase         = "https://auth.openai.com"
	openAIPlatformBase     = "https://platform.openai.com"
	openAIOAuthClientID    = "app_2SKx67EdpoN0G6j64rFvigXD"
	openAIOAuthRedirectURI = "https://platform.openai.com/auth/callback"
	openAIOAuthAudience    = "https://api.openai.com/v1"
	openAIAuth0Client      = "eyJuYW1lIjoiYXV0aDAtc3BhLWpzIiwidmVyc2lvbiI6IjEuMjEuMCJ9"
	oauthLoginSessionTTL   = 10 * time.Minute
	maxOAuthLoginSessions  = 64
)

type oauthLoginSession struct {
	codeVerifier string
	state        string
	createdAt    time.Time
	redirectURI  string
}

type oauthLoginManager struct {
	mu       sync.Mutex
	sessions map[string]oauthLoginSession
}

func newOAuthLoginManager() *oauthLoginManager {
	return &oauthLoginManager{
		sessions: make(map[string]oauthLoginSession),
	}
}

func generatePKCE() (verifier, challenge string, err error) {
	b := make([]byte, 64)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("generate pkce: %w", err)
	}
	verifier = base64.RawURLEncoding.EncodeToString(b)
	hash := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(hash[:])
	return verifier, challenge, nil
}

func (m *oauthLoginManager) purgeExpiredLocked() {
	now := time.Now()
	for sid, sess := range m.sessions {
		if now.Sub(sess.createdAt) > oauthLoginSessionTTL {
			delete(m.sessions, sid)
		}
	}
	if len(m.sessions) > maxOAuthLoginSessions {
		type entry struct {
			sid       string
			createdAt time.Time
		}
		entries := make([]entry, 0, len(m.sessions))
		for sid, sess := range m.sessions {
			entries = append(entries, entry{sid: sid, createdAt: sess.createdAt})
		}
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].createdAt.Before(entries[j].createdAt)
		})
		toRemove := len(m.sessions) - maxOAuthLoginSessions
		for i := 0; i < toRemove && i < len(entries); i++ {
			delete(m.sessions, entries[i].sid)
		}
	}
}

type oauthLoginStartResult struct {
	SessionID         string `json:"session_id"`
	AuthorizeURL      string `json:"authorize_url"`
	ExpiresIn         string `json:"expires_in"`
	RedirectURIPrefix string `json:"redirect_uri_prefix"`
}

func (m *oauthLoginManager) start(emailHint string) (*oauthLoginStartResult, error) {
	verifier, challenge, err := generatePKCE()
	if err != nil {
		return nil, err
	}
	nonceBytes := make([]byte, 32)
	if _, err := rand.Read(nonceBytes); err != nil {
		return nil, err
	}
	nonce := base64.RawURLEncoding.EncodeToString(nonceBytes)

	stateBytes := make([]byte, 16)
	if _, err := rand.Read(stateBytes); err != nil {
		return nil, err
	}
	stateSuffix := base64.RawURLEncoding.EncodeToString(stateBytes)

	deviceID := uuid.NewString()
	sessionID := strings.ReplaceAll(uuid.NewString(), "-", "")
	state := fmt.Sprintf("%s.%s", sessionID, stateSuffix)

	vals := url.Values{}
	vals.Set("issuer", openAIAuthBase)
	vals.Set("client_id", openAIOAuthClientID)
	vals.Set("audience", openAIOAuthAudience)
	vals.Set("redirect_uri", openAIOAuthRedirectURI)
	vals.Set("device_id", deviceID)
	vals.Set("screen_hint", "login_or_signup")
	vals.Set("max_age", "0")
	vals.Set("scope", "openid profile email offline_access")
	vals.Set("response_type", "code")
	vals.Set("response_mode", "query")
	vals.Set("state", state)
	vals.Set("nonce", nonce)
	vals.Set("code_challenge", challenge)
	vals.Set("code_challenge_method", "S256")
	vals.Set("auth0Client", openAIAuth0Client)
	emailHint = strings.TrimSpace(emailHint)
	if emailHint != "" {
		vals.Set("login_hint", emailHint)
	}

	authorizeURL := fmt.Sprintf("%s/api/accounts/authorize?%s", openAIAuthBase, vals.Encode())

	m.mu.Lock()
	m.purgeExpiredLocked()
	m.sessions[sessionID] = oauthLoginSession{
		codeVerifier: verifier,
		state:        state,
		createdAt:    time.Now(),
		redirectURI:  openAIOAuthRedirectURI,
	}
	m.mu.Unlock()

	return &oauthLoginStartResult{
		SessionID:         sessionID,
		AuthorizeURL:      authorizeURL,
		ExpiresIn:         fmt.Sprintf("%d", int(oauthLoginSessionTTL.Seconds())),
		RedirectURIPrefix: openAIOAuthRedirectURI,
	}, nil
}

func extractCodeAndStateFromCallback(value string) (string, string, error) {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return "", "", errors.New("缺少 code 或 callback URL")
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		u, err := url.Parse(raw)
		if err != nil {
			return "", "", fmt.Errorf("无法解析 callback URL: %w", err)
		}
		q := u.Query()
		code := strings.TrimSpace(q.Get("code"))
		state := strings.TrimSpace(q.Get("state"))
		if code == "" {
			errDesc := strings.TrimSpace(q.Get("error_description"))
			if errDesc == "" {
				errDesc = strings.TrimSpace(q.Get("error"))
			}
			if errDesc != "" {
				return "", "", errors.New(errDesc)
			}
			return "", "", errors.New("callback URL 中没有 code 参数")
		}
		return code, state, nil
	}
	return raw, "", nil
}

func (m *oauthLoginManager) getSessionForFinish(bodySID, callbackState string) (oauthLoginSession, string, error) {
	var stateSID string
	if callbackState != "" {
		if parts := strings.SplitN(callbackState, ".", 2); len(parts) > 0 {
			stateSID = parts[0]
		}
	}
	var candidateSIDs []string
	if stateSID != "" {
		candidateSIDs = append(candidateSIDs, stateSID)
	}
	if bodySID != "" && bodySID != stateSID {
		candidateSIDs = append(candidateSIDs, bodySID)
	}
	if len(candidateSIDs) == 0 {
		return oauthLoginSession{}, "", errors.New("既未提供 session_id，callback URL 中也未携带 state")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.purgeExpiredLocked()

	var matchedSession oauthLoginSession
	var matchedSID string
	var found bool
	for _, sid := range candidateSIDs {
		if sess, ok := m.sessions[sid]; ok {
			matchedSession = sess
			matchedSID = sid
			found = true
			break
		}
	}
	if !found {
		return oauthLoginSession{}, "", errors.New("OAuth 会话已过期或不存在，请回到导入对话框点\"重新生成\"再走一次")
	}

	if callbackState != "" && matchedSession.state != "" && callbackState != matchedSession.state {
		return oauthLoginSession{}, "", errors.New("state 不匹配。常见原因：你点过两次\"打开授权页面\"，但浏览器里登录的还是前一次的窗口。请点\"重新生成\"重来。")
	}

	return matchedSession, matchedSID, nil
}

func (m *oauthLoginManager) removeSession(sid string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, sid)
}

type oauthExchangeTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int    `json:"expires_in"`
}

func (s *Server) exchangeOAuthCode(ctx context.Context, code, verifier, redirectURI string) (*oauthExchangeTokenResponse, error) {
	client, err := s.newUpstreamClientForAccount(Account{})
	if err != nil {
		return nil, fmt.Errorf("创建上游请求客户端失败: %w", err)
	}

	reqBody := map[string]string{
		"client_id":     openAIOAuthClientID,
		"code_verifier": verifier,
		"grant_type":    "authorization_code",
		"code":          code,
		"redirect_uri":  redirectURI,
	}
	b, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	tokenEndpoint := fmt.Sprintf("%s/api/accounts/oauth/token", openAIAuthBase)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", openAIPlatformBase)
	req.Header.Set("Referer", fmt.Sprintf("%s/", openAIPlatformBase))
	req.Header.Set("auth0-client", openAIAuth0Client)
	if client.userAgent != "" {
		req.Header.Set("User-Agent", client.userAgent)
	}
	if client.secCHUA != "" {
		req.Header.Set("sec-ch-ua", client.secCHUA)
	}

	resp, err := client.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("换 token 网络异常: %w", err)
	}
	defer resp.Body.Close()

	var payload map[string]any
	respBytes, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(respBytes, &payload)

	if resp.StatusCode != stdhttp.StatusOK {
		detail := ""
		if payload != nil {
			detail = strAny(payload["error_description"], strAny(payload["error"], strAny(payload["message"], "")))
		}
		if detail == "" && len(respBytes) > 0 {
			if len(respBytes) > 300 {
				detail = string(respBytes[:300])
			} else {
				detail = string(respBytes)
			}
		}
		errSuffix := ""
		if detail != "" {
			errSuffix = ": " + detail
		}
		return nil, fmt.Errorf("OpenAI 拒绝换 token (HTTP %d)%s", resp.StatusCode, errSuffix)
	}

	accessToken := strings.TrimSpace(strAny(payload["access_token"], ""))
	refreshToken := strings.TrimSpace(strAny(payload["refresh_token"], ""))
	idToken := strings.TrimSpace(strAny(payload["id_token"], ""))
	expiresIn := intAny(payload["expires_in"], 0)

	if accessToken == "" {
		return nil, errors.New("OpenAI 返回的 access_token 为空")
	}
	if refreshToken == "" {
		return nil, errors.New("OpenAI 没有返回 refresh_token（可能 scope 未包含 offline_access 或 code 已使用过）")
	}

	return &oauthExchangeTokenResponse{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		IDToken:      idToken,
		ExpiresIn:    expiresIn,
	}, nil
}

func (s *Server) getOAuthLoginManager() *oauthLoginManager {
	s.accMu.Lock()
	defer s.accMu.Unlock()
	if s.oauthLogin == nil {
		s.oauthLogin = newOAuthLoginManager()
	}
	return s.oauthLogin
}

func (s *Server) handleOAuthLoginStart(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if r.Method != stdhttp.MethodPost {
		writeErr(w, stdhttp.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		EmailHint string `json:"email_hint"`
	}
	_ = readBody(w, r, &body)

	mgr := s.getOAuthLoginManager()
	res, err := mgr.start(body.EmailHint)
	if err != nil {
		writeErr(w, stdhttp.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, stdhttp.StatusOK, res)
}

func (s *Server) handleOAuthLoginFinish(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if r.Method != stdhttp.MethodPost {
		writeErr(w, stdhttp.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		SessionID string `json:"session_id"`
		Callback  string `json:"callback"`
	}
	if !readBody(w, r, &body) {
		return
	}

	body.SessionID = strings.TrimSpace(body.SessionID)
	body.Callback = strings.TrimSpace(body.Callback)
	if body.Callback == "" {
		writeErr(w, stdhttp.StatusBadRequest, "缺少 callback 或 code")
		return
	}

	code, callbackState, err := extractCodeAndStateFromCallback(body.Callback)
	if err != nil {
		writeErr(w, stdhttp.StatusBadRequest, err.Error())
		return
	}

	mgr := s.getOAuthLoginManager()
	sess, matchedSID, err := mgr.getSessionForFinish(body.SessionID, callbackState)
	if err != nil {
		writeErr(w, stdhttp.StatusBadRequest, err.Error())
		return
	}

	tokens, err := s.exchangeOAuthCode(r.Context(), code, sess.codeVerifier, sess.redirectURI)
	if err != nil {
		writeErr(w, stdhttp.StatusBadRequest, err.Error())
		return
	}
	mgr.removeSession(matchedSID)

	now := nowISO()
	clientID := openAIOAuthClientID
	sourceType := "oauth_login"
	account := Account{
		AccessToken:  tokens.AccessToken,
		RefreshToken: &tokens.RefreshToken,
		IDToken:      &tokens.IDToken,
		SourceType:   sourceType,
		ClientID:     &clientID,
		Type:         "free",
		Status:       "正常",
		CreatedAt:    &now,
	}
	if tokens.ExpiresIn > 0 {
		account.ExpiresAt = time.Now().Unix() + int64(tokens.ExpiresIn)
	}

	added, skipped := 0, 0
	if err := s.store.UpdateAccounts(func(accounts []Account) []Account {
		idx := -1
		for i, a := range accounts {
			if a.AccessToken == tokens.AccessToken {
				idx = i
				break
			}
		}
		if idx >= 0 {
			mergeImportedAccount(&accounts[idx], account, map[string]any{
				"access_token":  tokens.AccessToken,
				"refresh_token": tokens.RefreshToken,
				"id_token":      tokens.IDToken,
				"source_type":   sourceType,
				"client_id":     clientID,
			}, true)
			skipped++
		} else {
			accounts = append(accounts, account)
			added++
		}
		return accounts
	}); err != nil {
		writeErr(w, stdhttp.StatusInternalServerError, "保存账号失败: "+err.Error())
		return
	}

	refreshed, errs := s.refreshAccountInfos(r.Context(), []string{tokens.AccessToken})
	accounts := s.store.LoadAccounts()
	if s.logSvc != nil {
		s.logSvc.add("account", "OAuth 导入账号", map[string]any{
			"added":     added,
			"skipped":   skipped,
			"refreshed": refreshed,
		})
	}

	writeJSON(w, stdhttp.StatusOK, map[string]any{
		"added":     added,
		"skipped":   skipped,
		"refreshed": refreshed,
		"errors":    errs,
		"items":     accounts,
	})
}
