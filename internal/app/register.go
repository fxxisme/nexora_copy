package app

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type RegisterMailConfig struct {
	RequestTimeout      int              `json:"request_timeout"`
	WaitTimeout         int              `json:"wait_timeout"`
	WaitInterval        int              `json:"wait_interval"`
	APIUseRegisterProxy bool             `json:"api_use_register_proxy"`
	Providers           []map[string]any `json:"providers"`
}

type RegisterStats struct {
	JobID            string  `json:"job_id,omitempty"`
	JobKind          string  `json:"job_kind,omitempty"`
	Success          int     `json:"success"`
	Fail             int     `json:"fail"`
	Done             int     `json:"done"`
	Running          int     `json:"running"`
	Threads          int     `json:"threads"`
	ElapsedSeconds   float64 `json:"elapsed_seconds,omitempty"`
	AvgSeconds       float64 `json:"avg_seconds,omitempty"`
	SuccessRate      float64 `json:"success_rate,omitempty"`
	CurrentQuota     int     `json:"current_quota,omitempty"`
	CurrentAvailable int     `json:"current_available,omitempty"`
	CPUPercent       float64 `json:"cpu_percent,omitempty"`
	MemoryMB         float64 `json:"memory_mb,omitempty"`
	HeapMB           float64 `json:"heap_mb,omitempty"`
	MemorySysMB      float64 `json:"memory_sys_mb,omitempty"`
	Goroutines       int     `json:"goroutines,omitempty"`
	StartedAt        string  `json:"started_at,omitempty"`
	UpdatedAt        string  `json:"updated_at,omitempty"`
	FinishedAt       string  `json:"finished_at,omitempty"`
}

type RegisterConfig struct {
	Enabled          bool               `json:"enabled"`
	Mail             RegisterMailConfig `json:"mail"`
	Proxy            string             `json:"proxy"`
	Total            int                `json:"total"`
	Threads          int                `json:"threads"`
	Mode             string             `json:"mode"`
	TargetQuota      int                `json:"target_quota"`
	TargetAvailable  int                `json:"target_available"`
	CheckInterval    int                `json:"check_interval"`
	FixedPassword    string             `json:"fixed_password"`
	ProfileName      string             `json:"profile_name,omitempty"`
	ProfileBirthdate string             `json:"profile_birthdate,omitempty"`
	Stats            RegisterStats      `json:"stats"`
	Logs             []registerLogItem  `json:"logs,omitempty"`
	Failures         []registerFailure  `json:"failures,omitempty"`
}

type registerLogItem struct {
	Time  string `json:"time"`
	Text  string `json:"text"`
	Level string `json:"level"`
}

type registerFailure struct {
	Time   string `json:"time"`
	JobID  string `json:"job_id"`
	Task   int    `json:"task"`
	Reason string `json:"reason"`
}

type registerService struct {
	mu                sync.RWMutex
	path              string
	store             *Store
	initErr           error
	cfg               RegisterConfig
	logs              []registerLogItem
	providerIndex     int
	server            *Server
	cancel            context.CancelFunc
	runFunc           func(context.Context)
	passwordStepProbe func(context.Context, RegisterConfig) error
}

func defaultRegisterConfig() RegisterConfig {
	return RegisterConfig{
		Enabled: false,
		Mail: RegisterMailConfig{
			RequestTimeout:      30,
			WaitTimeout:         30,
			WaitInterval:        2,
			APIUseRegisterProxy: true,
			Providers:           []map[string]any{},
		},
		Proxy:           "",
		Total:           10,
		Threads:         3,
		Mode:            "total",
		TargetQuota:     100,
		TargetAvailable: 10,
		CheckInterval:   5,
		FixedPassword:   "",
		Stats: RegisterStats{
			Success:          0,
			Fail:             0,
			Done:             0,
			Running:          0,
			Threads:          3,
			CurrentQuota:     0,
			CurrentAvailable: 0,
		},
	}
}

func normalizeRegisterConfig(raw RegisterConfig) RegisterConfig {
	return normalizeRegisterConfigWithOptions(raw, true)
}

