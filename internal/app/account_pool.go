package app

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
)

type refreshFlight struct {
	done    chan struct{}
	account Account
	err     error
}

type accountPool struct {
	mu             sync.Mutex
	index          int
	inflight       map[string]int
	refreshSlots   chan struct{}
	tokenAliases   map[string]string
	refreshFlights map[string]*refreshFlight
	cfg            *Config
}

func newAccountPool(cfg *Config) *accountPool {
	return &accountPool{
		inflight:       make(map[string]int),
		refreshSlots:   make(chan struct{}, 8),
		tokenAliases:   make(map[string]string),
		refreshFlights: make(map[string]*refreshFlight),
		cfg:            cfg,
	}
}

func (p *accountPool) recordAlias(oldToken, newToken string) {
	oldToken = strings.TrimSpace(oldToken)
	newToken = strings.TrimSpace(newToken)
	if oldToken == "" || newToken == "" || oldToken == newToken {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.tokenAliases == nil {
		p.tokenAliases = make(map[string]string)
	}
	p.tokenAliases[oldToken] = newToken
	for src, dst := range p.tokenAliases {
		if dst == oldToken {
			p.tokenAliases[src] = newToken
		}
	}
	if p.inflight[oldToken] > 0 {
		p.inflight[newToken] += p.inflight[oldToken]
		delete(p.inflight, oldToken)
	}
}

func (p *accountPool) resolveToken(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	cur := token
	for hop := 0; hop < 8; hop++ {
		next, ok := p.tokenAliases[cur]
		if !ok || next == "" || next == cur {
			break
		}
		cur = next
	}
	return cur
}

func (p *accountPool) acquireRefreshFlight(token string) (*refreshFlight, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.refreshFlights == nil {
		p.refreshFlights = make(map[string]*refreshFlight)
	}
	if f, ok := p.refreshFlights[token]; ok {
		return f, false
	}
	f := &refreshFlight{done: make(chan struct{})}
	p.refreshFlights[token] = f
	return f, true
}

func (p *accountPool) releaseRefreshFlight(token string, f *refreshFlight) {
	p.mu.Lock()
	defer p.mu.Unlock()
	close(f.done)
	delete(p.refreshFlights, token)
}

func ptrString(v *string) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(*v)
}

func isSignupAccessToken(token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var claims struct {
		Auth struct {
			IsSignup bool `json:"is_signup"`
		} `json:"https://api.openai.com/auth"`
	}
	return json.Unmarshal(payload, &claims) == nil && claims.Auth.IsSignup
}

func parseAccountTime(value string) (time.Time, error) {
	text := strings.TrimSpace(value)
	if text == "" {
		return time.Time{}, errors.New("empty time")
	}
	if t, err := time.Parse(time.RFC3339, text); err == nil {
		return t, nil
	}
	if strings.HasSuffix(text, "Z") {
		return time.Parse(time.RFC3339Nano, text)
	}
	return time.Parse(time.RFC3339Nano, text)
}

func (p *accountPool) pickToken(accounts []Account, needCodex bool) (string, error) {
	return p.pickTokenExcluding(accounts, needCodex, "", nil)
}

func (p *accountPool) pickTokenExcluding(accounts []Account, needCodex bool, codexPlanType string, excluded map[string]bool) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	maxConc := p.cfg.ImageAccountConcurrency
	if maxConc < 1 {
		maxConc = 3
	}
	premium := map[string]bool{"plus": true, "pro": true, "team": true}
	now := time.Now().UTC()
	for signupPass := 0; signupPass < 2; signupPass++ {
		var bestToken string
		var bestInflight int = 1 << 30
		var bestLastUsed time.Time
		var bestIdx int = -1
		found := false

		for offset := 0; offset < len(accounts); offset++ {
			idx := (p.index + offset) % len(accounts)
			a := accounts[idx]
			if a.AccessToken == "" || excluded[a.AccessToken] || isSignupAccessToken(a.AccessToken) != (signupPass == 1) {
				continue
			}
			if a.Status == "禁用" || a.Status == "异常" {
				continue
			}
			if a.Status == "限流" {
				resetAt := firstNonEmpty(ptrString(a.RateLimitResetAt), ptrString(a.RestoreAt))
				if resetAt != "" {
					rt, err := parseAccountTime(resetAt)
					if err != nil || now.Before(rt) {
						continue
					}
				} else {
					continue
				}
			}
			if !a.ImageQuotaUnknown && a.Quota <= 0 {
				continue
			}
			if needCodex {
				accountType := strings.ToLower(a.Type)
				if strings.ToLower(a.SourceType) != "codex" {
					continue
				}
				if codexPlanType != "" {
					if accountType != codexPlanType {
						continue
					}
				} else if !premium[accountType] {
					continue
				}
			}
			// 检查并发槽
			curInflight := p.inflight[a.AccessToken]
			if curInflight >= maxConc {
				continue
			}
			var lastUsed time.Time
			if a.LastUsedAt != nil {
				lastUsed, _ = parseAccountTime(*a.LastUsedAt)
			}
			// 最少并发优先 + 最久未使用优先打散
			if !found || curInflight < bestInflight || (curInflight == bestInflight && lastUsed.Before(bestLastUsed)) {
				bestToken = a.AccessToken
				bestInflight = curInflight
				bestLastUsed = lastUsed
				bestIdx = idx
				found = true
			}
		}
		if found {
			p.index = (bestIdx + 1) % len(accounts)
			p.inflight[bestToken]++
			return bestToken, nil
		}
	}
	if needCodex {
		return "", errors.New("no available codex Plus/Team/Pro account")
	}
	return "", errors.New("no available image quota")
}

