package app

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dop251/goja"
)

//go:embed sentinel_goja.js
var sentinelGojaScript string

type gojaSentinelSolver struct {
	client *UpstreamClient
}

func (s *gojaSentinelSolver) Identity(context.Context) (sentinelIdentity, error) {
	zone := localTimezoneName()
	return sentinelIdentity{
		UserAgent: s.client.userAgent, Timezone: zone,
		DateString: (&UpstreamClient{timezone: zone}).powDate(),
		ScreenSize: 1400, HeapLimit: 4395630592,
		Language: "zh-CN", Languages: "zh-CN,zh;q=0.9,en;q=0.8",
		HardwareConcurrency: 8, Vendor: "Google Inc.",
	}, nil
}

func (s *gojaSentinelSolver) Turnstile(ctx context.Context, _ map[string]any, p, dx string) (string, error) {
	if dx == "" {
		return "", errors.New("missing turnstile challenge")
	}
	runCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	vm := goja.New()
	stop := context.AfterFunc(runCtx, func() { vm.Interrupt(runCtx.Err()) })
	defer stop()
	started := time.Now()
	if err := vm.Set("__sentinelInput", map[string]any{
		"dx": dx, "p": p, "user_agent": s.client.userAgent, "scripts": s.client.scriptSources,
	}); err != nil {
		return "", err
	}
	vm.Set("__performanceNow", func() float64 { return float64(time.Since(started).Nanoseconds()) / 1e6 })
	vm.Set("atob", func(value string) (string, error) {
		data, err := base64.StdEncoding.DecodeString(value)
		if err != nil {
			return "", err
		}
		var decoded strings.Builder
		for _, b := range data {
			decoded.WriteRune(rune(b))
		}
		return decoded.String(), nil
	})
	vm.Set("btoa", func(value string) string {
		data := make([]byte, 0, len(value))
		for _, r := range value {
			data = append(data, byte(r))
		}
		return base64.StdEncoding.EncodeToString(data)
	})
	vm.Set("__randomBytes", func(size int) ([]byte, error) {
		if size < 0 || size > 1<<20 {
			return nil, fmt.Errorf("invalid random byte count: %d", size)
		}
		data := make([]byte, size)
		_, err := rand.Read(data)
		return data, err
	})
	var timeoutCallback goja.Callable
	var timeoutAt time.Time
	vm.Set("setTimeout", func(call goja.FunctionCall) goja.Value {
		timeoutCallback, _ = goja.AssertFunction(call.Argument(0))
		timeoutAt = time.Now().Add(time.Duration(call.Argument(1).ToInteger()) * time.Millisecond)
		return vm.ToValue(1)
	})
	vm.Set("clearTimeout", func(goja.FunctionCall) goja.Value {
		timeoutCallback = nil
		return goja.Undefined()
	})
	if _, err := vm.RunString(sentinelGojaScript); err != nil {
		return "", fmt.Errorf("Goja page setup: %w", err)
	}
	if _, err := vm.RunString(sentinelVMScript); err != nil {
		return "", fmt.Errorf("Goja turnstile VM setup: %w", err)
	}
	result, err := vm.RunString(`window.__sentinelVM.turnstile(__sentinelInput.dx, __sentinelInput.p)`)
	if err != nil {
		return "", fmt.Errorf("Goja turnstile VM: %w", err)
	}
	promise, ok := result.Export().(*goja.Promise)
	if !ok {
		return "", errors.New("Goja turnstile VM returned no promise")
	}
	for i := 0; i < 4 && promise.State() == goja.PromiseStatePending; i++ {
		if _, err := vm.RunString("void 0"); err != nil {
			return "", fmt.Errorf("Goja turnstile VM jobs: %w", err)
		}
	}
	if promise.State() == goja.PromiseStatePending && timeoutCallback != nil {
		wait := time.Until(timeoutAt)
		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-timer.C:
			case <-runCtx.Done():
				timer.Stop()
				return "", runCtx.Err()
			}
		}
		if _, err := timeoutCallback(goja.Undefined()); err != nil {
			return "", fmt.Errorf("Goja turnstile VM timer: %w", err)
		}
	}
	if promise.State() == goja.PromiseStateRejected {
		return "", fmt.Errorf("Goja turnstile VM: %s", truncateText(promise.Result().String(), 300))
	}
	if promise.State() != goja.PromiseStateFulfilled {
		return "", errors.New("Goja turnstile VM did not complete")
	}
	token := promise.Result().String()
	if reason := invalidSentinelToken(token, p); reason != "" {
		return "", fmt.Errorf("Goja turnstile VM: %s", reason)
	}
	return token, nil
}
