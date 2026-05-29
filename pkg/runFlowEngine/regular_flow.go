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

type rumpUpResponse struct {
	rampDuration        time.Duration
	stableRPS           float64
	forcedPlateau       bool
	forcedPlateauReason string
	totalNegativeResets int
	maxReached          bool
}

type regulator struct {
	currentRPS float64
	timeSleep  time.Duration
	mu         sync.Mutex
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
) {
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
	var isRampUp = true
	var mapForSetpsMu sync.RWMutex

	// errorsByStatus and errorsByStep for the report.
	errByStatus := make(map[string]int64)
	errByStep := make(map[string]int64)
	var errMu sync.Mutex

	// lastCtx captures the final execution context from the last successful
	// iteration;.
	var lastCtxMu sync.Mutex

	responseCh := make(chan rumpUpResponse, 1)

	// runCtx is cancelled when the analysis duration completes; all
	// regulator goroutines and iterations should observe this and stop.
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	var regulatorsWg sync.WaitGroup

	var windowsBeforeIterations int64
	var windowsBeforeSecondIterations int64
	timer := time.NewTicker(stepWin)
	timerGen := time.NewTicker(1 * time.Second)
	currentRPS := initRPS

	winIdx := 0
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

	maxRampDur, err := time.ParseDuration(rampCfg.MaxRampDuration)
	if err != nil {
		maxRampDur = 600 * time.Second
	}

	mapForSetps := make(map[string]*regulator)
	mapNum := 0
	tryIncrement := 0

	rampTimeout := time.NewTimer(maxRampDur)
	defer rampTimeout.Stop()

	bestWindowsRPS := func() float64 {
		return BestWindowsRPS(stableWindowRPS, rampCfg, prevWindowRPS, targetRPS)
	}

	recordResult := func(id string, res stepResult) {
		RecordResult(id, res, &totalRequests, &totalErrors, &allLatsMu, &allLats, stepBuckets, errByStatus, errByStep, &errMu)
	}

	spawnIteration := func(ctx context.Context) time.Duration {
		return SpawnIterationSimple(ctx, log, &totalLaunches, baseURL, bucket, flow, authMap, &lastCtx, &lastCtxMu, &totalIterations, func(id string, res stepResult) { recordResult(id, res) })
	}

	rampStart := time.Now()

	go func() {
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
				rumpUpResponse := rumpUpResponse{
					rampDuration:        time.Since(rampStart),
					stableRPS:           currentRPS,
					forcedPlateau:       forcedPlateau,
					forcedPlateauReason: forcedPlateauReason,
					totalNegativeResets: negativeResets,
					maxReached:          maxReached,
				}
				responseCh <- rumpUpResponse
				return
			case <-timer.C:
				windowsIterations := atomic.LoadInt64(&totalIterations) - windowsBeforeIterations
				windowRPS := float64(windowsIterations) * nSteps / stepWin.Seconds()
				fmt.Printf("Window completed: iterations=%d, windowRPS=%.2f\n", windowsIterations, windowRPS)
				windowsBeforeIterations += windowsIterations

				log.Info("ramp-up: window result",
					zap.Int("window", winIdx),
					zap.Int("window_iteration_before", int(windowsBeforeIterations)),
					zap.Int("total_iterations", int(atomic.LoadInt64(&totalIterations))),
					zap.Int("window_iters", int(windowsIterations)),
					zap.Float64("window_rps", windowRPS),
				)
				winIdx++

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
					zap.Int("total_iterations", int(atomic.LoadInt64(&totalIterations))),
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

					rumpUpResponse := rumpUpResponse{
						rampDuration:        time.Since(rampStart),
						stableRPS:           currentRPS,
						forcedPlateau:       forcedPlateau,
						forcedPlateauReason: forcedPlateauReason,
						totalNegativeResets: negativeResets,
						maxReached:          maxReached,
					}
					responseCh <- rumpUpResponse
					return
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
					rumpUpResponse := rumpUpResponse{
						rampDuration:        time.Since(rampStart),
						stableRPS:           currentRPS,
						forcedPlateau:       forcedPlateau,
						forcedPlateauReason: forcedPlateauReason,
						totalNegativeResets: negativeResets,
						maxReached:          maxReached,
					}
					responseCh <- rumpUpResponse
					return
				}

				// Logic to decide if we need to send a control signal based on the observed windowRPS vs targetRPS and maxRPS.
			}
		}
	}()

	for {
		select {
		case resp := <-responseCh:
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

			log.Info("plateau reached — starting analysis window",
				zap.Float64("ramp_up_duration_s", resp.rampDuration.Seconds()),
				zap.Float64("stable_rps", resp.stableRPS),
				zap.Duration("analysis_duration", duration),
				zap.Bool("forced_plateau", resp.forcedPlateau),
				zap.String("forced_plateau_reason", resp.forcedPlateauReason),
				zap.Int("total_negative_resets", resp.totalNegativeResets),
			)
			isRampUp = false

			// Start the analysis-duration timer: when it fires, cancel the run
			// to stop all regulators/iterations. We do this once per plateau
			// response.
			go func() {
				select {
				case <-time.After(duration):
					log.Info("analysis duration complete — cancelling run", zap.Duration("duration", duration))
					runCancel()
				case <-runCtx.Done():
					// already cancelled
				}
			}()

		case <-timerGen.C:
			windowsIterations := atomic.LoadInt64(&totalIterations) - windowsBeforeSecondIterations
			windowRPS := float64(windowsIterations) * nSteps / stepWin.Seconds()
			windowsBeforeSecondIterations += windowsIterations

			if isRampUp {

				// During analysis, just log the observed RPS every second.
				// check if all regulators have the corrects rps, based on the observed currentRPS and the currentRPS
				// if all rps are more greater that the current rps, calculate a new time to sleep for each regulator,
				// based on the currentRPS and the targetRPS, and update the regulators in the mapForSetps
				// compute total RPS across existing regulators + this candidate
				totalRPS := currentRPS
				mapForSetpsMu.RLock()
				for _, r := range mapForSetps {
					r.mu.Lock()
					totalRPS += r.currentRPS
					r.mu.Unlock()
				}
				mapForSetpsMu.RUnlock()
				if totalRPS > targetRPS && totalRPS > 0 {
					ratio := targetRPS / totalRPS // < 1
					// Set a sleep proportionally so each regulator reduces its effective
					// rate by `ratio`. We translate that into a per-iteration pause.
					mapForSetpsMu.RLock()
					for _, r := range mapForSetps {
						// avoid division by zero
						if r.currentRPS <= 0 {
							r.mu.Lock()
							r.timeSleep = 0
							r.mu.Unlock()
							continue
						}
						// desired rate = r.currentRPS * ratio; to achieve that, add a sleep
						// roughly equal to (1/ratio - 1) seconds per iteration as a simple heuristic.
						r.mu.Lock()
						d := time.Duration((1/ratio - 1) * float64(time.Second))
						r.timeSleep = d
						r.mu.Unlock()
					}
					mapForSetpsMu.RUnlock()
				} else if totalRPS < targetRPS && totalRPS > 0 {
					// We are under target: decrease timeSleep so regulators try to increase throughput.
					ratio := targetRPS / totalRPS // > 1
					mapForSetpsMu.RLock()
					for _, r := range mapForSetps {
						r.mu.Lock()
						if r.currentRPS <= 0 {
							r.mu.Unlock()
							continue
						}
						// Reduce existing sleep by dividing by ratio; keep minimum 0.
						cur := float64(r.timeSleep)
						newd := time.Duration(cur / ratio)
						if newd < 0 {
							newd = 0
						}
						r.timeSleep = newd
						r.mu.Unlock()
					}
					mapForSetpsMu.RUnlock()
				}

				if windowRPS < currentRPS*0.70 && isRampUp {
					tryIncrement++
					if tryIncrement >= 5 {
						log.Warn("observed RPS consistently below target, evaluating regulators",
							zap.String("flow", flow.Name),
							zap.Float64("current_rps", currentRPS),
						)
						// Add the new regulator with computed sleep too. Capture a
						// stable index to avoid races on mapNum.
						idx := mapNum
						mapNum++
						key := fmt.Sprintf("step-%d", idx)
						mapForSetpsMu.Lock()
						mapForSetps[key] = &regulator{
							currentRPS: currentRPS,
							timeSleep:  time.Duration(1 * time.Second),
						}
						mapForSetpsMu.Unlock()

						regulatorsWg.Add(1)
						go func(k string) {
							defer regulatorsWg.Done()
							for {
								// Exit quickly when run cancelled.
								select {
								case <-runCtx.Done():
									return
								default:
								}

								// read sleep under map RLock
								mapForSetpsMu.RLock()
								reg := mapForSetps[k]
								mapForSetpsMu.RUnlock()
								if reg == nil {
									select {
									case <-time.After(100 * time.Millisecond):
									case <-runCtx.Done():
										return
									}
									continue
								}
								reg.mu.Lock()
								sleepFor := reg.timeSleep
								reg.mu.Unlock()

								// Wait or exit if cancelled during sleep.
								select {
								case <-time.After(sleepFor):
								case <-runCtx.Done():
									return
								}

								// Before launching an iteration, check cancel.
								select {
								case <-runCtx.Done():
									return
								default:
								}

								dur := spawnIteration(runCtx)

								// calc rps based on duration and steps num
								if dur > 0 {
									reg.mu.Lock()
									reg.currentRPS = nSteps / dur.Seconds()
									reg.mu.Unlock()
								}
							}
						}(key)
						tryIncrement = 0
					}
				}

				if windowRPS > currentRPS*0.95 && isRampUp {
					// Grow towards target.
					nextRPS := currentRPS * rampCfg.Factor
					if nextRPS >= targetRPS {
						nextRPS = targetRPS
						// Give it one more window at target before declaring plateau.
						stableWindows = rampCfg.StabilityWindows - 1
					}
					currentRPS = nextRPS
				}
			} else {
				log.Info("analysis window progress",
					zap.Float64("rps", windowRPS),
				)
			}

		case <-runCtx.Done():
			// Analysis duration expired and run cancelled: exit main loop.
			log.Info("run context cancelled — stopping ramp/monitor loop")
			// Wait for regulators to finish their current iteration.
			regulatorsWg.Wait()
			return
		}

	}
}
