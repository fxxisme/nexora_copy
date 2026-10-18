package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"reflect"
	"regexp"
	"strings"
	"time"
)

//go:embed promptdata/default_prompt_library.json
var bundledPromptLibrary embed.FS

const promptLibraryDocument = "prompt_library_state"
const defaultPromptRegistry = "https://raw.githubusercontent.com/yukkcat/image-prompts/main/dist"

type PromptLibraryItem struct {
	ID                 string   `json:"id"`
	SourceID           string   `json:"source_id"`
	SourceName         string   `json:"source_name"`
	Title              string   `json:"title"`
	Prompt             string   `json:"prompt"`
	Description        string   `json:"description"`
	Preview            string   `json:"preview"`
	Link               string   `json:"link"`
	Author             string   `json:"author"`
	Category           string   `json:"category"`
	SubCategory        string   `json:"sub_category"`
	Tags               []string `json:"tags"`
	ReferenceImageURLs []string `json:"reference_image_urls"`
	ImageMode          string   `json:"image_mode"`
	ImageModel         string   `json:"image_model"`
	ImageSize          string   `json:"image_size"`
	ImageCount         *int     `json:"image_count"`
	CreatedAt          string   `json:"created_at"`
}

type promptRegistrySource struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Homepage    string `json:"homepage"`
	UpstreamURL string `json:"upstream_url"`
	Path        string `json:"path"`
	Count       int    `json:"count"`
	SHA256      string `json:"sha256"`
}

type promptSourceStatus struct {
	ContentRevision string `json:"content_revision"`
	LastSyncAt      string `json:"last_sync_at"`
	LastError       string `json:"last_error"`
	LastFetchMS     *int64 `json:"last_fetch_ms"`
}

type promptLibraryState struct {
	Sources             []promptRegistrySource         `json:"sources"`
	Enabled             map[string]bool                `json:"enabled"`
	Items               map[string][]PromptLibraryItem `json:"items"`
	Status              map[string]promptSourceStatus  `json:"status"`
	RegistryRevision    string                         `json:"registry_revision"`
	RegistryGeneratedAt string                         `json:"registry_generated_at"`
	SnapshotDigest      string                         `json:"snapshot_digest"`
	SnapshotVerified    bool                           `json:"snapshot_verified"`
}

type promptLibrarySource struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	Homepage    string `json:"homepage"`
	Enabled     bool   `json:"enabled"`
	BuiltIn     bool   `json:"built_in"`
	SortOrder   int    `json:"sort_order"`
	PromptCount int    `json:"prompt_count"`
	Cached      bool   `json:"cached"`
	SyncState   string `json:"sync_state"`
	SyncLabel   string `json:"sync_label"`
	SyncMessage string `json:"sync_message"`
	SyncTone    string `json:"sync_tone"`
	LastSyncAt  string `json:"last_sync_at"`
	LastError   string `json:"last_error"`
	LastFetchMS *int64 `json:"last_fetch_ms"`
}

type promptSourceError struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Error string `json:"error"`
}

var defaultPromptSources = []promptRegistrySource{
	{ID: "banana-prompt-quicker", Name: "Banana Prompt Quicker", Homepage: "https://glidea.github.io/banana-prompt-quicker/", UpstreamURL: "https://glidea.github.io/banana-prompt-quicker/prompts.json"},
	{ID: "davidwu-gpt-image2-prompts", Name: "DavidWu GPT Image 2 Prompts", Homepage: "https://github.com/davidwuw0811-boop/awesome-gpt-image2-prompts", UpstreamURL: "https://raw.githubusercontent.com/davidwuw0811-boop/awesome-gpt-image2-prompts/main/prompts.json"},
	{ID: "awesome-gpt-image", Name: "Awesome GPT Image", Homepage: "https://github.com/ZeroLu/awesome-gpt-image", UpstreamURL: "https://raw.githubusercontent.com/ZeroLu/awesome-gpt-image/main/README.zh-CN.md"},
	{ID: "awesome-gpt4o-image-prompts", Name: "Awesome GPT-4o Image Prompts", Homepage: "https://github.com/ImgEdify/Awesome-GPT4o-Image-Prompts", UpstreamURL: "https://raw.githubusercontent.com/ImgEdify/Awesome-GPT4o-Image-Prompts/main/README.zh-CN.md"},
	{ID: "youmind-gpt-image-2", Name: "YouMind GPT Image 2", Homepage: "https://github.com/YouMind-OpenLab/awesome-gpt-image-2", UpstreamURL: "https://raw.githubusercontent.com/YouMind-OpenLab/awesome-gpt-image-2/main/README_zh.md"},
	{ID: "youmind-nano-banana-pro", Name: "YouMind Nano Banana Pro", Homepage: "https://github.com/YouMind-OpenLab/awesome-nano-banana-pro-prompts", UpstreamURL: "https://raw.githubusercontent.com/YouMind-OpenLab/awesome-nano-banana-pro-prompts/main/README_zh.md"},
}