func (p *accountPool) releaseToken(token string) {
	token = strings.TrimSpace(token)
	if token == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	cur := token
	for hop := 0; hop < 8; hop++ {
		next, ok := p.tokenAliases[cur]
		if !ok || next == "" || next == cur {
			break
		}
		cur = next
	}
	if v := p.inflight[cur]; v <= 1 {
		delete(p.inflight, cur)
	} else {
		p.inflight[cur] = v - 1
	}
}

func (p *accountPool) pickTextToken(accounts []Account, planType string) (string, error) {
	return p.pickTextTokenExcluding(accounts, planType, nil)
}

func (p *accountPool) pickTextTokenExcluding(accounts []Account, planType string, excluded map[string]bool) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	maxConc := p.cfg.ImageAccountConcurrency
	if maxConc < 1 {
		maxConc = 3
	}
	premium := map[string]bool{"plus": true, "pro": true, "team": true}
	now := time.Now().UTC()
	for signupPass := 0; signupPass < 2; signupPass++ {
		var bestToken string
		var bestInflight int = 1 << 30
		var bestLastUsed time.Time
		var bestIdx int = -1
		found := false

		for offset := 0; offset < len(accounts); offset++ {
			idx := (p.index + offset) % len(accounts)
			a := accounts[idx]
			if a.AccessToken == "" || excluded[a.AccessToken] || a.Status == "禁用" || a.Status == "异常" || isSignupAccessToken(a.AccessToken) != (signupPass == 1) {
				continue
			}
			if a.Status == "限流" {
				resetAt := firstNonEmpty(ptrString(a.RateLimitResetAt), ptrString(a.RestoreAt))
				if resetAt != "" {
					rt, err := parseAccountTime(resetAt)
					if err != nil || now.Before(rt) {
						continue
					}
				} else {
					continue
				}
			}
			lower := strings.ToLower(a.Type)
			if planType == "free" {
				if premium[lower] {
					continue
				}
			} else if planType != "" {
				if lower != planType && (!premium[lower] || lower != planType) {
					continue
				}
			}
			curInflight := p.inflight[a.AccessToken]
			if curInflight >= maxConc {
				continue
			}
			var lastUsed time.Time
			if a.LastUsedAt != nil {
				lastUsed, _ = parseAccountTime(*a.LastUsedAt)
			}
			if !found || curInflight < bestInflight || (curInflight == bestInflight && lastUsed.Before(bestLastUsed)) {
				bestToken = a.AccessToken
				bestInflight = curInflight
				bestLastUsed = lastUsed
				bestIdx = idx
				found = true
			}
		}
		if found {
			p.index = (bestIdx + 1) % len(accounts)
			p.inflight[bestToken]++
			return bestToken, nil
		}
	}
	return "", errors.New("no available text account")
}

func (s *Server) pickToken(model, resolution string) (string, error) {
	return s.pickTokenExcluding(model, resolution, nil)
}

func (s *Server) pickTokenExcluding(model, resolution string, excluded map[string]bool) (string, error) {
	account, err := s.pickAccountExcluding(model, resolution, excluded)
	return account.AccessToken, err
}

func (s *Server) pickAccountExcluding(model, resolution string, excluded map[string]bool) (Account, error) {
	accounts := s.store.LoadAccounts()
	needCodex := isCodexImageRequest(model, resolution)
	token, err := s.accountPool.pickTokenExcluding(accounts, needCodex, codexPlanTypeFromModel(model), excluded)
	if err != nil {
		return Account{}, err
	}
	resolved := s.accountPool.resolveToken(token)
	for _, account := range accounts {
		if account.AccessToken == resolved || account.AccessToken == token {
			return account, nil
		}
	}
	return Account{AccessToken: resolved}, nil
}

func (s *Server) pickTextToken() (string, error) {
	return s.pickTextTokenExcluding(nil, "")
}

func (s *Server) pickTextTokenExcluding(excluded map[string]bool, planType string) (string, error) {
	account, err := s.pickTextAccountExcluding(excluded, planType)
	return account.AccessToken, err
}

func (s *Server) pickTextAccountExcluding(excluded map[string]bool, planType string) (Account, error) {
	accounts := s.store.LoadAccounts()
	token, err := s.accountPool.pickTextTokenExcluding(accounts, planType, excluded)
	if err != nil {
		return Account{}, err
	}
	resolved := s.accountPool.resolveToken(token)
	for _, account := range accounts {
		if account.AccessToken == resolved || account.AccessToken == token {
			return account, nil
		}
	}
	return Account{AccessToken: resolved}, nil
}
