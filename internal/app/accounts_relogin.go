package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

func (s *Server) reLoginAccountWithPassword(ctx context.Context, old Account) (Account, error) {
	email := ptrString(old.Email)
	password := ptrString(old.Password)
	if email == "" || password == "" {
		return Account{}, errors.New("账号缺少邮箱或密码")
	}
	proxyURL, err := s.selectedProxyForAccount(old)
	if err != nil {
		return Account{}, err
	}
	session, err := newRegisterSession(proxyURL, 45, s.ensureCurlImpersonateBinary)
	if err != nil {
		return Account{}, err
	}
	defer session.close()
	tokens, err := session.loginWithPassword(ctx, email, password)
	if err != nil {
		return Account{}, err
	}
	accessToken := strings.TrimSpace(strAny(tokens["access_token"], ""))
	if accessToken == "" {
		return Account{}, errors.New("重新授权未返回 access_token")
	}
	refreshToken := strings.TrimSpace(strAny(tokens["refresh_token"], ""))
	idToken := strings.TrimSpace(strAny(tokens["id_token"], ""))
	now := nowISO()
	next := old
	next.AccessToken = accessToken
	next.Status = "正常"
	next.SourceType = firstNonEmpty(strings.TrimSpace(next.SourceType), "password")
	next.RateLimitedAt = nil
	next.RateLimitResetAt = nil
	next.RestoreAt = nil
	next.LastUsedAt = &now
	if refreshToken != "" {
		next.RefreshToken = &refreshToken
	}
	if idToken != "" {
		next.IDToken = &idToken
	}
	if expiresIn := intAny(tokens["expires_in"], 0); expiresIn > 0 {
		next.ExpiresAt = time.Now().Unix() + int64(expiresIn)
	}
	if accountID := chatGPTAccountID(accessToken); accountID != "" {
		next.AccountID = &accountID
	}
	return next, nil
}

func (s *Server) reLoginAccountWithEmailCode(ctx context.Context, old Account) (Account, error) {
	email := ptrString(old.Email)
	emailPassword := ptrString(old.EmailPassword)
	if emailPassword == "" && old.Mailbox != nil {
		emailPassword = strings.TrimSpace(strAny(old.Mailbox["password"], ""))
	}
	if email == "" || emailPassword == "" {
		return Account{}, errors.New("账号缺少邮箱或邮箱密码，无法邮箱验证码登录")
	}
	proxyURL, err := s.selectedProxyForAccount(old)
	if err != nil {
		return Account{}, err
	}
	session, err := newRegisterSession(proxyURL, 60, s.ensureCurlImpersonateBinary)
	if err != nil {
		return Account{}, err
	}
	defer session.close()
	deviceID := uuid4()
	codeVerifier, err := session.platformAuthorizeWithHint(ctx, deviceID, email, "login")
	if err != nil {
		codeVerifier, err = session.platformAuthorizeWithHint(ctx, deviceID, email, "login_or_signup")
		if err != nil {
			return Account{}, err
		}
	}
	codeNotBefore := time.Now().UTC()
	if err := session.sendOTP(ctx, deviceID); err != nil {
		return Account{}, err
	}
	code, err := s.waitAccountEmailCode(ctx, session, old, email, emailPassword, codeNotBefore)
	if err != nil {
		return Account{}, err
	}
	continueURL, err := session.validateOTPContinueURL(ctx, deviceID, code)
	if err != nil {
		return Account{}, err
	}
	oauthCode := parseContinueCode(continueURL)
	if oauthCode == "" {
		return Account{}, errors.New("邮箱验证码已校验，但未返回 oauth callback code")
	}
	tokens, err := session.exchangeTokens(ctx, codeVerifier, oauthCode)
	if err != nil {
		return Account{}, err
	}
	accessToken := strings.TrimSpace(strAny(tokens["access_token"], ""))
	if accessToken == "" {
		return Account{}, errors.New("邮箱验证码登录未返回 access_token")
	}
	refreshToken := strings.TrimSpace(strAny(tokens["refresh_token"], ""))
	idToken := strings.TrimSpace(strAny(tokens["id_token"], ""))
	now := nowISO()
	next := old
	next.AccessToken = accessToken
	next.Status = "正常"
	next.SourceType = firstNonEmpty(strings.TrimSpace(next.SourceType), "email_code")
	next.RateLimitedAt = nil
	next.RateLimitResetAt = nil
	next.RestoreAt = nil
	next.LastUsedAt = &now
	if refreshToken != "" {
		next.RefreshToken = &refreshToken
	}
	if idToken != "" {
		next.IDToken = &idToken
	}
	if expiresIn := intAny(tokens["expires_in"], 0); expiresIn > 0 {
		next.ExpiresAt = time.Now().Unix() + int64(expiresIn)
	}
	if accountID := chatGPTAccountID(accessToken); accountID != "" {
		next.AccountID = &accountID
	}
	return next, nil
}