func promptRevision(items []PromptLibraryItem) string {
	h := sha256.New()
	for _, item := range items {
		_, _ = io.WriteString(h, item.ID+"\x00"+item.Prompt+"\x00")
	}
	return hex.EncodeToString(h.Sum(nil))
}

func promptSnapshotDigest(state promptLibraryState) string {
	payload, _ := json.Marshal(struct {
		Sources             []promptRegistrySource         `json:"sources"`
		Items               map[string][]PromptLibraryItem `json:"items"`
		RegistryRevision    string                         `json:"registry_revision"`
		RegistryGeneratedAt string                         `json:"registry_generated_at"`
	}{state.Sources, state.Items, state.RegistryRevision, state.RegistryGeneratedAt})
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func promptRefreshStateMatches(before, current promptLibraryState) bool {
	return before.SnapshotDigest == current.SnapshotDigest &&
		before.SnapshotVerified == current.SnapshotVerified &&
		reflect.DeepEqual(before.Status, current.Status)
}

func validatePromptSnapshot(state promptLibraryState) error {
	if len(state.Sources) == 0 || (state.RegistryRevision != "" && !promptHashPattern.MatchString(state.RegistryRevision)) {
		return errors.New("词源元数据无效")
	}
	sources := make(map[string]promptRegistrySource, len(state.Sources))
	for _, source := range state.Sources {
		if !promptSourceIDPattern.MatchString(source.ID) || source.Name == "" || source.Count < 0 {
			return errors.New("词源元数据无效")
		}
		if _, exists := sources[source.ID]; exists {
			return errors.New("词源 ID 重复")
		}
		sources[source.ID] = source
	}
	seen := make(map[string]bool)
	for sourceID, items := range state.Items {
		source, exists := sources[sourceID]
		if !exists || (state.RegistryRevision != "" && len(items) != source.Count) {
			return errors.New("词源缓存数量或归属无效")
		}
		for _, item := range items {
			if item.SourceID != sourceID || !strings.HasPrefix(item.ID, sourceID+":") || seen[item.ID] || item.Title == "" || item.Prompt == "" {
				return errors.New("词源缓存条目无效")
			}
			seen[item.ID] = true
		}
	}
	for _, source := range state.Sources {
		if state.RegistryRevision != "" && len(state.Items[source.ID]) != source.Count {
			return errors.New("词源缓存数量无效")
		}
	}
	if len(seen) == 0 {
		return errors.New("词源缓存为空")
	}
	return nil
}

func promptURL(base, value string) string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || value == "" {
		return ""
	}
	baseURL, err := url.Parse(base)
	if err != nil {
		return ""
	}
	resolved := baseURL.ResolveReference(parsed)
	if (resolved.Scheme != "http" && resolved.Scheme != "https") || resolved.Host == "" || resolved.User != nil {
		return ""
	}
	return resolved.String()
}

func promptImageMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "generate", "image", "text-to-image", "t2i":
		return "generate"
	case "edit", "image-to-image", "i2i":
		return "edit"
	default:
		return ""
	}
}

