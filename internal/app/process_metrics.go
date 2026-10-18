package app

import (
	"runtime"
	"sync"
	"syscall"
	"time"
)

var processMetricsState = struct {
	mu      sync.Mutex
	lastAt  time.Time
	lastCPU float64
}{}

func enrichRegisterRuntimeStats(stats RegisterStats) RegisterStats {
	cpuPercent, memMB, heapMB, memSysMB, goroutines := sampleProcessMetrics()
	stats.CPUPercent = round1(cpuPercent)
	stats.MemoryMB = round1(memMB)
	stats.HeapMB = round1(heapMB)
	stats.MemorySysMB = round1(memSysMB)
	stats.Goroutines = goroutines
	return stats
}

func sampleProcessMetrics() (cpuPercent, memoryMB, heapMB, memorySysMB float64, goroutines int) {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	heapMB = bytesToMB(mem.Alloc)
	memoryMB = bytesToMB(mem.Sys)
	memorySysMB = bytesToMB(mem.Sys)
	goroutines = runtime.NumGoroutine()

	now := time.Now()
	currentCPU := currentProcessCPUSeconds()
	processMetricsState.mu.Lock()
	defer processMetricsState.mu.Unlock()

	if !processMetricsState.lastAt.IsZero() {
		wall := now.Sub(processMetricsState.lastAt).Seconds()
		cpuDelta := currentCPU - processMetricsState.lastCPU
		if wall > 0 && cpuDelta >= 0 {
			cpuPercent = cpuDelta / wall / float64(runtime.NumCPU()) * 100
		}
	}
	processMetricsState.lastAt = now
	processMetricsState.lastCPU = currentCPU
	return cpuPercent, memoryMB, heapMB, memorySysMB, goroutines
}

func currentProcessCPUSeconds() float64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	return timevalSeconds(ru.Utime) + timevalSeconds(ru.Stime)
}

func timevalSeconds(tv syscall.Timeval) float64 {
	return float64(tv.Sec) + float64(tv.Usec)/1_000_000
}

func bytesToMB(v uint64) float64 {
	return float64(v) / 1024 / 1024
}
