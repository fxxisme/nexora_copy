package app

import (
	"context"
	"errors"
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