func bundledPromptState() (promptLibraryState, error) {
	data, err := bundledPromptLibrary.ReadFile("promptdata/default_prompt_library.json")
	if err != nil {
		return promptLibraryState{}, err
	}
	var seed struct {
		Prompts []struct {
			ID          string   `json:"id"`
			Name        string   `json:"name"`
			Title       string   `json:"title"`
			Prompt      string   `json:"prompt"`
			Content     string   `json:"content"`
			Description string   `json:"description"`
			Preview     string   `json:"preview"`
			Link        string   `json:"link"`
			Author      string   `json:"author"`
			Category    string   `json:"category"`
			SubCategory string   `json:"sub_category"`
			Mode        string   `json:"mode"`
			ImageCount  string   `json:"image_count"`
			ImageSize   string   `json:"image_size"`
			Created     string   `json:"created"`
			CreatedAt   string   `json:"created_at"`
			References  []string `json:"reference_image_urls"`
		} `json:"prompts"`
	}
	if err := json.Unmarshal(data, &seed); err != nil {
		return promptLibraryState{}, err
	}
	state := promptLibraryState{
		Sources: append([]promptRegistrySource(nil), defaultPromptSources...),
		Enabled: map[string]bool{}, Items: map[string][]PromptLibraryItem{}, Status: map[string]promptSourceStatus{},
	}
	for _, source := range state.Sources {
		state.Enabled[source.ID] = true
		state.Items[source.ID] = []PromptLibraryItem{}
	}
	source := state.Sources[0]
	seen := map[string]bool{}
	for _, raw := range seed.Prompts {
		title := firstNonEmpty(raw.Title, raw.Name)
		text := firstNonEmpty(raw.Prompt, raw.Content)
		if strings.TrimSpace(title) == "" || strings.TrimSpace(text) == "" {
			continue
		}
		id := strings.TrimSpace(raw.ID)
		if id == "" {
			digest := sha256.Sum256([]byte(title + "\n" + text))
			id = hex.EncodeToString(digest[:])[:16]
		}
		id = source.ID + ":" + id
		if seen[id] {
			return promptLibraryState{}, fmt.Errorf("内置提示词 ID 重复：%s", id)
		}
		seen[id] = true
		references := make([]string, 0, len(raw.References))
		for _, value := range raw.References {
			if resolved := promptURL(source.UpstreamURL, value); resolved != "" {
				references = append(references, resolved)
			}
		}
		item := PromptLibraryItem{
			ID: id, SourceID: source.ID, SourceName: source.Name,
			Title: strings.TrimSpace(title), Prompt: strings.TrimSpace(text),
			Description: strings.TrimSpace(raw.Description), Preview: promptURL(source.UpstreamURL, raw.Preview),
			Link: promptURL(source.UpstreamURL, raw.Link), Author: strings.TrimSpace(raw.Author),
			Category: strings.TrimSpace(raw.Category), SubCategory: strings.TrimSpace(raw.SubCategory),
			Tags: []string{}, ReferenceImageURLs: references, ImageMode: promptImageMode(raw.Mode),
			ImageSize: strings.TrimSpace(raw.ImageSize), CreatedAt: firstNonEmpty(raw.CreatedAt, raw.Created),
		}
		if item.Link == "" {
			item.Link = source.Homepage
		}
		var count int
		if _, err := fmt.Sscanf(raw.ImageCount, "%d", &count); err == nil && count > 0 {
			item.ImageCount = &count
		}
		state.Items[source.ID] = append(state.Items[source.ID], item)
	}
	if len(state.Items[source.ID]) == 0 {
		return promptLibraryState{}, errors.New("内置提示词库为空")
	}
	state.Status[source.ID] = promptSourceStatus{ContentRevision: promptRevision(state.Items[source.ID]), LastSyncAt: nowISO()}
	state.SnapshotDigest = promptSnapshotDigest(state)
	return state, nil
}

