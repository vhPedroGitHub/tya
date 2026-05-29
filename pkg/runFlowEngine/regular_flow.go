package runflowengine

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vhPedroGitHub/tya/pkg/configyml"
	"go.uber.org/zap"
)

type rumpUpResponse struct {
	rampDuration        time.Duration
	stableRPS           float64
	forcedPlateau       bool
	forcedPlateauReason string
	totalNegativeResets int
	maxReached          bool
	rampWindows         []RampUpWindow
	avgConcurrency      float64
	maxConcurrency      int64
	thinkTimeAppliedMs  float64
	iterationsPerSecond float64
}

func revisionerRampUpandExecuteFlow(
	log *zap.Logger,
	flow configyml.Flow,
	rampCfg configyml.RampUp,
	initRPS float64,
	targetRPS float64,
	stepWin time.Duration,
	nSteps float64,
	bucket *GlobalBucket,
	authMap map[string]configyml.AuthProfile,
	baseURL string,
	lastCtx FlowContext,
	duration time.Duration,
) (int64, int64, int64, []time.Duration, float64, rumpUpResponse, []StepReport) {
	stepBuckets := make(map[string]*stepMetricsBucket, len(flow.Steps))
	for _, s := range flow.Steps {
		stepBuckets[stepID(s)] = &stepMetricsBucket{}
	}

	var totalRequests, totalErrors, totalIterations int64
	var allLatsMu sync.Mutex
	var allLats []time.Duration

	errByStatus := make(map[string]int64)
	errByStep := make(map[string]int64)
	var errMu sync.Mutex

	var lastCtxMu sync.Mutex

	var concurrencySamples []int64
	var concurrencyMu sync.Mutex
	var activeConcurrency int64

	var thinkTimeSamples []time.Duration
	var thinkTimeMu sync.Mutex

	sampleConcurrency := func(n int64) {
		concurrencyMu.Lock()
		concurrencySamples = append(concurrencySamples, n)
		concurrencyMu.Unlock()
	}

	maxRampDur, err := time.ParseDuration(rampCfg.MaxRampDuration)
	if err != nil {
		maxRampDur = 600 * time.Second
	}

	semCap := int(targetRPS/nSteps*10) + 1
	if semCap < 8 {
		semCap = 8
	}
	sem := make(chan struct{}, semCap)

	var rampWg sync.WaitGroup

	tickInterval := func(httpRPS float64) time.Duration {
		return time.Duration(float64(time.Second) * nSteps / httpRPS)
	}

	recordResult := func(id string, res stepResult) {
		RecordResult(id, res, &totalRequests, &totalErrors, &allLatsMu, &allLats, stepBuckets, errByStatus, errByStep, &errMu)
	}

	spawnIteration := func(launchRPS float64) {
		select {
		case sem <- struct{}{}:
		default:
			return
		}
		cur := atomic.AddInt64(&activeConcurrency, 1)
		sampleConcurrency(cur)
		rampWg.Add(1)
		go func() {
			iterStart := time.Now()

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
				res := executeStep(log, step, fCtx, authMap[flow.Auth])
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

			// Liberar semáforo ANTES del think-time
			<-sem
			atomic.AddInt64(&activeConcurrency, -1)

			// Contar iteración DESPUÉS de completar
			atomic.AddInt64(&totalIterations, 1)

			// Think-time sin bloquear semáforo
			targetIterDur := time.Duration(float64(time.Second) * nSteps / launchRPS)
			elapsed := time.Since(iterStart)
			if thinkTime := targetIterDur - elapsed; thinkTime > 0 {
				thinkTimeMu.Lock()
				thinkTimeSamples = append(thinkTimeSamples, thinkTime)
				thinkTimeMu.Unlock()
				time.Sleep(thinkTime)
			}

			rampWg.Done()
		}()
	}

	rampMonitorCtx, rampMonitorCancel := context.WithCancel(context.Background())
	rampMonitorDone := make(chan struct{})
	go func() {
		defer close(rampMonitorDone)
		monTicker := time.NewTicker(time.Second)
		defer monTicker.Stop()
		prevIter := atomic.LoadInt64(&totalIterations)
		for {
			select {
			case <-rampMonitorCtx.Done():
				return
			case <-monTicker.C:
				curIter := atomic.LoadInt64(&totalIterations)
				deltaIter := curIter - prevIter
				liveRPS := float64(deltaIter) * nSteps
				prevIter = curIter
				log.Info("live_rps",
					zap.String("flow", flow.Name),
					zap.String("phase", "ramp_up"),
					zap.Float64("rps", liveRPS),
					zap.Float64("target_rps", targetRPS),
				)
			}
		}
	}()

	rampStart := time.Now()
	stableWindows := 0
	prevWindowRPS := 0.0
	maxReached := false

	forcedPlateau := false
	forcedPlateauReason := ""
	forcedPlateauRPS := 0.0
	negativeResets := 0
	consecNegResets := 0
	var rampWindows []RampUpWindow
	var stableWindowRPS []float64

	rampTimeout := time.NewTimer(maxRampDur)
	defer rampTimeout.Stop()

	bestWindowsRPS := func() float64 {
		return BestWindowsRPS(stableWindowRPS, rampCfg, prevWindowRPS, targetRPS)
	}

	currentRPS := initRPS
	winIdx := 0

rampLoop:
	for {
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
		ticker := time.NewTicker(tickInterval(currentRPS))
		winTimer := time.NewTimer(stepWin)

	rampWindow:
		for {
			select {
			case <-winTimer.C:
				ticker.Stop()
				break rampWindow
			case <-ticker.C:
				spawnIteration(currentRPS)
			}
		}
		winTimer.Stop()

		windowIters := atomic.LoadInt64(&totalIterations) - windowItersBefore
		windowRPS := float64(windowIters) * nSteps / stepWin.Seconds()

		variation := 0.0
		isStable := false
		isNegReset := false

		if prevWindowRPS > 0 {
			variation = (windowRPS - prevWindowRPS) / prevWindowRPS
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
		)

		prevWindowRPS = windowRPS

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

		nextRPS := currentRPS * rampCfg.Factor
		if nextRPS >= targetRPS {
			nextRPS = targetRPS
			stableWindows = rampCfg.StabilityWindows - 1
		}
		currentRPS = nextRPS
	}

	rampMonitorCancel()
	<-rampMonitorDone

	rampDuration := time.Since(rampStart)

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

	analysisStart := time.Now()
	runCtx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()

	analysisTicker := time.NewTicker(tickInterval(currentRPS))
	defer analysisTicker.Stop()

analysisLoop:
	for {
		select {
		case <-runCtx.Done():
			break analysisLoop
		case <-analysisTicker.C:
			spawnIteration(currentRPS)
		}
	}

	rampWg.Wait()

	analysisDuration := time.Since(analysisStart)

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

	iterationsPerSecond := float64(totalIterations) / analysisDuration.Seconds()
	rpsAchieved := iterationsPerSecond * nSteps

	allLatsMu.Lock()
	lats := make([]time.Duration, len(allLats))
	copy(lats, allLats)
	allLatsMu.Unlock()

	// Build per-step reports
	stepReports := make([]StepReport, 0, len(flow.Steps))
	for _, s := range flow.Steps {
		id := stepID(s)
		stepReports = append(stepReports, stepBuckets[id].toReport(id))
	}

	finalResp := rumpUpResponse{
		rampDuration:        rampDuration,
		stableRPS:           currentRPS,
		forcedPlateau:       forcedPlateau,
		forcedPlateauReason: forcedPlateauReason,
		totalNegativeResets: negativeResets,
		maxReached:          maxReached,
		rampWindows:         rampWindows,
		avgConcurrency:      avgConc,
		maxConcurrency:      maxConc,
		thinkTimeAppliedMs:  ttMean,
		iterationsPerSecond: iterationsPerSecond,
	}

	return totalRequests, totalErrors, totalIterations, lats, rpsAchieved, finalResp, stepReports
}