func normalizeRegisterConfigWithOptions(raw RegisterConfig, hasAPIUseRegisterProxy bool) RegisterConfig {
	cfg := defaultRegisterConfig()
	cfg.Enabled = raw.Enabled
	if raw.Proxy != "" {
		cfg.Proxy = strings.TrimSpace(raw.Proxy)
	}
	if raw.Total > 0 {
		cfg.Total = raw.Total
	}
	if raw.Threads > 0 {
		cfg.Threads = raw.Threads
	}
	if mode := strings.TrimSpace(raw.Mode); mode == "quota" || mode == "available" || mode == "total" {
		cfg.Mode = mode
	}
	if raw.TargetQuota > 0 {
		cfg.TargetQuota = raw.TargetQuota
	}
	if raw.TargetAvailable > 0 {
		cfg.TargetAvailable = raw.TargetAvailable
	}
	if raw.CheckInterval > 0 {
		cfg.CheckInterval = raw.CheckInterval
	}
	cfg.FixedPassword = strings.TrimSpace(raw.FixedPassword)
	cfg.ProfileName = strings.TrimSpace(raw.ProfileName)
	cfg.ProfileBirthdate = strings.TrimSpace(raw.ProfileBirthdate)
	if raw.Mail.RequestTimeout > 0 {
		cfg.Mail.RequestTimeout = raw.Mail.RequestTimeout
	}
	if raw.Mail.WaitTimeout > 0 {
		cfg.Mail.WaitTimeout = raw.Mail.WaitTimeout
	}
	if raw.Mail.WaitInterval > 0 {
		cfg.Mail.WaitInterval = raw.Mail.WaitInterval
	}
	if hasAPIUseRegisterProxy {
		cfg.Mail.APIUseRegisterProxy = raw.Mail.APIUseRegisterProxy
	}
	if raw.Mail.Providers != nil {
		cfg.Mail.Providers = raw.Mail.Providers
	}
	cfg.Stats = raw.Stats
	cfg.Failures = raw.Failures
	cfg.Stats.Threads = cfg.Threads
	if cfg.Stats.Threads <= 0 {
		cfg.Stats.Threads = cfg.Threads
	}
	return cfg
}

func registerJSONHasNestedKey(data []byte, parent, key string) bool {
	var raw map[string]any
	if len(data) == 0 || json.Unmarshal(data, &raw) != nil {
		return false
	}
	parentMap, _ := raw[parent].(map[string]any)
	if parentMap == nil {
		return false
	}
	_, ok := parentMap[key]
	return ok
}

func newRegisterService(dataDir string, storage ...*Store) *registerService {
	svc := &registerService{path: filepath.Join(dataDir, "register.json"), cfg: defaultRegisterConfig()}
	if len(storage) > 0 {
		svc.store = storage[0]
	}
	svc.runFunc = svc.run
	svc.passwordStepProbe = svc.probePasswordSignup
	svc.initErr = svc.load()
	return svc
}

func (s *registerService) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.store != nil {
		var raw json.RawMessage
		if err := s.store.LoadDocument("register.json", &raw); err != nil {
			return err
		}
		var cfg RegisterConfig
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &cfg); err != nil {
				return err
			}
		}
		s.cfg = normalizeRegisterConfigWithOptions(cfg, registerJSONHasNestedKey(raw, "mail", "api_use_register_proxy"))
		return nil
	}
	if err := ensureNotDir(s.path); err != nil {
		return err
	}
	var raw RegisterConfig
	hasAPIUseRegisterProxy := false
	if b, err := os.ReadFile(s.path); err == nil && len(b) > 0 {
		hasAPIUseRegisterProxy = registerJSONHasNestedKey(b, "mail", "api_use_register_proxy")
		_ = json.Unmarshal(b, &raw)
	}
	s.cfg = normalizeRegisterConfigWithOptions(raw, hasAPIUseRegisterProxy)
	return nil
}

func (s *registerService) saveLocked() error {
	cfg := s.cfg
	cfg.Logs = nil
	if s.store != nil {
		return s.store.SaveDocument("register.json", cfg)
	}
	return writeJSONFile(s.path, cfg)
}

func (s *registerService) snapshotLocked() RegisterConfig {
	cfg := s.cfg
	cfg.Stats = enrichRegisterRuntimeStats(cfg.Stats)
	cfg.Logs = append([]registerLogItem(nil), s.logs...)
	if len(cfg.Logs) > 300 {
		cfg.Logs = cfg.Logs[len(cfg.Logs)-300:]
	}
	return cfg
}

func (s *registerService) snapshot() RegisterConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshotLocked()
}

func (s *registerService) appendLog(text, level string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logs = append(s.logs, registerLogItem{Time: nowISO(), Text: text, Level: strings.TrimSpace(level)})
	if len(s.logs) > 300 {
		s.logs = s.logs[len(s.logs)-300:]
	}
	_ = s.saveLocked()
}