func (s *Server) loadPromptState() (promptLibraryState, error) {
	var state promptLibraryState
	for attempt := 0; attempt < 3; attempt++ {
		state = promptLibraryState{}
		err := s.store.LoadDocument(promptLibraryDocument, &state)
		if err == nil {
			break
		}
		var syntaxErr *json.SyntaxError
		var typeErr *json.UnmarshalTypeError
		if !errors.As(err, &syntaxErr) && !errors.As(err, &typeErr) {
			return state, err
		}
		seed, seedErr := bundledPromptState()
		if seedErr != nil {
			return state, seedErr
		}
		payload, queryErr := s.store.documentPayload(promptLibraryDocument)
		if queryErr != nil {
			return state, queryErr
		}
		var current promptLibraryState
		if json.Unmarshal([]byte(payload), &current) != nil {
			status := seed.Status[seed.Sources[0].ID]
			status.LastError = "本地词源缓存无法解码，已恢复内置快照"
			seed.Status[seed.Sources[0].ID] = status
			if _, repairErr := s.store.replaceDocumentIfPayload(promptLibraryDocument, payload, seed); repairErr != nil {
				return state, repairErr
			}
		}
		if attempt == 2 {
			state = promptLibraryState{}
			if readErr := s.store.LoadDocument(promptLibraryDocument, &state); readErr != nil {
				return state, fmt.Errorf("读取提示词库失败：%w", readErr)
			}
		}
	}
	validErr := validatePromptSnapshot(state)
	if validErr == nil && state.SnapshotDigest != "" && state.SnapshotDigest == promptSnapshotDigest(state) {
		return state, nil
	}
	seed, err := bundledPromptState()
	if err != nil {
		return state, err
	}
	if err := updateDocument(s.store, promptLibraryDocument, seed, func(current promptLibraryState) promptLibraryState {
		if len(current.Sources) == 0 {
			recovered := seed
			if current.Enabled != nil {
				recovered.Enabled = current.Enabled
			}
			status := recovered.Status[recovered.Sources[0].ID]
			status.LastError = "本地词源缓存校验失败，已恢复内置快照"
			recovered.Status[recovered.Sources[0].ID] = status
			return recovered
		}
		if validatePromptSnapshot(current) == nil {
			if current.SnapshotDigest == "" {
				current.SnapshotDigest = promptSnapshotDigest(current)
			}
			if current.SnapshotDigest == promptSnapshotDigest(current) {
				return current
			}
		}
		recovered := seed
		if current.Enabled != nil {
			recovered.Enabled = current.Enabled
		}
		status := recovered.Status[recovered.Sources[0].ID]
		status.LastError = "本地词源缓存校验失败，已恢复内置快照"
		recovered.Status[recovered.Sources[0].ID] = status
		return recovered
	}); err != nil {
		return state, err
	}
	var persisted promptLibraryState
	if err := s.store.LoadDocument(promptLibraryDocument, &persisted); err != nil {
		return state, err
	}
	return persisted, nil
}

func promptSourceEnabled(state promptLibraryState, sourceID string) bool {
	if enabled, configured := state.Enabled[sourceID]; configured {
		return enabled
	}
	for _, source := range defaultPromptSources {
		if source.ID == sourceID {
			return true
		}
	}
	return false
}

