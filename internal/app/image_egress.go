package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

func (s *Server) acquireImageProxyRoute(ctx context.Context, selected proxyRoute) (proxyRoute, func(), error) {
	return s.acquireImageProxyRouteExcluding(ctx, selected, nil)
}

func (s *Server) acquireImageProxyRouteExcluding(ctx context.Context, selected proxyRoute, excluded map[string]bool) (proxyRoute, func(), error) {
	noop := func() {}
	if selected.NodeID == "" || s.store == nil {
		return selected, noop, nil
	}
	for {
		if err := ctx.Err(); err != nil {
			return proxyRoute{}, nil, &proxyConnectionError{cause: err}
		}
		groups, err := s.store.LoadProxyGroups()
		if err != nil {
			return proxyRoute{}, nil, &proxyConnectionError{cause: err}
		}
		var candidates []ProxyNode
		for _, group := range groups {
			for _, node := range group.Nodes {
				if node.ID != selected.NodeID {
					continue
				}
				if !group.Enabled {
					return proxyRoute{}, nil, &proxyConnectionError{cause: errors.New("代理组已禁用")}
				}
				if node.Enabled && !excluded[node.URL] && !recentlyUnhealthyProxyNode(node, time.Now()) {
					candidates = append(candidates, node)
				}
				for _, sibling := range group.Nodes {
					if sibling.ID != node.ID && sibling.Enabled && !excluded[sibling.URL] && !recentlyUnhealthyProxyNode(sibling, time.Now()) {
						candidates = append(candidates, sibling)
					}
				}
				break
			}
			if candidates != nil {
				break
			}
		}
		if len(candidates) == 0 {
			return proxyRoute{}, nil, &proxyConnectionError{cause: errors.New("代理组没有可用的生图节点")}
		}
		s.imageEgressMu.Lock()
		if s.imageEgressInflight == nil {
			s.imageEgressInflight = make(map[string]int)
		}
		if s.imageEgressNotify == nil {
			s.imageEgressNotify = make(chan struct{})
		}
		for _, node := range candidates {
			key := imageProxyRouteKey(node.ID, node.URL)
			if node.ImageConcurrencyLimit > 0 && s.imageEgressInflight[key] >= node.ImageConcurrencyLimit {
				continue
			}
			route := proxyRoute{URL: node.URL, NodeID: node.ID, Bound: selected.Bound}
			if node.ImageConcurrencyLimit <= 0 {
				s.imageEgressMu.Unlock()
				return route, noop, nil
			}
			s.imageEgressInflight[key]++
			s.imageEgressMu.Unlock()
			var once sync.Once
			return route, func() { once.Do(func() { s.releaseImageProxyRoute(key) }) }, nil
		}
		notify := s.imageEgressNotify
		s.imageEgressMu.Unlock()
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return proxyRoute{}, nil, &proxyConnectionError{cause: ctx.Err()}
		case <-notify:
		case <-timer.C:
		}
		timer.Stop()
	}
}

func imageProxyRouteKey(nodeID, proxyURL string) string {
	return nodeID + "\x00" + proxyURL
}

func (s *Server) releaseImageProxyRoute(key string) {
	s.imageEgressMu.Lock()
	defer s.imageEgressMu.Unlock()
	if s.imageEgressInflight[key] <= 1 {
		delete(s.imageEgressInflight, key)
	} else {
		s.imageEgressInflight[key]--
	}
	if s.imageEgressNotify != nil {
		close(s.imageEgressNotify)
		s.imageEgressNotify = make(chan struct{})
	}
}

func (s *Server) formatProxyRouteLabel(route proxyRoute) string {
	if route.URL == "" {
		return "直连"
	}
	safeURL := safeURLForLog(route.URL)
	if route.NodeID != "" && s.store != nil {
		if groups, err := s.store.LoadProxyGroups(); err == nil {
			for _, g := range groups {
				for _, n := range g.Nodes {
					if n.ID == route.NodeID {
						if strings.TrimSpace(n.Name) != "" {
							return fmt.Sprintf("%s (%s)", n.Name, safeURL)
						}
						break
					}
				}
			}
		}
	}
	return safeURL
}

type imageProxyTrackerKey struct{}

type imageProxyTracker struct {
	mu      sync.Mutex
	proxies []string
}

func (t *imageProxyTracker) add(p string) {
	p = strings.TrimSpace(p)
	if p == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, existing := range t.proxies {
		if existing == p {
			return
		}
	}
	t.proxies = append(t.proxies, p)
}

func (t *imageProxyTracker) String() string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.proxies) == 0 {
		return ""
	}
	return strings.Join(t.proxies, ", ")
}

func withImageProxyTracker(ctx context.Context, tracker *imageProxyTracker) context.Context {
	if tracker == nil {
		return ctx
	}
	return context.WithValue(ctx, imageProxyTrackerKey{}, tracker)
}

func imageProxyTrackerFromContext(ctx context.Context) *imageProxyTracker {
	if ctx == nil {
		return nil
	}
	if t, ok := ctx.Value(imageProxyTrackerKey{}).(*imageProxyTracker); ok {
		return t
	}
	return nil
}

func recordImageProxyAttempt(ctx context.Context, label string) {
	if t := imageProxyTrackerFromContext(ctx); t != nil {
		t.add(label)
	}
}