func (s *registerService) recordFailure(jobID string, task int, err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.Failures = append(s.cfg.Failures, registerFailure{
		Time: nowISO(), JobID: jobID, Task: task, Reason: truncateText(err.Error(), 500),
	})
	if len(s.cfg.Failures) > 300 {
		s.cfg.Failures = s.cfg.Failures[len(s.cfg.Failures)-300:]
	}
	_ = s.saveLocked()
}

func (s *registerService) clearLogsLocked() {
	s.logs = nil
}

func (s *registerService) update(body map[string]any) RegisterConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg := s.cfg
	if v, ok := body["enabled"]; ok {
		cfg.Enabled = boolAny(v, cfg.Enabled)
	}
	if v, ok := body["proxy"]; ok {
		cfg.Proxy = strings.TrimSpace(strAny(v, cfg.Proxy))
	}
	if v, ok := body["total"]; ok {
		cfg.Total = maxInt(intAny(v, cfg.Total), 1)
	}
	if v, ok := body["threads"]; ok {
		cfg.Threads = maxInt(intAny(v, cfg.Threads), 1)
	}
	if v, ok := body["mode"]; ok {
		mode := strings.TrimSpace(strAny(v, cfg.Mode))
		if mode == "quota" || mode == "available" || mode == "total" {
			cfg.Mode = mode
		}
	}
	if v, ok := body["target_quota"]; ok {
		cfg.TargetQuota = maxInt(intAny(v, cfg.TargetQuota), 1)
	}
	if v, ok := body["target_available"]; ok {
		cfg.TargetAvailable = maxInt(intAny(v, cfg.TargetAvailable), 1)
	}
	if v, ok := body["check_interval"]; ok {
		cfg.CheckInterval = maxInt(intAny(v, cfg.CheckInterval), 1)
	}
	if v, ok := body["fixed_password"]; ok {
		cfg.FixedPassword = strings.TrimSpace(strAny(v, cfg.FixedPassword))
	}
	if mv, ok := body["mail"].(map[string]any); ok {
		if v, ok := mv["request_timeout"]; ok {
			cfg.Mail.RequestTimeout = maxInt(intAny(v, cfg.Mail.RequestTimeout), 1)
		}
		if v, ok := mv["wait_timeout"]; ok {
			cfg.Mail.WaitTimeout = maxInt(intAny(v, cfg.Mail.WaitTimeout), 1)
		}
		if v, ok := mv["wait_interval"]; ok {
			cfg.Mail.WaitInterval = maxInt(intAny(v, cfg.Mail.WaitInterval), 1)
		}
		if v, ok := mv["api_use_register_proxy"]; ok {
			cfg.Mail.APIUseRegisterProxy = boolAny(v, cfg.Mail.APIUseRegisterProxy)
		}
		if v, ok := mv["providers"].([]any); ok {
			providers := make([]map[string]any, 0, len(v))
			for _, item := range v {
				if m, ok := item.(map[string]any); ok {
					providers = append(providers, m)
				}
			}
			cfg.Mail.Providers = providers
		}
	}
	cfg.Stats.Threads = cfg.Threads
	s.cfg = normalizeRegisterConfig(cfg)
	_ = s.saveLocked()
	return s.snapshotLocked()
}

func (s *registerService) start(jobKind string) RegisterConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cfg.Enabled = true
		_ = s.saveLocked()
		return s.snapshotLocked()
	}
	cfg := s.cfg
	cfg.Enabled = true
	s.clearLogsLocked()
	cfg.Stats = RegisterStats{
		JobID:            uuid4(),
		JobKind:          strings.TrimSpace(jobKind),
		Success:          0,
		Fail:             0,
		Done:             0,
		Running:          0,
		Threads:          maxInt(cfg.Threads, 1),
		CurrentQuota:     0,
		CurrentAvailable: 0,
		StartedAt:        nowISO(),
		UpdatedAt:        nowISO(),
	}
	s.cfg = cfg
	_ = s.saveLocked()
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	if s.runFunc == nil {
		s.runFunc = s.run
	}
	go s.runFunc(ctx)
	s.logs = append(s.logs, registerLogItem{Time: nowISO(), Text: fmt.Sprintf("注册任务启动，模式=%s，线程数=%d", cfg.Mode, cfg.Threads), Level: "yellow"})
	_ = s.saveLocked()
	return s.snapshotLocked()
}

func (s *registerService) stop() RegisterConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.Enabled = false
	s.cfg.Stats.UpdatedAt = nowISO()
	s.logs = append(s.logs, registerLogItem{Time: nowISO(), Text: "已请求停止注册任务，不再提交新任务，正在等待当前运行任务结束", Level: "yellow"})
	_ = s.saveLocked()
	return s.snapshotLocked()
}