func buildPromptLibraryView(state promptLibraryState) map[string]any {
	sources := make([]promptLibrarySource, 0, len(state.Sources))
	items := make([]PromptLibraryItem, 0)
	errorsBySource := make([]promptSourceError, 0)
	cachedCount, enabledCount := 0, 0
	for index, source := range state.Sources {
		enabled := promptSourceEnabled(state, source.ID)
		cached := len(state.Items[source.ID]) > 0
		status := state.Status[source.ID]
		view := promptLibrarySource{
			ID: source.ID, Name: source.Name, URL: source.UpstreamURL, Homepage: source.Homepage,
			Enabled: enabled, BuiltIn: true, SortOrder: index * 10, PromptCount: len(state.Items[source.ID]),
			Cached: cached, LastSyncAt: status.LastSyncAt, LastError: status.LastError, LastFetchMS: status.LastFetchMS,
		}
		switch {
		case !enabled:
			view.SyncState, view.SyncLabel, view.SyncMessage, view.SyncTone = "disabled", "已停用", "提示词源已停用", "muted"
		case status.LastError != "" && cached:
			view.SyncState, view.SyncLabel, view.SyncMessage, view.SyncTone = "cached", "缓存可用", "同步失败，继续使用本地缓存", "warning"
		case status.LastError != "":
			view.SyncState, view.SyncLabel, view.SyncMessage, view.SyncTone = "failed", "同步失败", "当前没有可用缓存", "danger"
		case cached:
			view.SyncState, view.SyncLabel, view.SyncMessage, view.SyncTone = "synced", "已同步", "提示词源已更新到本地", "success"
		default:
			view.SyncState, view.SyncLabel, view.SyncMessage, view.SyncTone = "pending", "待同步", "等待首次同步", "muted"
		}
		if cached {
			cachedCount++
		}
		if enabled {
			enabledCount++
			items = append(items, state.Items[source.ID]...)
			if status.LastError != "" {
				errorsBySource = append(errorsBySource, promptSourceError{ID: source.ID, Name: source.Name, Error: status.LastError})
			}
		}
		sources = append(sources, view)
	}
	failed := len(errorsBySource)
	summaryStatus, summaryTone := "success", "success"
	if failed > 0 {
		summaryStatus, summaryTone = "partial", "warning"
		if failed == enabledCount {
			summaryStatus, summaryTone = "failed", "danger"
		}
	}
	message := fmt.Sprintf("词源同步完成：成功 %d，失败 %d，共 %d 条提示词", enabledCount-failed, failed, len(items))
	if enabledCount == 0 {
		message = "没有启用的提示词源"
	}
	revisionBytes, _ := json.Marshal(struct {
		Revision string
		Enabled  map[string]bool
		Status   map[string]promptSourceStatus
	}{state.RegistryRevision, state.Enabled, state.Status})
	digest := sha256.Sum256(revisionBytes)
	return map[string]any{
		"schema_version": 1, "generated_at": nowISO(), "revision": hex.EncodeToString(digest[:])[:24],
		"registry_revision": state.RegistryRevision, "registry_generated_at": state.RegistryGeneratedAt,
		"synced": cachedCount > 0, "prompt_count": len(items), "source_count": len(sources),
		"enabled_source_count": enabledCount, "cached_source_count": cachedCount, "source_error_count": failed,
		"sync_summary":  map[string]any{"status": summaryStatus, "tone": summaryTone, "total": enabledCount, "succeeded": enabledCount - failed, "failed": failed, "message": message},
		"source_errors": errorsBySource, "items": items, "sources": sources,
	}
}

func (s *Server) handlePromptLibrary(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireIdentity(w, r); !ok {
		return
	}
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	state, err := s.loadPromptState()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取提示词库失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, buildPromptLibraryView(state))
}

