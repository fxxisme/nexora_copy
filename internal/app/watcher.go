package app

import (
	"context"
	"log"
	"strings"
	"time"
)

func (s *Server) startLimitedAccountWatcher() {
	interval := time.Duration(s.cfg.RefreshAccountIntervalMinute) * time.Minute
	if interval <= 0 {
		interval = 60 * time.Minute
	}
	go func() {
		for {
			time.Sleep(interval)
			accounts := s.store.LoadAccounts()
			var limited []Account
			var refreshCandidates []Account
			for _, a := range accounts {
				if a.AccessToken == "" {
					continue
				}
				if a.Status == "限流" {
					limited = append(limited, a)
				} else if a.Status == "正常" {
					refreshCandidates = append(refreshCandidates, a)
				} else if a.Status == "异常" && a.RefreshToken != nil && strings.TrimSpace(*a.RefreshToken) != "" {
					refreshCandidates = append(refreshCandidates, a)
				}
			}
			if len(limited) > 0 {
				log.Printf("[account-limited-watcher] checking %d limited accounts", len(limited))
				for _, observed := range limited {
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					release, err := s.acquireAccountRefreshSlot(ctx)
					if err != nil {
						cancel()
						continue
					}
					client, err := s.newUpstreamClientForAccount(observed)
					if err != nil {
						release()
						cancel()
						continue
					}
					info, err := client.GetUserInfo(ctx)
					release()
					cancel()
					if err != nil {
						continue
					}
					if err := s.applyLimitedAccountRefresh(observed, info); err != nil {
						log.Printf("[account-limited-watcher] update failed: %v", err)
					}
				}
			}
			if len(refreshCandidates) > 0 {
				log.Printf("[account-watcher] syncing %d accounts quota and token status", len(refreshCandidates))
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
				runAccountRefreshBatch(ctx, refreshCandidates, s.refreshAccountInfo)
				cancel()
			}
		}
	}()
}

func (s *Server) applyLimitedAccountRefresh(observed, info Account) error {
	return s.store.UpdateAccounts(func(current []Account) []Account {
		for i := range current {
			if current[i].AccessToken != observed.AccessToken || current[i].Status != "限流" {
				continue
			}
			if current[i].Fail != observed.Fail || ptrString(current[i].RateLimitedAt) != ptrString(observed.RateLimitedAt) {
				break
			}
			mergeRefreshedAccount(&current[i], info)
			break
		}
		return current
	})
}
