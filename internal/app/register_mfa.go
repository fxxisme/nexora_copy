package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const registerMFABase = "https://chatgpt.com/backend-api/accounts/mfa"

func writePrivateJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".mfa-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if err := tmp.Chmod(0600); err != nil {
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (s *Store) updateAccountMFA(token, status, secret string) error {
	return s.UpdateAccounts(func(items []Account) []Account {
		for i := range items {
			if items[i].AccessToken == token {
				items[i].MFAStatus = status
				if secret != "" {
					items[i].MFASecret = &secret
				}
				break
			}
		}
		return items
	})
}

func registerMFAAccountID(accessToken string) string {
	parts := strings.Split(accessToken, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims map[string]any
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	auth, _ := claims["https://api.openai.com/auth"].(map[string]any)
	return strings.TrimSpace(strAny(auth["chatgpt_account_id"], ""))
}

func registerTOTP(secret string, now time.Time) (string, error) {
	secret = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(secret), " ", ""))
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.TrimRight(secret, "="))
	if err != nil || len(key) == 0 {
		return "", errors.New("invalid TOTP secret")
	}
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(now.Unix()/30))
	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write(counter[:])
	digest := mac.Sum(nil)
	offset := int(digest[len(digest)-1] & 0x0f)
	value := binary.BigEndian.Uint32(digest[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", value%1000000), nil
}

func freshRegisterTOTP(ctx context.Context, secret string) (string, time.Time, error) {
	now := time.Now()
	if remaining := 30 - now.Unix()%30; remaining < 6 {
		timer := time.NewTimer(time.Duration(remaining)*time.Second - time.Duration(now.Nanosecond()))
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return "", time.Time{}, ctx.Err()
		case <-timer.C:
		}
		now = time.Now()
	}
	code, err := registerTOTP(secret, now)
	return code, now, err
}

func (s *Server) handleAccountMFACode(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if !readBody(w, r, &body) {
		return
	}
	for _, account := range s.store.LoadAccounts() {
		if account.AccessToken != body.AccessToken {
			continue
		}
		if account.MFAStatus != "enabled" || account.MFASecret == nil {
			writeErr(w, http.StatusConflict, "2FA is not enabled")
			return
		}
		code, now, err := freshRegisterTOTP(r.Context(), *account.MFASecret)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "invalid stored 2FA secret")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"code": code, "expires_in": 30 - now.Unix()%30})
		return
	}
	writeErr(w, http.StatusNotFound, "account not found")
}

func (s *registerSession) registerMFARequest(ctx context.Context, method, path, accessToken, deviceID string, body any) (map[string]any, error) {
	headers := map[string]string{
		"Authorization": "Bearer " + accessToken,
		"Accept":        "*/*",
		"Origin":        "https://chatgpt.com",
		"Referer":       "https://chatgpt.com/",
		"Oai-Did":       deviceID,
		"Oai-Language":  s.registerAcceptLanguage(),
		"Content-Type":  "application/json",
	}
	if accountID := registerMFAAccountID(accessToken); accountID != "" {
		headers["Chatgpt-Account-Id"] = accountID
	}
	resp, err := s.do(ctx, method, registerMFABase+path, headers, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var result map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8192)).Decode(&result); err != nil {
		return nil, errors.New("invalid MFA response")
	}
	return result, nil
}

func (s *registerService) enableRegisterMFA(ctx context.Context, session *registerSession, token, deviceID string) error {
	store := s.server.store
	setStatus := func(status, secret string) error { return store.updateAccountMFA(token, status, secret) }
	enrollment, err := session.registerMFARequest(ctx, http.MethodPost, "/enroll", token, deviceID, map[string]string{"factor_type": "totp", "source": "settings"})
	if err != nil {
		_ = setStatus("failed", "")
		return fmt.Errorf("enroll: %w", err)
	}
	secret := strings.TrimSpace(strAny(enrollment["secret"], ""))
	sessionID := strings.TrimSpace(strAny(enrollment["session_id"], ""))
	if secret == "" || sessionID == "" {
		_ = setStatus("failed", "")
		return errors.New("enroll missing secret or session_id")
	}
	if err := setStatus("pending", secret); err != nil {
		return fmt.Errorf("save pending 2FA: %w", err)
	}
	code, _, err := freshRegisterTOTP(ctx, secret)
	if err != nil {
		return err
	}
	activation, err := session.registerMFARequest(ctx, http.MethodPost, "/user/activate_enrollment", token, deviceID, map[string]string{
		"code": code, "factor_type": "totp", "session_id": sessionID,
	})
	if err != nil {
		return fmt.Errorf("activate: %w", err)
	}
	if activation["success"] != true {
		return errors.New("activate returned success=false")
	}
	found := false
	if err := store.UpdateAccounts(func(items []Account) []Account {
		for i := range items {
			if items[i].AccessToken == token {
				items[i].MFAStatus = "enabled"
				items[i].MFASecret = &secret
				items[i].Status = "正常"
				found = true
				break
			}
		}
		return items
	}); err != nil {
		return fmt.Errorf("save enabled 2FA: %w", err)
	}
	if !found {
		return errors.New("save enabled 2FA: account not found")
	}
	return nil
}