func (s *Server) handlePromptSource(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	suffix := strings.TrimPrefix(r.URL.Path, "/api/admin/prompt-sources/")
	refresh := strings.HasSuffix(suffix, "/refresh")
	sourceID := strings.TrimSuffix(suffix, "/refresh")
	if !promptSourceIDPattern.MatchString(sourceID) || r.Method != http.MethodPost {
		writeErr(w, http.StatusBadRequest, "提示词源 ID 或请求方法无效")
		return
	}
	if refresh {
		s.refreshPromptSources(w, r, sourceID)
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if !readBody(w, r, &body) {
		return
	}
	if body.Enabled == nil {
		writeErr(w, http.StatusBadRequest, "缺少 enabled")
		return
	}
	state, err := s.loadPromptState()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	known := false
	for _, source := range state.Sources {
		known = known || source.ID == sourceID
	}
	if !known {
		writeErr(w, http.StatusNotFound, "prompt source not found")
		return
	}
	err = updateDocument(s.store, promptLibraryDocument, state, func(current promptLibraryState) promptLibraryState {
		if current.Enabled == nil {
			current.Enabled = map[string]bool{}
		}
		current.Enabled[sourceID] = *body.Enabled
		return current
	})
	if err == nil {
		state, err = s.loadPromptState()
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, buildPromptLibraryView(state))
}

func (s *Server) handlePromptSourcesRefresh(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	s.refreshPromptSources(w, r, "")
}

func (s *Server) refreshPromptSources(w http.ResponseWriter, r *http.Request, sourceID string) {
	state, err := s.loadPromptState()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if sourceID != "" {
		known := false
		for _, source := range state.Sources {
			known = known || source.ID == sourceID
		}
		if !known {
			writeErr(w, http.StatusNotFound, "prompt source not found")
			return
		}
	}
	started := time.Now()
	proxyURL, err := s.selectedProxy()
	var manifest promptRegistryManifest
	var sources []promptRegistrySource
	var items map[string][]PromptLibraryItem
	if err == nil {
		manifest, sources, items, err = fetchPromptRegistry(r.Context(), strings.TrimSpace(os.Getenv("PROMPT_LIBRARY_REGISTRY_URL")), proxyURL, &state)
		if err != nil && proxyURL != "" {
			err = redactProxyTransportError(err, proxyURL)
		}
	}
	elapsed := time.Since(started).Milliseconds()
	if r.Context().Err() != nil {
		return
	}
	var refreshedSource promptRegistrySource
	if err == nil && sourceID != "" {
		found := false
		for _, source := range sources {
			if source.ID == sourceID {
				refreshedSource, found = source, true
				break
			}
		}
		if !found {
			err = fmt.Errorf("词源 %s 已不在远端清单中", sourceID)
		}
	}
	conflict := false
	if err == nil {
		err = updateDocument(s.store, promptLibraryDocument, state, func(current promptLibraryState) promptLibraryState {
			if !promptRefreshStateMatches(state, current) {
				conflict = true
				return current
			}
			if current.Status == nil {
				current.Status = map[string]promptSourceStatus{}
			}
			if sourceID != "" {
				for index := range current.Sources {
					if current.Sources[index].ID == sourceID {
						current.Sources[index] = refreshedSource
						break
					}
				}
				if current.Items == nil {
					current.Items = map[string][]PromptLibraryItem{}
				}
				current.Items[sourceID] = items[sourceID]
				value := elapsed
				current.Status[sourceID] = promptSourceStatus{ContentRevision: refreshedSource.SHA256, LastSyncAt: nowISO(), LastFetchMS: &value}
				if current.RegistryRevision != manifest.RegistryHash {
					current.RegistryRevision = ""
					current.RegistryGeneratedAt = ""
					current.SnapshotVerified = false
				}
				current.SnapshotDigest = promptSnapshotDigest(current)
				return current
			}
			current.Sources, current.Items = sources, items
			current.RegistryRevision, current.RegistryGeneratedAt = manifest.RegistryHash, manifest.GeneratedAt
			current.SnapshotVerified = true
			for _, source := range sources {
				value := elapsed
				current.Status[source.ID] = promptSourceStatus{ContentRevision: source.SHA256, LastSyncAt: nowISO(), LastFetchMS: &value}
			}
			current.SnapshotDigest = promptSnapshotDigest(current)
			return current
		})
	} else {
		message := err.Error()
		if len(message) > 500 {
			message = message[:500]
		}
		err = updateDocument(s.store, promptLibraryDocument, state, func(current promptLibraryState) promptLibraryState {
			if !promptRefreshStateMatches(state, current) {
				conflict = true
				return current
			}
			if current.Status == nil {
				current.Status = map[string]promptSourceStatus{}
			}
			for _, source := range current.Sources {
				if sourceID != "" && source.ID != sourceID {
					continue
				}
				if sourceID == "" && !promptSourceEnabled(current, source.ID) {
					continue
				}
				status := current.Status[source.ID]
				value := elapsed
				status.LastError, status.LastFetchMS = message, &value
				current.Status[source.ID] = status
			}
			return current
		})
	}
	if err == nil && conflict {
		writeErr(w, http.StatusConflict, "词源库在刷新期间已更新，请重试")
		return
	}
	if err == nil {
		state, err = s.loadPromptState()
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, buildPromptLibraryView(state))
}

type promptRegistryManifest struct {
	SchemaVersion int              `json:"schemaVersion"`
	RegistryHash  string           `json:"registryHash"`
	GeneratedAt   string           `json:"generatedAt"`
	PromptsPath   string           `json:"promptsPath"`
	Total         int              `json:"total"`
	Sources       []map[string]any `json:"sources"`
}

var promptSourceIDPattern = regexp.MustCompile(`^[a-z0-9-]+$`)
var promptHashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func registryPath(value string) (string, error) {
	if value == "" || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") || path.Clean(value) != value || strings.HasPrefix(value, "../") || value == ".." {
		return "", errors.New("invalid registry path")
	}
	for _, char := range value {
		if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-/", char) {
			return "", errors.New("invalid registry path")
		}
	}
	return value, nil
}