func (s *Server) waitAccountEmailCode(ctx context.Context, session *registerSession, account Account, email, emailPassword string, codeNotBefore time.Time) (string, error) {
	if account.Mailbox != nil {
		clientID := strings.TrimSpace(strAny(account.Mailbox["client_id"], ""))
		refreshToken := strings.TrimSpace(strAny(account.Mailbox["refresh_token"], ""))
		provider := strings.ToLower(strings.TrimSpace(strAny(account.Mailbox["provider"], "")))
		if clientID != "" && refreshToken != "" && (provider == "" || strings.Contains(provider, "outlook")) {
			p := &outlookTokenRegisterProvider{
				registerProviderBase: registerProviderBase{sess: session},
				entry:                map[string]any{"wait_timeout": 90, "wait_interval": 3},
				statePath:            s.store.path("outlook_token_used.json"),
				store:                s.store,
			}
			return p.WaitForCode(ctx, &registerMailbox{
				Provider:      "outlook_token",
				Address:       email,
				Token:         refreshToken,
				ClientID:      clientID,
				Password:      emailPassword,
				CodeNotBefore: codeNotBefore,
			})
		}
	}
	loginEmail := email
	imapHost := ""
	if account.Mailbox != nil {
		loginEmail = firstNonEmpty(
			strAny(account.Mailbox["login_email"], ""),
			strAny(account.Mailbox["mailbox_email"], ""),
			strAny(account.Mailbox["mailbox_login_email"], ""),
			email,
		)
		imapHost = strAny(account.Mailbox["imap_host"], "")
	}
	return waitForIMAPCodeWithLogin(ctx, email, loginEmail, emailPassword, imapHost, codeNotBefore, 90, 3)
}

