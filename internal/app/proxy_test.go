package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHandleProxyTestUsesRequestedURLAndReportsError(t *testing.T) {
	t.Setenv("CHATGPT2API_UPSTREAM_TRANSPORT", "")
	type proxyRequest struct{ method, authorization string }
	requests := make(chan proxyRequest, 1)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- proxyRequest{method: r.Method, authorization: r.Header.Get("Proxy-Authorization")}
		w.WriteHeader(http.StatusProxyAuthRequired)
	}))
	defer proxy.Close()
	proxyURL, _ := url.Parse(proxy.URL)
	proxyURL.User = url.UserPassword("user", "secret")
	body, _ := json.Marshal(map[string]string{"url": proxyURL.String()})

	s := &Server{cfg: Config{AuthKey: "test-key", Proxy: "http://127.0.0.1:1"}}
	req := httptest.NewRequest(http.MethodPost, "/api/proxy/test", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer test-key")
	recorder := httptest.NewRecorder()
	s.handleProxyTest(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	select {
	case received := <-requests:
		if received.method != http.MethodConnect {
			t.Fatalf("proxy method = %s, want CONNECT", received.method)
		}
		if received.authorization != "Basic "+base64.StdEncoding.EncodeToString([]byte("user:secret")) {
			t.Fatal("proxy credentials were not sent in CONNECT")
		}
	default:
		t.Fatal("requested proxy was not contacted")
	}
	var payload struct {
		Result struct {
			OK        bool   `json:"ok"`
			Error     string `json:"error"`
			LatencyMS int64  `json:"latency_ms"`
		} `json:"result"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Result.OK || payload.Result.Error == "" || payload.Result.LatencyMS < 0 || strings.Contains(payload.Result.Error, "secret") {
		t.Fatalf("unexpected proxy result: %+v", payload.Result)
	}
}

func TestProxyTestUsesConfiguredUpstreamTransport(t *testing.T) {
	t.Setenv("CHATGPT2API_UPSTREAM_TRANSPORT", "curl-impersonate")
	t.Setenv("CHATGPT2API_CURL_IMPERSONATE_BIN", "/nonexistent/curl-impersonate")
	s := &Server{}
	result := s.testProxyURL(httptest.NewRequest(http.MethodPost, "/api/proxy/test", nil), "http://127.0.0.1:1")
	if result["ok"] != false || !strings.Contains(result["error"].(string), "CHATGPT2API_CURL_IMPERSONATE_BIN not found") {
		t.Fatalf("proxy test did not use selected upstream transport: %+v", result)
	}
}

func TestProxyGroupTestRequiresTargetAndIgnoresCancellation(t *testing.T) {
	t.Setenv("CHATGPT2API_UPSTREAM_TRANSPORT", "")
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	defer store.Close()
	group, err := store.SaveProxyGroup(proxyGroupInput{Name: "test", Nodes: []proxyNodeInput{{URL: "http://127.0.0.1:1"}}})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: Config{AuthKey: "test-key"}, store: store}
	request := func(ctx context.Context, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/proxy/groups/test", strings.NewReader(body)).WithContext(ctx)
		req.Header.Set("Authorization", "Bearer test-key")
		response := httptest.NewRecorder()
		s.handleProxyGroupTest(response, req)
		return response
	}
	if response := request(context.Background(), `{}`); response.Code != http.StatusBadRequest {
		t.Fatalf("missing target status = %d", response.Code)
	}
	if response := request(context.Background(), `{"id":"missing"}`); response.Code != http.StatusNotFound {
		t.Fatalf("unknown group status = %d", response.Code)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	response := request(ctx, `{"id":"`+group.ID+`"}`)
	if response.Body.Len() != 0 {
		t.Fatalf("cancelled request wrote response: %s", response.Body.String())
	}
	groups, err := store.LoadProxyGroups()
	if err != nil {
		t.Fatal(err)
	}
	if groups[0].Nodes[0].Health.State != "unknown" || groups[0].Nodes[0].FailCount != 0 {
		t.Fatalf("cancelled test changed node health: %+v", groups[0].Nodes[0].Health)
	}
}

func TestProxyGroupTestBoundsConcurrentProbes(t *testing.T) {
	t.Setenv("CHATGPT2API_UPSTREAM_TRANSPORT", "")
	var active, peak atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := active.Add(1)
		for old := peak.Load(); current > old && !peak.CompareAndSwap(old, current); old = peak.Load() {
		}
		time.Sleep(40 * time.Millisecond)
		active.Add(-1)
		w.WriteHeader(http.StatusProxyAuthRequired)
	}))
	defer proxy.Close()
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	defer store.Close()
	nodes := make([]proxyNodeInput, 16)
	for index := range nodes {
		u, _ := url.Parse(proxy.URL)
		u.User = url.UserPassword(fmt.Sprintf("user%d", index), "secret")
		nodes[index].URL = u.String()
	}
	group, err := store.SaveProxyGroup(proxyGroupInput{Name: "test", Nodes: nodes})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: Config{AuthKey: "test-key"}, store: store}
	req := httptest.NewRequest(http.MethodPost, "/api/proxy/groups/test", strings.NewReader(`{"id":"`+group.ID+`"}`))
	req.Header.Set("Authorization", "Bearer test-key")
	response := httptest.NewRecorder()
	s.handleProxyGroupTest(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if got := peak.Load(); got < 2 || got > 8 {
		t.Fatalf("concurrent probes = %d, want 2..8", got)
	}
	var payload struct {
		Summary struct{ Total, Failed int } `json:"summary"`
		Results []json.RawMessage           `json:"results"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Summary.Total != len(nodes) || payload.Summary.Failed != len(nodes) || len(payload.Results) != len(nodes) {
		t.Fatalf("unexpected group test result: %+v", payload.Summary)
	}
}

func TestProxyNodeHealthBatchIsAtomicAndIgnoresEditedURLs(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	defer store.Close()
	group, err := store.SaveProxyGroup(proxyGroupInput{Name: "test", Nodes: []proxyNodeInput{
		{URL: "http://127.0.0.1:8001"}, {URL: "http://127.0.0.1:8002"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	updates := []proxyNodeHealthUpdate{
		{NodeID: group.Nodes[0].ID, URL: group.Nodes[0].URL, State: "healthy"},
		{NodeID: group.Nodes[1].ID, URL: group.Nodes[1].URL, State: "invalid"},
	}
	if err := store.setProxyNodeHealthBatch(context.Background(), updates); err == nil {
		t.Fatal("accepted invalid batch")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.setProxyNodeHealthBatch(ctx, updates[:1]); err == nil {
		t.Fatal("accepted cancelled batch")
	}
	newURL := "http://127.0.0.1:8003"
	if _, err := store.db.Exec("UPDATE app_proxy_nodes SET url="+store.bind(1)+" WHERE id="+store.bind(2), newURL, group.Nodes[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := store.setProxyNodeHealthBatch(context.Background(), updates[:1]); err != nil {
		t.Fatal(err)
	}
	groups, err := store.LoadProxyGroups()
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range groups[0].Nodes {
		if node.Health.State != "unknown" || node.SuccessCount != 0 {
			t.Fatalf("stale or rolled-back health changed node: %+v", node.Health)
		}
	}
	updates[0].URL = newURL
	if err := store.setProxyNodeHealthBatch(context.Background(), updates[:1]); err != nil {
		t.Fatal(err)
	}
	groups, err = store.LoadProxyGroups()
	if err != nil {
		t.Fatal(err)
	}
	if groups[0].Nodes[0].Health.State != "healthy" || groups[0].Nodes[0].SuccessCount != 1 {
		t.Fatalf("current node health was not updated: %+v", groups[0].Nodes[0].Health)
	}
}

func TestDiagnoseProxyFailureRedactsCredentials(t *testing.T) {
	proxyURL := "http://user:secret@127.0.0.1:7890"
	err := fmt.Errorf("proxyconnect tcp %s: 407 Proxy Authentication Required", proxyURL)
	got := diagnoseProxyFailure(context.Background(), &http.Client{}, err, proxyURL)
	if got != "代理认证失败（HTTP 407）" || strings.Contains(got, "secret") {
		t.Fatalf("diagnostic = %q", got)
	}
}
