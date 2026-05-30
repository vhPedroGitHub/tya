package ui

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	runflowengine "github.com/vhPedroGitHub/tya/pkg/runFlowEngine"
	"go.uber.org/zap"
)

type dashboardUpdate struct {
	Name   string
	Report runflowengine.FlowReport
}

var (
	mu        sync.Mutex
	updatesCh chan dashboardUpdate
	stopCh    chan struct{}
	started   bool
	logger    *zap.Logger
)

// StartDashboard initialises the live dashboard and registers an update
// callback with the runflowengine package. It returns an error if the
// dashboard is already running.
func StartDashboard(log *zap.Logger) error {
	mu.Lock()
	defer mu.Unlock()
	if started {
		return nil
	}
	logger = log
	updatesCh = make(chan dashboardUpdate, 256)
	stopCh = make(chan struct{})
	// hide cursor to avoid flicker
	fmt.Print("\033[?25l")

	// Register update callback
	runflowengine.RegisterUpdateFunc(func(flowName string, r runflowengine.FlowReport) {
		select {
		case updatesCh <- dashboardUpdate{Name: flowName, Report: r}:
		default:
			// drop if buffer full
		}
	})

	go renderLoop()
	started = true
	return nil
}

// StopDashboard stops the live dashboard and clears the registered callback.
func StopDashboard() {
	mu.Lock()
	if !started {
		mu.Unlock()
		return
	}
	// restore cursor
	fmt.Print("\033[?25h")
	close(stopCh)
	started = false
	runflowengine.ClearUpdateFunc()
	close(updatesCh)
	mu.Unlock()
}

func renderLoop() {
	reports := map[string]runflowengine.FlowReport{}
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	// CPU accounting: keep previous ticks to compute percent
	var prevProcTicks uint64
	var prevTotalTicks uint64
	// attempt initial read
	if p, t, err := readProcTimes(); err == nil {
		prevProcTicks = p
		prevTotalTicks = t
	}

	for {
		select {
		case u, ok := <-updatesCh:
			if !ok {
				return
			}
			r := u.Report
			r.Name = u.Name
			reports[u.Name] = r
		case <-ticker.C:
			// compute resource usage
			cpuPercent := 0.0
			if p, t, err := readProcTimes(); err == nil {
				if prevTotalTicks > 0 && t > prevTotalTicks {
					cpuPercent = float64(p-prevProcTicks) / float64(t-prevTotalTicks) * 100.0
				}
				prevProcTicks = p
				prevTotalTicks = t
			}

			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			res := struct {
				CPUPercent float64
				AllocBytes uint64
				SysBytes   uint64
				Goroutines int
			}{
				CPUPercent: cpuPercent,
				AllocBytes: m.Alloc,
				SysBytes:   m.Sys,
				Goroutines: runtime.NumGoroutine(),
			}

			draw(reports, res)
		case <-stopCh:
			return
		}
	}
}

// readProcTimes returns (procTicks, totalTicks, error) by parsing
// /proc/self/stat and /proc/stat. Works on Linux.
func readProcTimes() (uint64, uint64, error) {
	// proc self stat
	data, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0, 0, err
	}
	s := string(data)
	// find the closing parenthesis of the comm field
	idx := strings.LastIndex(s, ")")
	if idx < 0 {
		return 0, 0, fmt.Errorf("unexpected stat format")
	}
	fields := strings.Fields(s[idx+2:])
	if len(fields) < 15 {
		return 0, 0, fmt.Errorf("unexpected stat fields")
	}
	utime, err := strconv.ParseUint(fields[11], 10, 64)
	if err != nil {
		return 0, 0, err
	}
	stime, err := strconv.ParseUint(fields[12], 10, 64)
	if err != nil {
		return 0, 0, err
	}
	procTicks := utime + stime

	// total CPU stat
	f, err := os.Open("/proc/stat")
	if err != nil {
		return procTicks, 0, err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	line, err := r.ReadString('\n')
	if err != nil {
		return procTicks, 0, err
	}
	parts := strings.Fields(line)
	var total uint64
	for _, p := range parts[1:] {
		v, err := strconv.ParseUint(p, 10, 64)
		if err != nil {
			continue
		}
		total += v
	}
	return procTicks, total, nil
}

func draw(reports map[string]runflowengine.FlowReport, res struct {
	CPUPercent float64
	AllocBytes uint64
	SysBytes   uint64
	Goroutines int
}) {
	// move cursor home and clear screen
	fmt.Print("\033[H\033[2J")
	fmt.Printf("TYA Live Dashboard — flows (updates every second)\033[K\n")
	fmt.Printf("-----------------------------------------------------------------\033[K\n")

	// show resource usage
	fmt.Printf("CPU: %5.1f%%  Goroutines: %d  Mem(alloc): %.2fMB  Mem(sys): %.2fMB\033[K\n",
		res.CPUPercent, res.Goroutines, float64(res.AllocBytes)/1024.0/1024.0, float64(res.SysBytes)/1024.0/1024.0)
	fmt.Printf("-----------------------------------------------------------------\033[K\n")

	// sort names for stable display
	names := make([]string, 0, len(reports))
	for n := range reports {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, n := range names {
		r := reports[n]
		fmt.Printf("%-24s  RPS: %6.2f  Req: %6d  Fail: %6d  Concurrency: %3d  Sem: %2d/%2d  p50: %6.2fms  p95: %6.2fms\033[K\n",
			n, r.RPSAchieved, r.TotalRequests, r.FailedRequests, r.CurrentConcurrency, r.SemaphoreInUse, r.SemaphoreCapacity, r.LatencyMS.P50, r.LatencyMS.P95)
		// show global bucket usage for this flow if present
		if u, ok := r.GlobalBucketUsage[n]; ok {
			fmt.Printf("   GlobalBucket: scalars=%d  list_items=%d\033[K\n", u.Scalars, u.ListItems)
		}
		// show per-step brief metrics
		for _, s := range r.Steps {
			fmt.Printf("   - %-20s req:%6d  err:%4d  p95:%6.2fms\033[K\n",
				s.StepID, s.Requests, s.Errors, s.LatencyMS.P95)
		}
		fmt.Println()
	}
}
