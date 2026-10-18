package app

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPromptLibrarySeedAndSourceSettingsPersist(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	store := NewStore(dir)
	s := &Server{store: store, cfg: Config{AuthKey: "admin-key"}}
	request := func(method, target, body, key string, handler http.HandlerFunc) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		recorder := httptest.NewRecorder()
		handler(recorder, req)
		return recorder
	}
	if response := request(http.MethodGet, "/api/prompts", "", "", s.handlePromptLibrary); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d", response.Code)
	}
	response := request(http.MethodGet, "/api/prompts", "", "admin-key", s.handlePromptLibrary)
	if response.Code != http.StatusOK {
		t.Fatalf("seed status = %d: %s", response.Code, response.Body.String())
	}
	var view struct {
		PromptCount int                   `json:"prompt_count"`
		Items       []PromptLibraryItem   `json:"items"`
		Sources     []promptLibrarySource `json:"sources"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.PromptCount != 301 || len(view.Items) != 301 || len(view.Sources) != 6 {
		t.Fatalf("incomplete seed: prompts=%d sources=%d", view.PromptCount, len(view.Sources))
	}
	ids := map[string]bool{}
	for _, item := range view.Items {
		if item.ID == "" || ids[item.ID] || item.Prompt == "" {
			t.Fatalf("invalid seeded item: %q", item.ID)
		}
		ids[item.ID] = true
	}
	response = request(http.MethodPost, "/api/admin/prompt-sources/banana-prompt-quicker", `{"enabled":false}`, "admin-key", s.handlePromptSource)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"prompt_count":0`) {
		t.Fatalf("disable source: %d %s", response.Code, response.Body.String())
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = NewStore(dir)
	defer store.Close()
	s.store = store
	state, err := s.loadPromptState()
	if err != nil || state.Enabled["banana-prompt-quicker"] || len(state.Items["banana-prompt-quicker"]) != 301 {
		t.Fatalf("source setting or cache did not persist: enabled=%v cached=%d err=%v", state.Enabled["banana-prompt-quicker"], len(state.Items["banana-prompt-quicker"]), err)
	}
}

