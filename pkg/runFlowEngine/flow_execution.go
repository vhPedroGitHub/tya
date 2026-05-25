package runflowengine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vhPedroGitHub/tya/pkg/configyml"
	"github.com/vhPedroGitHub/tya/pkg/models"
	"go.uber.org/zap"
)

// executeFlow runs a single flow according to its type and options.
// It returns a FlowReport and the last successful execution context
// (used by wire-flow children).
func ExecuteFlow(
	log *zap.Logger,
	flow configyml.Flow,
	authMap map[string]configyml.AuthProfile,
	opts *models.RunOptions,
	baseURL string,
	bucket *GlobalBucket,
) (FlowReport, FlowContext) {

	duration := 30 * time.Second
	if d, err := time.ParseDuration(flow.Duration); err == nil {
		duration = d
	}
	rps := flow.RequestsPerSecond

	// A flow runs in single-pass (test) mode when:
	//   - the --test / -t flag is set globally, OR
	//   - requests_per_second is 0 or not configured, OR
	//   - the flow type is "alone" and neither duration nor rps is configured
	//     (treat as a one-shot utility flow).
	aloneNoConfig := strings.EqualFold(flow.Type, "alone") && flow.Duration == "" && rps <= 0
	testMode := opts.TestMode || rps <= 0 || aloneNoConfig

	// Per-step metric accumulators.
	stepBuckets := make(map[string]*stepMetricsBucket, len(flow.Steps))
	for _, s := range flow.Steps {
		stepBuckets[stepID(s)] = &stepMetricsBucket{}
	}

	// Global counters.
	// totalRequests counts individual HTTP calls; totalIterations counts full flow executions.
	var totalLaunches, totalRequests, totalErrors, totalIterations int64
	var allLatsMu sync.Mutex
	var allLats []time.Duration

	// errorsByStatus and errorsByStep for the report.
	errByStatus := make(map[string]int64)
	errByStep := make(map[string]int64)
	var errMu sync.Mutex

	// thinkTime tracking (load mode only).
	var thinkTimeSamples []time.Duration
	var thinkTimeMu sync.Mutex

	// extraReportFields is set by the adaptive engine to populate the new report
	// fields; it is a no-op in test mode.
	extraReportFields := func(_ *FlowReport) {}

	// rpsAchieved is computed after the run; the adaptive engine overrides it.
	var rpsAchieved float64

	// lastCtx captures the final execution context from the last successful
	// iteration; used by wire-flow children.
	var lastCtxMu sync.Mutex
	var lastCtx FlowContext

	recordResult := func(id string, res stepResult) {
		failed := res.Err != nil || res.StatusCode >= 400
		atomic.AddInt64(&totalRequests, 1)
		if failed {
			atomic.AddInt64(&totalErrors, 1)
		}
		allLatsMu.Lock()
		allLats = append(allLats, res.Latency)
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

	if testMode {
		// Single-pass: one sequential execution.
		fCtx := FlowContext{"_base_url": baseURL}
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
			res := executeStep(log, step, fCtx, authMap[flow.Auth], context.Background())
			recordResult(id, res)
			if res.Err != nil || res.StatusCode >= 400 {
				iterOK = false
				log.Warn("step failed",
					zap.String("step", id),
					zap.Int("status", res.StatusCode),
					zap.Error(res.Err),
				)
			} else {
				applyExtracts(step.Extract, res.Body, res.RequestBody, fCtx, flow.Name, bucket)
			}
		}
		if iterOK {
			lastCtxMu.Lock()
			snap := copyContext(fCtx)
			lastCtx = snap
			lastCtxMu.Unlock()
		}
		atomic.AddInt64(&totalIterations, 1)
	} else {
		// -----------------------------------------------------------------------
		// Adaptive load engine — four phases:
		//   1. Ramp-up   : multiplicative ticker growth per step window
		//   2. Plateau   : N consecutive step windows within stability threshold
		//   3. Analysis  : stable window; duration timer + metrics live here
		//   4. Drain     : wg.Wait() after context expires
		// -----------------------------------------------------------------------

		rampCfg := configyml.RampUp{}
		if flow.RampUp != nil {
			rampCfg = *flow.RampUp
		}
		rampCfg = rampCfg.Resolve()

		stepWin, err := time.ParseDuration(rampCfg.StepWindow)
		if err != nil {
			stepWin = 2 * time.Second
		}
		maxRampDur, err := time.ParseDuration(rampCfg.MaxRampDuration)
		if err != nil {
			maxRampDur = 600 * time.Second
		}

		// nSteps is used to convert between iteration rate and HTTP-call rate.
		// ticker fires at (currentRPS / nSteps) iterations/s so that the
		// actual HTTP call rate equals currentRPS calls/s.
		nSteps := float64(len(flow.Steps))
		if nSteps < 1 {
			nSteps = 1
		}

		currentRPS := rampCfg.InitialRPS
		targetRPS := flow.RequestsPerSecond

		// tickInterval returns the ticker period required to produce the given
		// HTTP-calls/s rate with nSteps per iteration.
		tickInterval := func(httpRPS float64) time.Duration {
			return time.Duration(float64(time.Second) * nSteps / httpRPS)
		}

		// Semaphore: cap concurrent goroutines to avoid unbounded accumulation.
		// Initial cap based on targetRPS (HTTP calls/s) converted to iterations/s.
		semCap := max(int((targetRPS/nSteps)*10)+1, 8)
		sem := make(chan struct{}, semCap)

		// concurrency tracking
		var concurrencySamples []int64
		var concurrencyMu sync.Mutex
		var activeConcurrency int64

		sampleConcurrency := func(n int64) {
			concurrencyMu.Lock()
			concurrencySamples = append(concurrencySamples, n)
			concurrencyMu.Unlock()
		}

		var rampWg sync.WaitGroup

		spawnIteration := func(ctx context.Context, winWg *sync.WaitGroup) {
			if winWg != nil {
				winWg.Add(1)
			}
			select {
			case sem <- struct{}{}:
			default:
				// semaphore full — drop this tick to avoid runaway goroutines
				winWg.Done()

				log.Warn("semaphore-full",
					zap.Int("semaphorecap", cap(sem)),
				)
				return
			}
			atomic.AddInt64(&totalLaunches, 1)
			cur := atomic.AddInt64(&activeConcurrency, 1)
			sampleConcurrency(cur)
			rampWg.Add(1)
			go func() {
				defer func() {
					<-sem
					atomic.AddInt64(&activeConcurrency, -1)
					if winWg != nil {
						winWg.Done()
					}
					rampWg.Done()
				}()

				select {
				case <-ctx.Done():
					return
				default:
				}

				fCtx := FlowContext{"_base_url": baseURL}
				fCtx["global"] = bucket.Snapshot()
				fCtx["global_lists"] = bucket.SnapshotLists()
				if flow.Auth != "" {
					if auth, ok := authMap[flow.Auth]; ok {
						acquireToken(log, auth, baseURL, fCtx)
					}
				}
				iterOK := true
				for _, step := range flow.Steps {
					// Cancelar entre steps si el contexto expiró
					select {
					case <-ctx.Done():
						return
					default:
					}

					id := stepID(step)
					res := executeStep(log, step, fCtx, authMap[flow.Auth], ctx)
					recordResult(id, res)
					if res.Err != nil || res.StatusCode >= 400 {
						iterOK = false
					} else {
						applyExtracts(step.Extract, res.Body, res.RequestBody, fCtx, flow.Name, bucket)
					}
				}
				if iterOK {
					lastCtxMu.Lock()
					lastCtx = copyContext(fCtx)
					lastCtxMu.Unlock()
				}

				atomic.AddInt64(&totalIterations, 1)
				fmt.Printf("Total iteration = %d\n", totalIterations)
			}()
		}

		// ── Phase 1 + 2: Ramp-up and plateau detection ──────────────────────

		rampStart := time.Now()
		stableWindows := 0
		prevWindowRPS := 0.0
		maxReached := false

		// Forced-plateau tracking.
		forcedPlateau := false
		forcedPlateauReason := ""
		forcedPlateauRPS := 0.0
		negativeResets := 0  // total negative resets observed
		consecNegResets := 0 // consecutive negative resets (resets on drop)
		var rampWindows []RampUpWindow
		// stableWindowRPS holds the observed RPS of every stable window,
		// used to compute the best-N average on a forced plateau.
		var stableWindowRPS []float64

		rampTimeout := time.NewTimer(maxRampDur)
		defer rampTimeout.Stop()

		// bestWindowsRPS returns the average of the top-K stable window RPS
		// values (closest to targetRPS). Falls back to the overall average if
		// fewer than K stable windows were recorded.
		bestWindowsRPS := func() float64 {
			if len(stableWindowRPS) == 0 {
				return prevWindowRPS
			}
			// Sort descending by closeness to target (minimise |rps - target|).
			sorted := make([]float64, len(stableWindowRPS))
			copy(sorted, stableWindowRPS)
			k := rampCfg.BestWindowsAvg
			// Simple selection of best k by proximity to target.
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

		winIdx := 0
	rampLoop:
		for {
			// Check ramp-up timeout before starting the next window.
			select {
			case <-rampTimeout.C:
				forcedPlateau = true
				forcedPlateauReason = "timeout"
				forcedPlateauRPS = bestWindowsRPS()
				currentRPS = forcedPlateauRPS
				log.Warn("ramp-up: timeout reached, forcing plateau",
					zap.Float64("max_ramp_duration_s", maxRampDur.Seconds()),
					zap.Float64("forced_plateau_rps", forcedPlateauRPS),
				)
				break rampLoop
			default:
			}

			winIdx++
			windowItersBefore := atomic.LoadInt64(&totalIterations)

			winCtx, winCancel := context.WithCancel(context.Background())
			var winWg sync.WaitGroup

			ticker := time.NewTicker(tickInterval(currentRPS))
			winTimer := time.NewTimer(stepWin)
			fmt.Printf("new ticker: %f , windows timer: %f\n", float64(time.Second)*nSteps/currentRPS, stepWin.Seconds())
		rampWindow:
			for {
				select {
				case <-winTimer.C:
					ticker.Stop()
					winCancel()
					break rampWindow
				case <-ticker.C:
					fmt.Printf("total concurrency now: %d\n", activeConcurrency)
					spawnIteration(winCtx, &winWg)
				}
			}
			winTimer.Stop()

			// Wait for this window's goroutines to finish (they should cancel quickly
			// because winCancel() signalled their context). This ensures windowRPS
			// measures completed iterations only.
			winWg.Wait()

			windowIters := atomic.LoadInt64(&totalIterations) - windowItersBefore
			// Convert iterations to HTTP calls/s for comparison against targetRPS.
			windowRPS := float64(windowIters) * nSteps / stepWin.Seconds()

			log.Info("ramp-up: window result",
				zap.Int("window", winIdx),
				zap.Int("window_iteration_before", int(windowItersBefore)),
				zap.Int("total_iterations", int(totalIterations)),
				zap.Int("window_iters", int(windowIters)),
				zap.Float64("window_rps", windowRPS),
			)

			// ── Stability and negative-reset analysis ──────────────────────
			variation := 0.0
			isStable := false
			isNegReset := false

			if prevWindowRPS > 0 {
				variation = (windowRPS - prevWindowRPS) / prevWindowRPS // signed
				absVar := abs64(variation)
				isNegReset = windowRPS < prevWindowRPS
				isStable = absVar <= rampCfg.StabilityThreshold

				if isStable {
					stableWindows++
					stableWindowRPS = append(stableWindowRPS, windowRPS)
					consecNegResets = 0
				} else {
					stableWindows = 0
					if isNegReset {
						negativeResets++
						consecNegResets++
						log.Warn("ramp-up: negative reset detected",
							zap.Int("window", winIdx),
							zap.Float64("prev_rps", prevWindowRPS),
							zap.Float64("current_rps", windowRPS),
							zap.Int("consecutive_negative_resets", consecNegResets),
						)
					} else {
						consecNegResets = 0
					}
				}
			}

			win := RampUpWindow{
				WindowIndex:               winIdx,
				TargetRPS:                 currentRPS,
				ObservedRPS:               windowRPS,
				Variation:                 variation,
				Stable:                    isStable,
				NegativeReset:             isNegReset,
				ConsecutiveNegativeResets: consecNegResets,
			}
			rampWindows = append(rampWindows, win)

			log.Info("ramp-up window",
				zap.Int("window", winIdx),
				zap.Float64("current_http_rps_target", currentRPS),
				zap.Float64("observed_http_rps", windowRPS),
				zap.Float64("target_http_rps", targetRPS),
				zap.Bool("stable", isStable),
				zap.Bool("negative_reset", isNegReset),
				zap.Int("consecutive_negative_resets", consecNegResets),
				zap.Int("stable_windows", stableWindows),
				zap.Int("total_negative_resets", negativeResets),
				zap.Float64("stability_threshold", rampCfg.StabilityThreshold),
				zap.Int("total_iterations", int(totalIterations)),
			)

			prevWindowRPS = windowRPS

			// ── Forced plateau: too many accumulated negative resets ───────
			if negativeResets >= rampCfg.MaxNegativeResets {
				forcedPlateau = true
				forcedPlateauReason = "negative_resets"
				forcedPlateauRPS = bestWindowsRPS()
				currentRPS = forcedPlateauRPS
				log.Warn("ramp-up: max negative resets reached, forcing plateau",
					zap.Int("total_negative_resets", negativeResets),
					zap.Float64("forced_plateau_rps", forcedPlateauRPS),
				)
				break rampLoop
			}

			// ── Natural plateau ─────────────────────────────────────────────
			if stableWindows >= rampCfg.StabilityWindows {
				if windowRPS < targetRPS*0.95 {
					maxReached = true
					log.Warn("ramp-up: target RPS unreachable, running at max achievable",
						zap.Float64("target_rps", targetRPS),
						zap.Float64("achieved_rps", windowRPS),
					)
				}
				break rampLoop
			}

			// Grow towards target.
			nextRPS := currentRPS * rampCfg.Factor
			if nextRPS >= targetRPS {
				nextRPS = targetRPS
				// Give it one more window at target before declaring plateau.
				stableWindows = rampCfg.StabilityWindows - 1
			}
			currentRPS = nextRPS

			// Recalculate semaphore cap using observed p95 latency estimate.
			allLatsMu.Lock()
			latSnap := make([]time.Duration, len(allLats))
			copy(latSnap, allLats)
			allLatsMu.Unlock()
			if len(latSnap) > 0 {
				stats := computeLatencyStats(latSnap)
				_ = stats // p95 available for future dynamic semaphore tuning
			}
		}

		rampDuration := time.Since(rampStart)

		rampWg.Wait()

		// Reset analysis-window metrics.
		atomic.StoreInt64(&totalRequests, 0)
		atomic.StoreInt64(&totalErrors, 0)
		atomic.StoreInt64(&totalIterations, 0)
		allLatsMu.Lock()
		allLats = allLats[:0]
		allLatsMu.Unlock()
		for _, b := range stepBuckets {
			atomic.StoreInt64(&b.requests, 0)
			atomic.StoreInt64(&b.errors, 0)
			b.mu.Lock()
			b.latencies = b.latencies[:0]
			b.mu.Unlock()
		}
		errMu.Lock()
		for k := range errByStatus {
			delete(errByStatus, k)
		}
		for k := range errByStep {
			delete(errByStep, k)
		}
		errMu.Unlock()
		concurrencyMu.Lock()
		concurrencySamples = concurrencySamples[:0]
		concurrencyMu.Unlock()

		log.Info("plateau reached — starting analysis window",
			zap.Float64("ramp_up_duration_s", rampDuration.Seconds()),
			zap.Float64("stable_rps", currentRPS),
			zap.Duration("analysis_duration", duration),
			zap.Bool("forced_plateau", forcedPlateau),
			zap.String("forced_plateau_reason", forcedPlateauReason),
			zap.Int("total_negative_resets", negativeResets),
		)

		// ── Phase 3: Analysis window ─────────────────────────────────────────

		analysisStart := time.Now()
		runCtx, cancel := context.WithTimeout(context.Background(), duration)
		analyCtx, analyCtxCancel := context.WithCancel(context.Background())
		defer cancel()

		analysisTicker := time.NewTicker(tickInterval(currentRPS))
		defer analysisTicker.Stop()

		// Timeline: one-second sampling goroutine.
		// Snapshots totalRequests and totalErrors each second and records deltas.
		var timelinePoints []TimelinePoint
		var timelineMu sync.Mutex
		timelineDone := make(chan struct{})
		go func() {
			defer close(timelineDone)
			secTicker := time.NewTicker(time.Second)
			defer secTicker.Stop()
			prevReq := atomic.LoadInt64(&totalRequests)
			prevErr := atomic.LoadInt64(&totalErrors)
			sec := 0
			for {
				select {
				case <-runCtx.Done():
					return
				case <-secTicker.C:
					curReq := atomic.LoadInt64(&totalRequests)
					curErr := atomic.LoadInt64(&totalErrors)
					pt := TimelinePoint{
						SecondOffset: sec,
						Requests:     curReq - prevReq,
						Errors:       curErr - prevErr,
					}
					log.Info("live_rps",
						zap.String("flow", flow.Name),
						zap.String("phase", "analysis"),
						zap.Float64("rps", float64(curReq-prevReq)),
						zap.Float64("target_rps", targetRPS),
						zap.Int64("errors_this_sec", curErr-prevErr),
					)
					timelineMu.Lock()
					timelinePoints = append(timelinePoints, pt)
					timelineMu.Unlock()
					prevReq = curReq
					prevErr = curErr
					sec++
				}
			}
		}()

	analysisLoop:
		for {
			select {
			case <-runCtx.Done():
				analyCtxCancel()
				break analysisLoop
			case <-analysisTicker.C:
				spawnIteration(analyCtx, nil)
			}
		}

		// ── Phase 4: Drain ───────────────────────────────────────────────────
		<-timelineDone
		rampWg.Wait()

		analysisDuration := time.Since(analysisStart)

		// Compute concurrency stats.
		concurrencyMu.Lock()
		cSnap := make([]int64, len(concurrencySamples))
		copy(cSnap, concurrencySamples)
		concurrencyMu.Unlock()

		var maxConc int64
		var sumConc float64
		for _, c := range cSnap {
			if c > maxConc {
				maxConc = c
			}
			sumConc += float64(c)
		}
		avgConc := 0.0
		if len(cSnap) > 0 {
			avgConc = sumConc / float64(len(cSnap))
		}

		// Mean think-time.
		thinkTimeMu.Lock()
		ttSnap := make([]time.Duration, len(thinkTimeSamples))
		copy(ttSnap, thinkTimeSamples)
		thinkTimeMu.Unlock()
		var ttSum float64
		for _, t := range ttSnap {
			ttSum += toMS(t)
		}
		ttMean := 0.0
		if len(ttSnap) > 0 {
			ttMean = ttSum / float64(len(ttSnap))
		}

		// Override the simple rpsAchieved calculation with analysis-window measurement.
		// rpsAchieved is HTTP calls/s = iterations × nSteps / analysisDuration.
		iterationsPerSecond := float64(totalIterations) / analysisDuration.Seconds()
		rpsAchieved = iterationsPerSecond * nSteps

		// Patch the extra report fields via a closure over the named vars.
		extraReportFields = func(r *FlowReport) {
			r.RampUpDurationS = rampDuration.Seconds()
			r.AnalysisDurationS = analysisDuration.Seconds()
			r.StableRPSTarget = targetRPS
			r.StableRPSAchieved = rpsAchieved
			r.IterationsPerSecond = iterationsPerSecond
			r.StableRPSMaxReached = maxReached
			r.ForcedPlateau = forcedPlateau
			r.ForcedPlateauReason = forcedPlateauReason
			r.ForcedPlateauRPS = forcedPlateauRPS
			r.NegativeResets = negativeResets
			r.RampUpWindows = rampWindows
			r.AvgConcurrency = avgConc
			r.MaxConcurrency = maxConc
			r.ThinkTimeAppliedMs = ttMean
			timelineMu.Lock()
			r.Timeline = make([]TimelinePoint, len(timelinePoints))
			copy(r.Timeline, timelinePoints)
			timelineMu.Unlock()
		}
	}

	// Build per-step reports in declaration order.
	stepReports := make([]StepReport, 0, len(flow.Steps))
	for _, s := range flow.Steps {
		id := stepID(s)
		stepReports = append(stepReports, stepBuckets[id].toReport(id))
	}

	// Compute overall latency stats.
	allLatsMu.Lock()
	lats := make([]time.Duration, len(allLats))
	copy(lats, allLats)
	allLatsMu.Unlock()

	successful := totalRequests - totalErrors
	// In test mode compute a simple rpsAchieved (HTTP calls/s); load mode overrides via extraReportFields.
	if !opts.TestMode && totalIterations > 0 && rpsAchieved == 0 {
		nStepsFallback := float64(len(flow.Steps))
		if nStepsFallback < 1 {
			nStepsFallback = 1
		}
		rpsAchieved = float64(totalIterations) * nStepsFallback / duration.Seconds()
	}

	errMu.Lock()
	ebs := copyInt64Map(errByStatus)
	ebStep := copyInt64Map(errByStep)
	errMu.Unlock()

	report := FlowReport{
		TotalRequests:      totalRequests,
		SuccessfulRequests: successful,
		FailedRequests:     totalErrors,
		RPSAchieved:        rpsAchieved,
		LatencyMS:          computeLatencyStats(lats),
		Steps:              stepReports,
		ErrorsByStatus:     ebs,
		ErrorsByStep:       ebStep,
	}
	extraReportFields(&report)

	lastCtxMu.Lock()
	ctx := lastCtx
	lastCtxMu.Unlock()

	return report, ctx
}

// ExecuteWireFlow runs a wire-flow using the inherited parent context.
// It returns one StepReport per step. The step metrics are appended to the
// parent's Children field in the report.
func ExecuteWireFlow(
	log *zap.Logger,
	wf configyml.WireFlow,
	authMap map[string]configyml.AuthProfile,
	parentCtx FlowContext,
	baseURL string,
	bucket *GlobalBucket,
) []StepReport {
	// Work on a snapshot copy so the parent context is not mutated.
	fCtx := copyContext(parentCtx)
	fCtx["_base_url"] = baseURL
	fCtx["global"] = bucket.Snapshot()
	fCtx["global_lists"] = bucket.SnapshotLists()

	// Acquire auth for the wire-flow if it specifies one.
	if wf.Auth != "" {
		if auth, ok := authMap[wf.Auth]; ok {
			acquireToken(log, auth, baseURL, fCtx)
		}
	}

	reports := make([]StepReport, 0, len(wf.Steps))
	for _, step := range wf.Steps {
		id := stepID(step)
		res := executeStep(log, step, fCtx, authMap[wf.Auth], context.Background())
		failed := res.Err != nil || res.StatusCode >= 400
		if failed {
			log.Warn("wire-flow step failed",
				zap.String("wire_flow", wf.Name),
				zap.String("step", id),
				zap.Int("status", res.StatusCode),
				zap.Error(res.Err),
			)
		} else {
			applyExtracts(step.Extract, res.Body, res.RequestBody, fCtx, wf.Name, bucket)
		}
		reports = append(reports, StepReport{
			StepID:    id,
			Requests:  1,
			Errors:    boolToInt64(failed),
			LatencyMS: computeLatencyStats([]time.Duration{res.Latency}),
		})
	}
	return reports
}

// ---------------------------------------------------------------------------
// Iterate-flow execution
// ---------------------------------------------------------------------------

// ExecuteIterateFlow runs a flow of type "iterate". It reads a list from the
// global bucket and processes every item using a goroutine pool, mirroring the
// end-to-end execution engine. RPS means HTTP calls/s (same as end-to-end):
// the arrival-rate ticker fires at rps/nSteps iterations/s so that total HTTP
// calls equal rps calls/s. Think-time inside each goroutine self-regulates pace.
//
// The pool stops as soon as all items have been dispatched (option A — no looping).
// The current item is injected into the flow context under the key specified
// by flow.ItemVariable (default "item"), making it accessible in templates as
// {{ .item }} or {{ index .item "field" }}.
func ExecuteIterateFlow(
	log *zap.Logger,
	flow configyml.Flow,
	authMap map[string]configyml.AuthProfile,
	opts *models.RunOptions,
	baseURL string,
	bucket *GlobalBucket,
) FlowReport {
	// Parse iterate_list: "flow-name.key".
	parts := strings.SplitN(flow.IterateList, ".", 2)
	if len(parts) != 2 {
		log.Error("iterate_list must be 'flow-name.key'", zap.String("iterate_list", flow.IterateList))
		return FlowReport{}
	}
	srcFlow, srcKey := parts[0], parts[1]

	// Get the list from the global bucket.
	items := bucket.GetList(srcFlow, srcKey)
	if len(items) == 0 {
		log.Warn("iterate: list is empty, nothing to process",
			zap.String("iterate_list", flow.IterateList),
		)
		return FlowReport{}
	}

	itemVar := flow.ItemVariable
	if itemVar == "" {
		itemVar = "item"
	}

	rps := flow.RequestsPerSecond
	testMode := opts.TestMode || rps <= 0

	nSteps := float64(len(flow.Steps))
	if nSteps < 1 {
		nSteps = 1
	}

	// Per-step metric accumulators.
	stepBuckets := make(map[string]*stepMetricsBucket, len(flow.Steps))
	for _, s := range flow.Steps {
		stepBuckets[stepID(s)] = &stepMetricsBucket{}
	}

	var totalRequests, totalErrors int64
	var allLatsMu sync.Mutex
	var allLats []time.Duration
	errByStatus := make(map[string]int64)
	errByStep := make(map[string]int64)
	var errMu sync.Mutex

	recordResult := func(id string, res stepResult) {
		failed := res.Err != nil || res.StatusCode >= 400
		atomic.AddInt64(&totalRequests, 1)
		if failed {
			atomic.AddInt64(&totalErrors, 1)
		}
		allLatsMu.Lock()
		allLats = append(allLats, res.Latency)
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

	log.Info("iterate: starting",
		zap.String("iterate_list", flow.IterateList),
		zap.Int("items", len(items)),
		zap.Float64("rps", rps),
		zap.Bool("test_mode", testMode),
	)

	iterStart := time.Now()

	if testMode {
		// Test mode: process all items sequentially, no pacing.
		for i, item := range items {
			fCtx := FlowContext{
				"_base_url":    baseURL,
				itemVar:        item,
				"global":       bucket.Snapshot(),
				"global_lists": bucket.SnapshotLists(),
			}
			if flow.Auth != "" {
				if auth, ok := authMap[flow.Auth]; ok {
					acquireToken(log, auth, baseURL, fCtx)
				}
			}
			for _, step := range flow.Steps {
				id := stepID(step)
				res := executeStep(log, step, fCtx, authMap[flow.Auth], context.Background())
				recordResult(id, res)
				if res.Err != nil || res.StatusCode >= 400 {
					log.Debug("iterate: step failed",
						zap.Int("item_index", i),
						zap.String("step", id),
						zap.Int("status", res.StatusCode),
						zap.Error(res.Err),
					)
				} else {
					applyExtracts(step.Extract, res.Body, res.RequestBody, fCtx, flow.Name, bucket)
				}
			}
		}
	} else {
		// Load mode: goroutine pool with arrival-rate ticker, mirroring executeFlow.
		//
		// RPS = HTTP calls/s. Ticker interval = nSteps / rps so that firing one
		// goroutine per tick produces exactly rps HTTP calls/s.
		tickInterval := time.Duration(float64(time.Second) * nSteps / rps)

		// Semaphore: cap concurrent goroutines.
		semCap := int(rps/nSteps*10) + 1
		if semCap < 8 {
			semCap = 8
		}
		sem := make(chan struct{}, semCap)

		var iterWg sync.WaitGroup

		// itemCh feeds items to goroutines; closed when all items are dispatched.
		itemCh := make(chan any, len(items))
		for _, it := range items {
			itemCh <- it
		}
		close(itemCh)

		// Live RPS monitor: logs actual HTTP calls/s every second during iterate load run.
		iterMonitorCtx, iterMonitorCancel := context.WithCancel(context.Background())
		iterMonitorDone := make(chan struct{})
		go func() {
			defer close(iterMonitorDone)
			monTicker := time.NewTicker(time.Second)
			defer monTicker.Stop()
			prevReq := atomic.LoadInt64(&totalRequests)
			for {
				select {
				case <-iterMonitorCtx.Done():
					return
				case <-monTicker.C:
					curReq := atomic.LoadInt64(&totalRequests)
					log.Info("live_rps",
						zap.String("flow", flow.Name),
						zap.String("phase", "iterate"),
						zap.Float64("rps", float64(curReq-prevReq)),
						zap.Float64("target_rps", rps),
					)
					prevReq = curReq
				}
			}
		}()

		ticker := time.NewTicker(tickInterval)
		defer ticker.Stop()

		for range ticker.C {
			item, more := <-itemCh
			if !more {
				// All items dispatched — stop spawning.
				break
			}

			select {
			case sem <- struct{}{}:
			default:
				// Semaphore full — put item back is not possible on closed channel,
				// so we process it inline (blocking) to avoid losing it.
				sem <- struct{}{}
			}

			capturedItem := item
			capturedRPS := rps
			iterWg.Add(1)
			go func() {
				defer func() {
					<-sem
					iterWg.Done()
				}()
				fCtx := FlowContext{
					"_base_url":    baseURL,
					itemVar:        capturedItem,
					"global":       bucket.Snapshot(),
					"global_lists": bucket.SnapshotLists(),
				}
				if flow.Auth != "" {
					if auth, ok := authMap[flow.Auth]; ok {
						acquireToken(log, auth, baseURL, fCtx)
					}
				}
				gStart := time.Now()
				for _, step := range flow.Steps {
					id := stepID(step)
					res := executeStep(log, step, fCtx, authMap[flow.Auth], context.Background())
					recordResult(id, res)
					if res.Err != nil || res.StatusCode >= 400 {
						log.Debug("iterate: step failed",
							zap.String("step", id),
							zap.Int("status", res.StatusCode),
							zap.Error(res.Err),
						)
					} else {
						applyExtracts(step.Extract, res.Body, res.RequestBody, fCtx, flow.Name, bucket)
					}
				}
				// Think-time: sleep remainder of target iteration slot.
				targetIterDur := time.Duration(float64(time.Second) * nSteps / capturedRPS)
				if thinkTime := targetIterDur - time.Since(gStart); thinkTime > 0 {
					time.Sleep(thinkTime)
				}
			}()
		}

		// Wait for all in-flight goroutines to finish.
		iterMonitorCancel()
		<-iterMonitorDone
		iterWg.Wait()
	}

	iterDuration := time.Since(iterStart)

	// Build per-step reports.
	stepReports := make([]StepReport, 0, len(flow.Steps))
	for _, s := range flow.Steps {
		id := stepID(s)
		stepReports = append(stepReports, stepBuckets[id].toReport(id))
	}

	allLatsMu.Lock()
	lats := make([]time.Duration, len(allLats))
	copy(lats, allLats)
	allLatsMu.Unlock()

	errMu.Lock()
	ebs := copyInt64Map(errByStatus)
	ebStep := copyInt64Map(errByStep)
	errMu.Unlock()

	// RPS = total HTTP calls / wall-clock seconds.
	rpsAchieved := 0.0
	if iterDuration.Seconds() > 0 {
		rpsAchieved = float64(totalRequests) / iterDuration.Seconds()
	}

	return FlowReport{
		TotalRequests:      totalRequests,
		SuccessfulRequests: totalRequests - totalErrors,
		FailedRequests:     totalErrors,
		RPSAchieved:        rpsAchieved,
		AnalysisDurationS:  iterDuration.Seconds(),
		LatencyMS:          computeLatencyStats(lats),
		Steps:              stepReports,
		ErrorsByStatus:     ebs,
		ErrorsByStep:       ebStep,
	}
}

// ---------------------------------------------------------------------------
// Step execution
// ---------------------------------------------------------------------------

// executeStep performs a single HTTP request described by step, using fCtx
// to resolve template variables and auth credentials.
func executeStep(log *zap.Logger, step configyml.Step, fCtx FlowContext, auth configyml.AuthProfile, ctx context.Context) stepResult {
	// Resolve endpoint template.
	endpoint := renderTemplate(step.Endpoint, fCtx)
	method := strings.ToUpper(step.Method)

	// Build request body.
	var bodyReader io.Reader
	var err error
	switch step.PayloadStrategy {
	case "fixed":
		data, err := os.ReadFile(step.PayloadFile)
		if err != nil {
			return stepResult{Err: fmt.Errorf("read payload file %s: %w", step.PayloadFile, err)}
		}
		bodyReader = bytes.NewReader(data)

	case "template":
		rendered := renderTemplate(step.PayloadTemplate, fCtx)
		bodyReader = strings.NewReader(rendered)

	case "extracted":
		if step.FromStep != "" {
			if raw, ok := fCtx[step.FromStep+"._body"]; ok {
				bodyReader = strings.NewReader(fmt.Sprintf("%v", raw))
			}
		}

	case "template-json":
		// Load the base JSON: from PayloadFile if specified, otherwise random payload.
		var baseData []byte
		if step.PayloadFile != "" {
			baseData, err = os.ReadFile(step.PayloadFile)
			if err != nil {
				return stepResult{Err: fmt.Errorf("template-json: read base file %s: %w", step.PayloadFile, err)}
			}
		} else {
			payloadDir := filepath.Join("api",
				strings.Trim(strings.ReplaceAll(renderTemplate(step.Endpoint, fCtx), "/", "_"), "_"),
				strings.ToLower(strings.ToUpper(step.Method)),
			)
			baseData, err = randomPayload(payloadDir)
			if err != nil {
				return stepResult{Err: fmt.Errorf("template-json: load random payload from %s: %w", payloadDir, err)}
			}
		}
		// Unmarshal into a generic map.
		var obj map[string]any
		if err = json.Unmarshal(baseData, &obj); err != nil {
			return stepResult{Err: fmt.Errorf("template-json: parse base JSON: %w", err)}
		}
		// Apply each override: render the value template, then set it at the dot-path.
		for path, tmplVal := range step.PayloadOverrides {
			rendered := renderTemplate(tmplVal, fCtx)
			// Try to unmarshal as JSON first (handles numbers, booleans, nested objects).
			var parsed any
			if json.Unmarshal([]byte(rendered), &parsed) == nil {
				setNestedJSON(obj, path, parsed)
			} else {
				setNestedJSON(obj, path, rendered)
			}
		}
		merged, err := json.Marshal(obj)
		if err != nil {
			return stepResult{Err: fmt.Errorf("template-json: marshal merged payload: %w", err)}
		}
		bodyReader = bytes.NewReader(merged)

	default: // "random" or empty
		payloadDir := filepath.Join("api",
			strings.Trim(strings.ReplaceAll(endpoint, "/", "_"), "_"),
			strings.ToLower(method),
		)
		data, err := randomPayload(payloadDir)
		if err == nil {
			bodyReader = bytes.NewReader(data)
		}
	}

	baseURL, _ := fCtx["_base_url"].(string)
	url := baseURL + endpoint

	// Capture the request body bytes so they can be used for extraction later.
	var requestBody []byte
	if bodyReader != nil {
		requestBody, err = io.ReadAll(bodyReader)
		if err != nil {
			return stepResult{Err: fmt.Errorf("read request body: %w", err)}
		}
		bodyReader = bytes.NewReader(requestBody)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return stepResult{Err: fmt.Errorf("build request: %w", err)}
	}
	if bodyReader != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	injectAuth(req, auth, fCtx)

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	latency := time.Since(start)
	if err != nil {
		return stepResult{Err: err, Latency: latency}
	}
	defer resp.Body.Close() //nolint:errcheck
	body, _ := io.ReadAll(resp.Body)

	log.Debug("step executed",
		zap.String("step", step.ID),
		zap.String("method", method),
		zap.String("url", url),
		zap.Int("status", resp.StatusCode),
		zap.Duration("latency", latency),
	)

	return stepResult{
		StepID:      step.ID,
		StatusCode:  resp.StatusCode,
		Latency:     latency,
		Body:        body,
		RequestBody: requestBody,
	}
}
