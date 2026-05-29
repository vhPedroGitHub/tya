package runflowengine

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vhPedroGitHub/tya/pkg/configyml"
	"go.uber.org/zap"
)

// BestWindowsRPS computes the average of the best-k stable window RPS values
// closest to the targetRPS. Mirrors the inline implementation used in the
// ramp-up engine.
func BestWindowsRPS(stableWindowRPS []float64, rampCfg configyml.RampUp, prevWindowRPS, targetRPS float64) float64 {
	if len(stableWindowRPS) == 0 {
		return prevWindowRPS
	}
	sorted := make([]float64, len(stableWindowRPS))
	copy(sorted, stableWindowRPS)
	k := rampCfg.BestWindowsAvg
	for i := 0; i < len(sorted)-1; i++ {
		for j := i + 1; j < len(sorted); j++ {
			if abs64(sorted[j]-targetRPS) < abs64(sorted[i]-targetRPS) {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	if k > len(sorted) {
		k = len(sorted)
	}
	sum := 0.0
	for _, v := range sorted[:k] {
		sum += v
	}
	return sum / float64(k)
}

// RecordResult centralises metrics recording used by the execute-flow engine.
func RecordResult(id string, res stepResult,
	totalRequests *int64, totalErrors *int64,
	allLatsMu *sync.Mutex, allLats *[]time.Duration,
	stepBuckets map[string]*stepMetricsBucket,
	errByStatus map[string]int64, errByStep map[string]int64,
	errMu *sync.Mutex) {

	failed := res.Err != nil || res.StatusCode >= 400
	atomic.AddInt64(totalRequests, 1)
	if failed {
		atomic.AddInt64(totalErrors, 1)
	}
	allLatsMu.Lock()
	*allLats = append(*allLats, res.Latency)
	allLatsMu.Unlock()
	stepBuckets[id].record(res.Latency, failed)
	if failed {
		errMu.Lock()
		if res.StatusCode > 0 {
			errByStatus[fmt.Sprintf("%d", res.StatusCode)]++
		}
		errByStep[id]++
		errMu.Unlock()
	}
}

// SpawnIterationSimple runs a single flow iteration (synchronously). It is
// intended to replace an inline closure used in some engine variants. It uses
// package-level helpers like executeStep and applyExtracts.
func SpawnIterationSimple(ctx context.Context, log *zap.Logger, totalLaunches *int64, baseURL string, bucket *GlobalBucket,
	flow configyml.Flow, authMap map[string]configyml.AuthProfile,
	lastCtx *FlowContext, lastCtxMu *sync.Mutex,
	totalIterations *int64, recordResultFunc func(string, stepResult)) time.Duration {

	startTime := time.Now()
	atomic.AddInt64(totalLaunches, 1)
	fCtx := FlowContext{"_base_url": baseURL}
	// attach run context so executeStep can cancel HTTP requests via req.WithContext
	if ctx != nil {
		fCtx["_run_ctx"] = ctx
	}
	fCtx["global"] = bucket.Snapshot()
	fCtx["global_lists"] = bucket.SnapshotLists()
	if flow.Auth != "" {
		if auth, ok := authMap[flow.Auth]; ok {
			acquireToken(log, auth, baseURL, fCtx)
		}
	}
	iterOK := true
	for _, step := range flow.Steps {
		id := stepID(step)
		res := executeStep(log, step, fCtx, authMap[flow.Auth])
		recordResultFunc(id, res)
		if res.Err != nil || res.StatusCode >= 400 {
			iterOK = false
		} else {
			applyExtracts(step.Extract, res.Body, res.RequestBody, fCtx, flow.Name, bucket)
		}
	}
	if iterOK {
		lastCtxMu.Lock()
		*lastCtx = copyContext(fCtx)
		lastCtxMu.Unlock()
	}
	atomic.AddInt64(totalIterations, 1)

	duration := time.Since(startTime)

	return duration
}