func TestPromptLibraryRefreshKeepsCacheOnFailure(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	defer store.Close()
	s := &Server{store: store, cfg: Config{AuthKey: "admin-key"}}
	t.Setenv("PROMPT_LIBRARY_REGISTRY_URL", "http://127.0.0.1:1")
	req := httptest.NewRequest(http.MethodPost, "/api/admin/prompt-sources/refresh", nil)
	req.Header.Set("Authorization", "Bearer admin-key")
	response := httptest.NewRecorder()
	s.handlePromptSourcesRefresh(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("refresh status = %d: %s", response.Code, response.Body.String())
	}
	var view struct {
		PromptCount      int `json:"prompt_count"`
		SourceErrorCount int `json:"source_error_count"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.PromptCount != 301 || view.SourceErrorCount == 0 {
		t.Fatalf("failed refresh lost cache or error state: %+v", view)
	}
}

func TestPromptLibrarySnapshotMigrationAndRecovery(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	defer store.Close()
	s := &Server{store: store}
	state, err := s.loadPromptState()
	if err != nil {
		t.Fatal(err)
	}
	sourceID := state.Sources[0].ID
	originalPrompt := state.Items[sourceID][0].Prompt
	state.Enabled[sourceID] = false
	state.SnapshotDigest = ""
	if err := store.SaveDocument(promptLibraryDocument, state); err != nil {
		t.Fatal(err)
	}
	migrated, err := s.loadPromptState()
	if err != nil || migrated.SnapshotDigest == "" || migrated.SnapshotDigest != promptSnapshotDigest(migrated) || migrated.Enabled[sourceID] || migrated.Items[sourceID][0].Prompt != originalPrompt {
		t.Fatalf("legacy snapshot was not migrated intact: digest=%q enabled=%v err=%v", migrated.SnapshotDigest, migrated.Enabled[sourceID], err)
	}
	migrated.Items[sourceID][0].Prompt = "corrupted"
	if err := store.SaveDocument(promptLibraryDocument, migrated); err != nil {
		t.Fatal(err)
	}
	recovered, err := s.loadPromptState()
	if err != nil || recovered.Items[sourceID][0].Prompt != originalPrompt || recovered.Enabled[sourceID] || recovered.Status[sourceID].LastError == "" || recovered.SnapshotDigest != promptSnapshotDigest(recovered) {
		t.Fatalf("corrupt snapshot was not restored: enabled=%v error=%q err=%v", recovered.Enabled[sourceID], recovered.Status[sourceID].LastError, err)
	}
	recovered.Items = nil
	recovered.SnapshotDigest = ""
	if err := store.SaveDocument(promptLibraryDocument, recovered); err != nil {
		t.Fatal(err)
	}
	filled, err := s.loadPromptState()
	if err != nil || len(filled.Items[sourceID]) != 301 || filled.Enabled[sourceID] {
		t.Fatalf("empty legacy snapshot was not restored: count=%d enabled=%v err=%v", len(filled.Items[sourceID]), filled.Enabled[sourceID], err)
	}
}

func TestPromptLibraryMalformedDocumentRecoveryPreservesNewerWrite(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	defer store.Close()
	s := &Server{store: store}
	for _, payload := range []string{"{broken", `{"sources":42}`} {
		if _, err := store.db.Exec("INSERT INTO app_documents (name, payload) VALUES (?, ?) ON CONFLICT (name) DO UPDATE SET payload=EXCLUDED.payload", promptLibraryDocument, payload); err != nil {
			t.Fatal(err)
		}
		state, err := s.loadPromptState()
		if err != nil || len(state.Items[defaultPromptSources[0].ID]) != 301 || state.Status[defaultPromptSources[0].ID].LastError == "" || state.SnapshotDigest != promptSnapshotDigest(state) {
			t.Fatalf("malformed document was not restored: count=%d err=%v", len(state.Items[defaultPromptSources[0].ID]), err)
		}
		changed, err := store.replaceDocumentIfPayload(promptLibraryDocument, payload, promptLibraryState{})
		if err != nil || changed {
			t.Fatalf("stale repair replaced a newer snapshot: changed=%v err=%v", changed, err)
		}
	}
	sourceID := defaultPromptSources[0].ID
	if err := store.SaveDocument(promptLibraryDocument, promptLibraryState{Enabled: map[string]bool{sourceID: false}}); err != nil {
		t.Fatal(err)
	}
	recovered, err := s.loadPromptState()
	if err != nil || recovered.Enabled[sourceID] || len(recovered.Items[sourceID]) != 301 || recovered.Status[sourceID].LastError == "" {
		t.Fatalf("metadata loss did not preserve source setting: enabled=%v count=%d err=%v", recovered.Enabled[sourceID], len(recovered.Items[sourceID]), err)
	}
}

func TestFetchPromptRegistryChecksHashesAndCounts(t *testing.T) {
	id := "banana-prompt-quicker:one"
	text := "Draw one red square"
	payload, err := json.Marshal([]map[string]any{{
		"id": id, "sourceId": "banana-prompt-quicker", "title": "Red square", "prompt": text,
		"description": "Simple image", "coverUrl": "https://example.com/preview.png", "sourceUrl": "https://example.com/source",
		"tags": []string{"minimal"}, "referenceImageUrls": []string{}, "imageMode": "generate", "imageModel": "", "createdAt": "",
	}})
	if err != nil {
		t.Fatal(err)
	}
	sourceHash := sha256.Sum256([]byte("source file bytes"))
	source := map[string]any{
		"id": "banana-prompt-quicker", "name": "Banana Prompt Quicker", "homepage": "https://example.com/",
		"upstreamUrl": "https://example.com/prompts.json", "path": "sources/banana-prompt-quicker.json",
		"count": 1, "sha256": hex.EncodeToString(sourceHash[:]),
	}
	canonical, err := canonicalPromptJSON([]map[string]any{source})
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(append(canonical, payload...))
	manifest, err := json.Marshal(map[string]any{
		"schemaVersion": 1, "registryHash": hex.EncodeToString(hash[:]), "generatedAt": "2026-10-17T00:00:00Z",
		"promptsPath": "prompts.json", "total": 1, "sources": []map[string]any{source},
	})
	if err != nil {
		t.Fatal(err)
	}
	var payloadRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.json":
			_, _ = w.Write(manifest)
		case "/prompts.json":
			payloadRequests.Add(1)
			_, _ = w.Write(payload)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	registryManifest, sources, items, err := fetchPromptRegistry(context.Background(), server.URL, "", nil)
	if err != nil || len(sources) != 1 || len(items["banana-prompt-quicker"]) != 1 {
		t.Fatalf("valid registry rejected: sources=%d items=%d err=%v", len(sources), len(items["banana-prompt-quicker"]), err)
	}
	cached := promptLibraryState{
		Sources: sources, Items: items, Status: map[string]promptSourceStatus{sources[0].ID: {ContentRevision: sources[0].SHA256}},
		RegistryRevision: registryManifest.RegistryHash, RegistryGeneratedAt: registryManifest.GeneratedAt,
		SnapshotVerified: true,
	}
	cached.SnapshotDigest = promptSnapshotDigest(cached)
	if _, _, _, err := fetchPromptRegistry(context.Background(), server.URL, "", &cached); err != nil || payloadRequests.Load() != 1 {
		t.Fatalf("verified unchanged snapshot fetched payload again: requests=%d err=%v", payloadRequests.Load(), err)
	}
	cached.SnapshotVerified = false
	if _, _, _, err := fetchPromptRegistry(context.Background(), server.URL, "", &cached); err != nil || payloadRequests.Load() != 2 {
		t.Fatalf("unverified snapshot skipped payload: requests=%d err=%v", payloadRequests.Load(), err)
	}
	cached.SnapshotVerified = true
	cached.Items[sources[0].ID][0].Prompt = "tampered"
	if _, _, _, err := fetchPromptRegistry(context.Background(), server.URL, "", &cached); err != nil || payloadRequests.Load() != 3 {
		t.Fatalf("modified snapshot skipped payload: requests=%d err=%v", payloadRequests.Load(), err)
	}
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Proxy-Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("user:secret")) || r.URL.Hostname() != "registry.invalid" {
			w.WriteHeader(http.StatusProxyAuthRequired)
			return
		}
		switch r.URL.Path {
		case "/dist/manifest.json":
			_, _ = w.Write(manifest)
		case "/dist/prompts.json":
			_, _ = w.Write(payload)
		default:
			http.NotFound(w, r)
		}
	}))
	defer proxyServer.Close()
	proxyURL, _ := url.Parse(proxyServer.URL)
	proxyURL.User = url.UserPassword("user", "secret")
	if _, _, _, err := fetchPromptRegistry(context.Background(), "http://registry.invalid/dist", proxyURL.String(), nil); err != nil {
		t.Fatalf("authenticated registry proxy rejected: %v", err)
	}
	payload[0] = ' '
	if _, _, _, err := fetchPromptRegistry(context.Background(), server.URL, "", nil); err == nil {
		t.Fatal("changed payload accepted with stale hash")
	}
}

func TestPromptSourceRefreshScopeAndNewSourceDefault(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	defer store.Close()
	s := &Server{store: store, cfg: Config{AuthKey: "admin-key"}}
	seed, err := s.loadPromptState()
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{"banana-prompt-quicker", "davidwu-gpt-image2-prompts", "new-source"}
	sources := make([]map[string]any, 0, len(ids))
	items := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		itemID := id + ":one"
		prompt := "A prompt from " + id
		sources = append(sources, map[string]any{
			"id": id, "name": id, "homepage": "https://example.com/",
			"upstreamUrl": "https://example.com/prompts.json", "path": "sources/" + id + ".json",
			"count": 1, "sha256": promptRevision([]PromptLibraryItem{{ID: itemID, Prompt: prompt}}),
		})
		items = append(items, map[string]any{
			"id": itemID, "sourceId": id, "title": id, "prompt": prompt,
			"sourceUrl": "https://example.com/", "tags": []string{},
		})
	}
	payload, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := canonicalPromptJSON(sources)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(append(canonical, payload...))
	manifest, err := json.Marshal(map[string]any{
		"schemaVersion": 1, "registryHash": hex.EncodeToString(hash[:]), "generatedAt": "2026-10-17T00:00:00Z",
		"promptsPath": "prompts.json", "total": len(items), "sources": sources,
	})
	if err != nil {
		t.Fatal(err)
	}
	var payloadRequests atomic.Int32
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.json":
			_, _ = w.Write(manifest)
		case "/prompts.json":
			payloadRequests.Add(1)
			_, _ = w.Write(payload)
		default:
			http.NotFound(w, r)
		}
	}))
	defer registry.Close()
	t.Setenv("PROMPT_LIBRARY_REGISTRY_URL", registry.URL)
	refresh := func(target string, handler http.HandlerFunc) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, target, nil)
		req.Header.Set("Authorization", "Bearer admin-key")
		response := httptest.NewRecorder()
		handler(response, req)
		if response.Code != http.StatusOK {
			t.Fatalf("refresh %s: %d %s", target, response.Code, response.Body.String())
		}
	}
	refresh("/api/admin/prompt-sources/banana-prompt-quicker/refresh", s.handlePromptSource)
	partial, err := s.loadPromptState()
	if err != nil {
		t.Fatal(err)
	}
	if len(partial.Sources) != len(seed.Sources) || len(partial.Items[ids[0]]) != 1 || len(partial.Items[ids[1]]) != 0 || partial.RegistryRevision != "" {
		t.Fatalf("single-source refresh changed other sources: sources=%d first=%d second=%d revision=%q", len(partial.Sources), len(partial.Items[ids[0]]), len(partial.Items[ids[1]]), partial.RegistryRevision)
	}
	refresh("/api/admin/prompt-sources/refresh", s.handlePromptSourcesRefresh)
	full, err := s.loadPromptState()
	if err != nil {
		t.Fatal(err)
	}
	view := buildPromptLibraryView(full)
	if len(full.Sources) != len(ids) || view["prompt_count"] != 2 || promptSourceEnabled(full, ids[2]) {
		t.Fatalf("full refresh did not preserve new source as disabled: sources=%d visible=%v enabled=%v", len(full.Sources), view["prompt_count"], promptSourceEnabled(full, ids[2]))
	}
	if !full.SnapshotVerified || payloadRequests.Load() != 2 {
		t.Fatalf("full refresh did not verify payload: verified=%v requests=%d", full.SnapshotVerified, payloadRequests.Load())
	}
	refresh("/api/admin/prompt-sources/refresh", s.handlePromptSourcesRefresh)
	if payloadRequests.Load() != 2 {
		t.Fatalf("unchanged full refresh downloaded payload: requests=%d", payloadRequests.Load())
	}
	items[0]["prompt"] = "Updated prompt from " + ids[0]
	payload, err = json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	hash = sha256.Sum256(append(canonical, payload...))
	manifest, err = json.Marshal(map[string]any{
		"schemaVersion": 1, "registryHash": hex.EncodeToString(hash[:]), "generatedAt": "2026-10-18T00:00:00Z",
		"promptsPath": "prompts.json", "total": len(items), "sources": sources,
	})
	if err != nil {
		t.Fatal(err)
	}
	refresh("/api/admin/prompt-sources/banana-prompt-quicker/refresh", s.handlePromptSource)
	mixed, err := s.loadPromptState()
	if err != nil || mixed.RegistryRevision != "" || mixed.RegistryGeneratedAt != "" || mixed.SnapshotVerified || mixed.Items[ids[0]][0].Prompt != items[0]["prompt"] || len(mixed.Items[ids[1]]) != 1 || mixed.SnapshotDigest != promptSnapshotDigest(mixed) {
		t.Fatalf("partial refresh kept stale registry revision or lost cache: revision=%q other=%d err=%v", mixed.RegistryRevision, len(mixed.Items[ids[1]]), err)
	}
}

func TestPromptRefreshPreservesConcurrentUpdates(t *testing.T) {
	sourceID := defaultPromptSources[0].ID
	source := map[string]any{
		"id": sourceID, "name": "Source", "homepage": "https://example.com/",
		"upstreamUrl": "https://example.com/prompts.json", "path": "sources/source.json",
		"count": 1, "sha256": strings.Repeat("a", 64),
	}
	payload, err := json.Marshal([]map[string]any{{
		"id": sourceID + ":one", "sourceId": sourceID, "title": "Prompt", "prompt": "remote prompt",
		"sourceUrl": "https://example.com/",
	}})
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := canonicalPromptJSON([]map[string]any{source})
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(append(canonical, payload...))
	manifest, err := json.Marshal(map[string]any{
		"schemaVersion": 1, "registryHash": hex.EncodeToString(hash[:]), "generatedAt": "2026-10-17T00:00:00Z",
		"promptsPath": "prompts.json", "total": 1, "sources": []map[string]any{source},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []struct {
		name          string
		remoteFailure bool
	}{
		{name: "newer content"},
		{name: "newer status", remoteFailure: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			store := NewStore(filepath.Join(t.TempDir(), "data"))
			defer store.Close()
			s := &Server{store: store, cfg: Config{AuthKey: "admin-key"}}
			started := make(chan struct{})
			release := make(chan struct{})
			var releaseOnce sync.Once
			registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/manifest.json":
					_, _ = w.Write(manifest)
				case "/prompts.json":
					close(started)
					<-release
					if scenario.remoteFailure {
						w.WriteHeader(http.StatusBadGateway)
						return
					}
					_, _ = w.Write(payload)
				default:
					http.NotFound(w, r)
				}
			}))
			defer registry.Close()
			defer releaseOnce.Do(func() { close(release) })
			t.Setenv("PROMPT_LIBRARY_REGISTRY_URL", registry.URL)
			finished := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				req := httptest.NewRequest(http.MethodPost, "/api/admin/prompt-sources/refresh", nil)
				req.Header.Set("Authorization", "Bearer admin-key")
				response := httptest.NewRecorder()
				s.handlePromptSourcesRefresh(response, req)
				finished <- response
			}()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("refresh did not reach remote payload")
			}
			newer, err := s.loadPromptState()
			if err != nil {
				t.Fatal(err)
			}
			if scenario.remoteFailure {
				status := newer.Status[sourceID]
				status.LastError = "newer status"
				newer.Status[sourceID] = status
			} else {
				newer.Items[sourceID][0].Prompt = "newer cached prompt"
				newer.SnapshotDigest = promptSnapshotDigest(newer)
			}
			if err := store.SaveDocument(promptLibraryDocument, newer); err != nil {
				t.Fatal(err)
			}
			releaseOnce.Do(func() { close(release) })
			select {
			case response := <-finished:
				if response.Code != http.StatusConflict {
					t.Fatalf("stale refresh status=%d body=%s", response.Code, response.Body.String())
				}
			case <-time.After(5 * time.Second):
				t.Fatal("stale refresh did not finish")
			}
			current, err := s.loadPromptState()
			if err != nil || current.Items[sourceID][0].Prompt != newer.Items[sourceID][0].Prompt || current.Status[sourceID].LastError != newer.Status[sourceID].LastError {
				t.Fatalf("stale refresh replaced newer state: err=%v", err)
			}
		})
	}
}