func fetchPromptBytes(ctx context.Context, client *http.Client, rawURL string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "chatgpt2api-go-prompt-library/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("词源返回 HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("词源响应超过 %d 字节", limit)
	}
	return data, nil
}

func canonicalPromptJSON(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}

func promptString(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func promptStrings(value any, max int) []string {
	values, _ := value.([]any)
	result := make([]string, 0)
	seen := map[string]bool{}
	for _, raw := range values {
		text := promptString(raw)
		if text != "" && !seen[text] {
			result = append(result, text)
			seen[text] = true
			if len(result) >= max {
				break
			}
		}
	}
	return result
}

func promptCount(value any) (int, bool) {
	number, ok := value.(float64)
	return int(number), ok && number >= 0 && number == float64(int(number))
}

func promptSnapshotComplete(state promptLibraryState, manifest promptRegistryManifest, sources []promptRegistrySource) bool {
	if !state.SnapshotVerified || state.RegistryRevision != manifest.RegistryHash || state.SnapshotDigest == "" || state.SnapshotDigest != promptSnapshotDigest(state) || validatePromptSnapshot(state) != nil {
		return false
	}
	if len(state.Sources) != len(sources) || len(state.Items) != len(sources) {
		return false
	}
	total := 0
	for index, source := range sources {
		if state.Sources[index] != source || len(state.Items[source.ID]) != source.Count || state.Status[source.ID].ContentRevision != source.SHA256 {
			return false
		}
		total += source.Count
	}
	return total == manifest.Total
}

func fetchPromptRegistry(ctx context.Context, configuredBase, proxyURL string, cached *promptLibraryState) (promptRegistryManifest, []promptRegistrySource, map[string][]PromptLibraryItem, error) {
	base := strings.TrimRight(configuredBase, "/")
	if base == "" {
		base = defaultPromptRegistry
	}
	base = strings.TrimSuffix(base, "/manifest.json")
	parsedBase, err := url.Parse(base + "/")
	if err != nil || (parsedBase.Scheme != "http" && parsedBase.Scheme != "https") || parsedBase.Host == "" || parsedBase.User != nil {
		return promptRegistryManifest{}, nil, nil, errors.New("词源仓库地址无效")
	}
	client := &http.Client{Timeout: 120 * time.Second}
	if proxyURL != "" {
		transport, err := newProxyHTTPTransport(proxyURL)
		if err != nil {
			return promptRegistryManifest{}, nil, nil, err
		}
		defer transport.CloseIdleConnections()
		client.Transport = transport
	}
	manifestData, err := fetchPromptBytes(ctx, client, base+"/manifest.json", 128*1024)
	if err != nil {
		return promptRegistryManifest{}, nil, nil, err
	}
	var manifest promptRegistryManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return manifest, nil, nil, err
	}
	if manifest.SchemaVersion != 1 || !promptHashPattern.MatchString(manifest.RegistryHash) || manifest.GeneratedAt == "" || len(manifest.Sources) == 0 {
		return manifest, nil, nil, errors.New("词源清单格式无效")
	}
	promptsPath, err := registryPath(manifest.PromptsPath)
	if err != nil {
		return manifest, nil, nil, err
	}
	sources := make([]promptRegistrySource, 0, len(manifest.Sources))
	seenSources := map[string]bool{}
	total := 0
	for _, raw := range manifest.Sources {
		count, validCount := promptCount(raw["count"])
		source := promptRegistrySource{ID: promptString(raw["id"]), Name: promptString(raw["name"]), Homepage: promptString(raw["homepage"]), UpstreamURL: promptString(raw["upstreamUrl"]), SHA256: promptString(raw["sha256"]), Count: count}
		source.Path, err = registryPath(promptString(raw["path"]))
		if err != nil || !validCount || !promptSourceIDPattern.MatchString(source.ID) || seenSources[source.ID] || source.Name == "" || !promptHashPattern.MatchString(source.SHA256) || promptURL("", source.Homepage) == "" || promptURL("", source.UpstreamURL) == "" {
			return manifest, nil, nil, errors.New("词源清单包含无效来源")
		}
		seenSources[source.ID] = true
		sources = append(sources, source)
		total += count
	}
	if manifest.Total < 0 || manifest.Total != total {
		return manifest, nil, nil, errors.New("词源清单数量不匹配")
	}
	if cached != nil && promptSnapshotComplete(*cached, manifest, sources) {
		return manifest, sources, cached.Items, nil
	}
	payload, err := fetchPromptBytes(ctx, client, base+"/"+promptsPath, 8*1024*1024)
	if err != nil {
		return manifest, nil, nil, err
	}
	canonicalSources, err := canonicalPromptJSON(manifest.Sources)
	if err != nil {
		return manifest, nil, nil, err
	}
	hash := sha256.New()
	_, _ = hash.Write(canonicalSources)
	_, _ = hash.Write(payload)
	if hex.EncodeToString(hash.Sum(nil)) != manifest.RegistryHash {
		return manifest, nil, nil, errors.New("词源清单与内容哈希不匹配")
	}
	var rawItems []map[string]any
	if err := json.Unmarshal(payload, &rawItems); err != nil || len(rawItems) != manifest.Total {
		return manifest, nil, nil, errors.New("词源内容格式或数量无效")
	}
	byID := map[string]promptRegistrySource{}
	items := map[string][]PromptLibraryItem{}
	for _, source := range sources {
		byID[source.ID] = source
		items[source.ID] = []PromptLibraryItem{}
	}
	seenItems := map[string]bool{}
	for _, raw := range rawItems {
		sourceID := promptString(raw["sourceId"])
		source, found := byID[sourceID]
		id, title, text := promptString(raw["id"]), promptString(raw["title"]), promptString(raw["prompt"])
		if !found || !strings.HasPrefix(id, sourceID+":") || len(id) > 160 || seenItems[id] || title == "" || text == "" {
			return manifest, nil, nil, errors.New("词源内容包含无效提示词")
		}
		preview, link := promptURL("", promptString(raw["coverUrl"])), promptURL("", promptString(raw["sourceUrl"]))
		if link == "" {
			return manifest, nil, nil, errors.New("词源内容包含无效来源链接")
		}
		countValue, hasCount := raw["imageCount"]
		var imageCount *int
		if hasCount && countValue != nil {
			count, valid := promptCount(countValue)
			if !valid || count < 1 {
				return manifest, nil, nil, errors.New("词源内容包含无效图片数量")
			}
			imageCount = &count
		}
		tags := promptStrings(raw["tags"], 24)
		category, subCategory := "", ""
		if len(tags) > 0 {
			category = tags[0]
		}
		if len(tags) > 1 {
			subCategory = tags[1]
		}
		item := PromptLibraryItem{
			ID: id, SourceID: sourceID, SourceName: source.Name, Title: title, Prompt: text,
			Description: promptString(raw["description"]), Preview: preview, Link: link,
			Author: promptString(raw["author"]), Category: category, SubCategory: subCategory,
			Tags: tags, ReferenceImageURLs: promptStrings(raw["referenceImageUrls"], 12),
			ImageMode: promptImageMode(promptString(raw["imageMode"])), ImageModel: promptString(raw["imageModel"]),
			ImageSize: promptString(raw["imageSize"]), ImageCount: imageCount, CreatedAt: promptString(raw["createdAt"]),
		}
		for _, reference := range item.ReferenceImageURLs {
			if promptURL("", reference) == "" {
				return manifest, nil, nil, errors.New("词源内容包含无效参考图链接")
			}
		}
		items[sourceID] = append(items[sourceID], item)
		seenItems[id] = true
	}
	for _, source := range sources {
		if len(items[source.ID]) != source.Count {
			return manifest, nil, nil, fmt.Errorf("词源 %s 的数量不匹配", source.ID)
		}
	}
	return manifest, sources, items, nil
}