func (s *Server) reLoginAccounts(ctx context.Context, tokens []string) (int, int, []map[string]any, []map[string]any) {
	want := map[string]bool{}
	for _, token := range tokens {
		if strings.TrimSpace(token) != "" {
			want[strings.TrimSpace(token)] = true
		}
	}
	accounts := s.store.LoadAccounts()
	relogined := 0
	skipped := 0
	skippedItems := []map[string]any{}
	errs := []map[string]any{}
	for i := range accounts {
		if len(want) > 0 && !want[accounts[i].AccessToken] {
			continue
		}
		if ptrString(accounts[i].Password) == "" {
			var refreshErr error
			if ptrString(accounts[i].RefreshToken) != "" {
				itemCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
				next, err := s.refreshOAuthAccount(itemCtx, accounts[i].AccessToken)
				cancel()
				if err != nil {
					refreshErr = err
				} else {
					accounts[i] = next
					relogined++
					continue
				}
			}
			if ptrString(accounts[i].Email) != "" && (ptrString(accounts[i].EmailPassword) != "" || (accounts[i].Mailbox != nil && strings.TrimSpace(strAny(accounts[i].Mailbox["password"], "")) != "")) {
				itemCtx, cancel := context.WithTimeout(ctx, 180*time.Second)
				next, err := s.reLoginAccountWithEmailCode(itemCtx, accounts[i])
				cancel()
				if err != nil {
					if refreshErr != nil {
						err = fmt.Errorf("%s；邮箱验证码登录失败: %w", refreshErr.Error(), err)
					}
					if saveErr := s.recordReLoginFailure(accounts[i].AccessToken, err); saveErr != nil {
						err = fmt.Errorf("%w；保存账号状态失败: %v", err, saveErr)
					}
					errs = append(errs, map[string]any{"token": accounts[i].AccessToken, "email": ptrString(accounts[i].Email), "error": err.Error()})
					continue
				}
				if err := s.persistReLoginAccount(accounts[i].AccessToken, next); err != nil {
					errs = append(errs, map[string]any{"token": accounts[i].AccessToken, "email": ptrString(accounts[i].Email), "error": err.Error()})
					continue
				}
				relogined++
				continue
			}
			if refreshErr != nil {
				if saveErr := s.recordReLoginFailure(accounts[i].AccessToken, refreshErr); saveErr != nil {
					refreshErr = fmt.Errorf("%w；保存账号状态失败: %v", refreshErr, saveErr)
				}
				errs = append(errs, map[string]any{"token": accounts[i].AccessToken, "email": ptrString(accounts[i].Email), "error": refreshErr.Error()})
				continue
			}
			skipped++
			skippedItems = append(skippedItems, map[string]any{
				"token":  accounts[i].AccessToken,
				"email":  ptrString(accounts[i].Email),
				"reason": "账号缺少 OpenAI 登录密码、refresh_token 或邮箱密码",
			})
			continue
		}
		if ptrString(accounts[i].Email) == "" {
			skipped++
			skippedItems = append(skippedItems, map[string]any{
				"token":  accounts[i].AccessToken,
				"email":  ptrString(accounts[i].Email),
				"reason": "账号缺少邮箱，无法密码登录",
			})
			continue
		}
		itemCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		next, err := s.reLoginAccountWithPassword(itemCtx, accounts[i])
		cancel()
		if err != nil {
			if saveErr := s.recordReLoginFailure(accounts[i].AccessToken, err); saveErr != nil {
				err = fmt.Errorf("%w；保存账号状态失败: %v", err, saveErr)
			}
			errs = append(errs, map[string]any{"token": accounts[i].AccessToken, "email": ptrString(accounts[i].Email), "error": err.Error()})
			continue
		}
		if err := s.persistReLoginAccount(accounts[i].AccessToken, next); err != nil {
			errs = append(errs, map[string]any{"token": accounts[i].AccessToken, "email": ptrString(accounts[i].Email), "error": err.Error()})
			continue
		}
		relogined++
	}
	return relogined, skipped, errs, skippedItems
}

func (s *Server) recordReLoginFailure(token string, failure error) error {
	return s.store.UpdateAccounts(func(accounts []Account) []Account {
		for i := range accounts {
			if accounts[i].AccessToken == token {
				applyAccountFailureState(&accounts[i], failure, nowISO())
				break
			}
		}
		return accounts
	})
}

func (s *Server) persistReLoginAccount(oldToken string, next Account) error {
	updated := false
	err := s.store.UpdateAccounts(func(accounts []Account) []Account {
		for i := range accounts {
			if accounts[i].AccessToken != oldToken {
				continue
			}
			current := accounts[i]
			current.AccessToken = next.AccessToken
			current.Status = next.Status
			current.SourceType = next.SourceType
			current.RateLimitedAt = next.RateLimitedAt
			current.RateLimitResetAt = next.RateLimitResetAt
			current.RestoreAt = next.RestoreAt
			current.LastUsedAt = next.LastUsedAt
			current.RefreshToken = next.RefreshToken
			current.IDToken = next.IDToken
			current.ExpiresAt = next.ExpiresAt
			current.AccountID = next.AccountID
			accounts[i] = current
			updated = true
			break
		}
		return accounts
	})
	if err != nil {
		return err
	}
	if !updated {
		return errors.New("账号已被删除或令牌已变化，重新授权结果未保存")
	}
	return nil
}