func (s *registerService) reset() RegisterConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logs = nil
	s.cfg.Enabled = false
	s.cfg.Stats = RegisterStats{Threads: maxInt(s.cfg.Threads, 1), UpdatedAt: nowISO()}
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	_ = s.saveLocked()
	return s.snapshotLocked()
}

func (s *registerService) repairAbnormal() RegisterConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	cfg := s.cfg
	cfg.Enabled = true
	s.clearLogsLocked()
	cfg.Stats = RegisterStats{
		JobID:            uuid4(),
		JobKind:          "repair_abnormal",
		Success:          0,
		Fail:             0,
		Done:             0,
		Running:          0,
		Threads:          maxInt(cfg.Threads, 1),
		CurrentQuota:     0,
		CurrentAvailable: 0,
		StartedAt:        nowISO(),
		UpdatedAt:        nowISO(),
	}
	s.cfg = cfg
	_ = s.saveLocked()
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	go s.run(ctx)
	s.logs = append(s.logs, registerLogItem{Time: nowISO(), Text: "已启动异常账号修复任务", Level: "yellow"})
	_ = s.saveLocked()
	return s.snapshotLocked()
}

func (s *registerService) run(ctx context.Context) {
	if s.snapshot().Stats.JobKind == "repair_abnormal" {
		s.runRepairAbnormalJob(ctx)
		return
	}
	s.runRegisterJob(ctx)
}

func (s *Server) maybeStartRegister() {
	if s == nil || s.register == nil {
		return
	}
	if s.register.snapshot().Enabled {
		s.register.start("register")
	}
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, 200, map[string]any{"register": s.register.snapshot()})
	case http.MethodPost:
		var body map[string]any
		if !readBody(w, r, &body) {
			return
		}
		writeJSON(w, 200, map[string]any{"register": s.register.update(body)})
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func (s *Server) handleRegisterStart(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	writeJSON(w, 200, map[string]any{"register": s.register.start("register")})
}

func (s *Server) handleRegisterStop(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	writeJSON(w, 200, map[string]any{"register": s.register.stop()})
}

func (s *Server) handleRegisterReset(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	writeJSON(w, 200, map[string]any{"register": s.register.reset()})
}

func (s *Server) handleRegisterOutlookPoolReset(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	scope := strings.TrimSpace(strAny(body["scope"], "all"))
	if scope == "" {
		scope = "all"
	}
	removed := s.register.resetOutlookPoolState(scope)
	s.register.appendLog(fmt.Sprintf("Outlook 邮箱池状态已重置，scope=%s，清理=%d", scope, removed), "yellow")
	writeJSON(w, 200, map[string]any{"register": s.register.snapshot(), "removed": removed})
}

func (s *Server) handleRegisterRepairAbnormal(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	writeJSON(w, 200, map[string]any{"register": s.register.repairAbnormal()})
}

func (s *Server) handleRegisterEvents(w http.ResponseWriter, r *http.Request) {
	if !s.requireRegisterEventsAdmin(w, r) {
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, "stream not supported")
		return
	}
	last := ""
	if payload, err := json.Marshal(s.register.snapshot()); err == nil {
		last = string(payload)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
		flusher.Flush()
	}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			payload, _ := json.Marshal(s.register.snapshot())
			if string(payload) == last {
				continue
			}
			last = string(payload)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		}
	}
}

func (s *Server) requireRegisterEventsAdmin(w http.ResponseWriter, r *http.Request) bool {
	if strings.TrimSpace(r.Header.Get("Authorization")) != "" {
		_, ok := s.requireAdmin(w, r)
		return ok
	}
	token := firstNonEmpty(
		strings.TrimSpace(r.URL.Query().Get("token")),
		strings.TrimSpace(r.URL.Query().Get("auth_key")),
		strings.TrimSpace(r.URL.Query().Get("key")),
	)
	if token == "" {
		writeErr(w, 401, "密钥无效或已失效，请重新登录")
		return false
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg.AuthKey)) == 1 {
		return true
	}
	h := hashKey(token)
	for _, k := range s.store.LoadAuthKeys() {
		if !k.Enabled || k.Role != "admin" || k.KeyHash == "" {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(k.KeyHash), []byte(h)) == 1 {
			return true
		}
	}
	writeErr(w, 403, "需要管理员权限才能执行这个操作")
	return false
}
