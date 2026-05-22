package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"text/template"
	"time"

	"tya/pkg/cli_functions"
	"tya/pkg/configyml"
	"tya/pkg/models"

	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

// NewRunCmd returns the cobra command for `tya run`.
func NewRunCmd(log *zap.Logger) *cobra.Command {
	opts := &models.RunOptions{}

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Execute flows against a live API",
		Long: `Reads config-run.yml and executes the defined flows.

Flows are started in dependency order: a flow listed in another flow's
depends_on will not start until all of its dependencies have completed.
Wire-flow children declared inside a parent flow run exactly once after
the parent finishes, inheriting its final execution context.

Examples:
  tya run                    # Execute all flows in dependency order
  tya run -t                 # Test mode: single pass, ignores RPS
  tya run --flow login-flow  # Execute a specific named flow`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFlows(log, opts)
		},
	}

	cmd.Flags().StringVar(&opts.ConfigFile, "config", "config-run.yml", "path to config-run.yml")
	cmd.Flags().BoolVarP(&opts.TestMode, "test", "t", false, "test mode: single pass, ignores RPS")
	cmd.Flags().StringVar(&opts.Flow, "flow", "", "run only this named flow")

	return cmd
}

// ---------------------------------------------------------------------------
// Run entry point
// ---------------------------------------------------------------------------

// runReport is the top-level JSON report structure written at the end of a run.
type runReport struct {
	RunID      string                                  `json:"run_id"`
	StartedAt  time.Time                               `json:"started_at"`
	FinishedAt time.Time                               `json:"finished_at"`
	DurationS  float64                                 `json:"duration_s"`
	Flows      map[string]cli_functions.FlowReport     `json:"flows"`
}

// runFlows is the main entry point for the run command.
func runFlows(log *zap.Logger, opts *models.RunOptions) error {
	cfg, err := configyml.LoadRunConfig(opts.ConfigFile)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Build auth profile map.
	authMap := map[string]configyml.AuthProfile{}
	for _, a := range cfg.AuthProfiles {
		authMap[a.Name] = a
	}

	// Filter to a single flow if --flow is specified.
	flows := cfg.Flows
	if opts.Flow != "" {
		filtered := []configyml.Flow{}
		for _, f := range flows {
			if f.Name == opts.Flow {
				filtered = append(filtered, f)
			}
		}
		if len(filtered) == 0 {
			return fmt.Errorf("flow %q not found in config", opts.Flow)
		}
		flows = filtered
	}

	if len(flows) == 0 {
		log.Warn("no flows to run")
		return nil
	}

	// Validate dependency graph and exit early on any violation.
	if err := cli_functions.ValidateDependencyGraph(flows); err != nil {
		return fmt.Errorf("dependency graph error: %w", err)
	}

	// Sort flows into topological execution order.
	flows = cli_functions.TopologicalOrder(flows)

	// TYA_BASE_URL env var overrides config-run.yml base_url.
	baseURL := cfg.BaseURL
	if env := os.Getenv("TYA_BASE_URL"); env != "" {
		baseURL = env
	}
	startedAt := time.Now()

	// Build the executor functions that close over logger, authMap, opts, baseURL.
	flowExec := func(flow configyml.Flow) (cli_functions.FlowReport, cli_functions.FlowContext) {
		return executeFlow(log, flow, authMap, opts, baseURL)
	}
	wireExec := func(wf configyml.WireFlow, parentCtx cli_functions.FlowContext) []cli_functions.StepReport {
		return executeWireFlow(log, wf, authMap, parentCtx, baseURL)
	}

	results := cli_functions.RunScheduler(log, flows, flowExec, wireExec)

	finishedAt := time.Now()

	report := runReport{
		RunID:      newRunID(),
		StartedAt:  startedAt,
		FinishedAt: finishedAt,
		DurationS:  finishedAt.Sub(startedAt).Seconds(),
		Flows:      results,
	}

	reportPath := fmt.Sprintf("tya-report-%s.json", startedAt.Format("20060102-150405"))
	data, _ := json.MarshalIndent(report, "", "  ")
	if err := os.WriteFile(reportPath, data, 0o644); err != nil {
		log.Warn("could not write report", zap.Error(err))
	} else {
		log.Info("report written", zap.String("path", reportPath))
	}

	return nil
}

// newRunID returns a short pseudo-unique run identifier.
func newRunID() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

// ---------------------------------------------------------------------------
// stepResult — internal to this package
// ---------------------------------------------------------------------------

// stepResult holds the outcome of a single HTTP step execution.
type stepResult struct {
	StepID     string
	StatusCode int
	Latency    time.Duration
	Err        error
	Body       []byte
}

// stepMetricsBucket accumulates counters for a single step.
type stepMetricsBucket struct {
	requests, errors int64
	latencies        []time.Duration
	mu               sync.Mutex
}

func (b *stepMetricsBucket) record(latency time.Duration, failed bool) {
	atomic.AddInt64(&b.requests, 1)
	if failed {
		atomic.AddInt64(&b.errors, 1)
	}
	b.mu.Lock()
	b.latencies = append(b.latencies, latency)
	b.mu.Unlock()
}

func (b *stepMetricsBucket) toReport(id string) cli_functions.StepReport {
	b.mu.Lock()
	lats := make([]time.Duration, len(b.latencies))
	copy(lats, b.latencies)
	b.mu.Unlock()

	return cli_functions.StepReport{
		StepID:    id,
		Requests:  b.requests,
		Errors:    b.errors,
		LatencyMS: computeLatencyStats(lats),
	}
}

// ---------------------------------------------------------------------------
// Flow execution
// ---------------------------------------------------------------------------

// executeFlow runs a single flow according to its type and options.
// It returns a FlowReport and the last successful execution context
// (used by wire-flow children).
func executeFlow(
	log *zap.Logger,
	flow configyml.Flow,
	authMap map[string]configyml.AuthProfile,
	opts *models.RunOptions,
	baseURL string,
) (cli_functions.FlowReport, cli_functions.FlowContext) {

	duration := 30 * time.Second
	if d, err := time.ParseDuration(flow.Duration); err == nil {
		duration = d
	}
	rps := flow.RequestsPerSecond
	testMode := opts.TestMode || rps <= 0

	// Per-step metric accumulators.
	stepBuckets := make(map[string]*stepMetricsBucket, len(flow.Steps))
	for _, s := range flow.Steps {
		stepBuckets[stepID(s)] = &stepMetricsBucket{}
	}

	// Global counters.
	// totalRequests counts individual HTTP calls; totalIterations counts full flow executions.
	var totalRequests, totalErrors, totalIterations int64
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
	extraReportFields := func(_ *cli_functions.FlowReport) {}

	// rpsAchieved is computed after the run; the adaptive engine overrides it.
	var rpsAchieved float64

	// lastCtx captures the final execution context from the last successful
	// iteration; used by wire-flow children.
	var lastCtxMu sync.Mutex
	var lastCtx cli_functions.FlowContext

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
		fCtx := cli_functions.FlowContext{"_base_url": baseURL}
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
				log.Warn("step failed",
					zap.String("step", id),
					zap.Int("status", res.StatusCode),
					zap.Error(res.Err),
				)
			} else {
				applyExtracts(step.Extract, res.Body, fCtx)
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

		currentRPS := rampCfg.InitialRPS
		targetRPS := flow.RequestsPerSecond

		// Semaphore: cap concurrent goroutines to avoid unbounded accumulation.
		// Initial cap = ceil(targetRPS * 10) — generous upper bound that gets
		// refined after plateau is detected via observed p95 latency.
		semCap := int(targetRPS*10) + 1
		if semCap < 8 {
			semCap = 8
		}
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

		spawnIteration := func(launchRPS float64) {
			select {
			case sem <- struct{}{}:
			default:
				// semaphore full — drop this tick to avoid runaway goroutines
				return
			}
			cur := atomic.AddInt64(&activeConcurrency, 1)
			sampleConcurrency(cur)
			rampWg.Add(1)
			go func() {
				defer func() {
					<-sem
					atomic.AddInt64(&activeConcurrency, -1)
					rampWg.Done()
				}()
				fCtx := cli_functions.FlowContext{"_base_url": baseURL}
				if flow.Auth != "" {
					if auth, ok := authMap[flow.Auth]; ok {
						acquireToken(log, auth, baseURL, fCtx)
					}
				}
				iterStart := time.Now()
				iterOK := true
				for _, step := range flow.Steps {
					id := stepID(step)
					res := executeStep(log, step, fCtx, authMap[flow.Auth])
					recordResult(id, res)
					if res.Err != nil || res.StatusCode >= 400 {
						iterOK = false
					} else {
						applyExtracts(step.Extract, res.Body, fCtx)
					}
				}
				if iterOK {
					lastCtxMu.Lock()
					lastCtx = copyContext(fCtx)
					lastCtxMu.Unlock()
				}

				// Think-time: sleep the remainder of the target iteration
				// duration so that this goroutine self-regulates its pace.
				// The slot is held during the sleep (occupies the semaphore).
				nSteps := float64(len(flow.Steps))
				if nSteps < 1 {
					nSteps = 1
				}
				targetIterDur := time.Duration(float64(time.Second) * nSteps / launchRPS)
				elapsed := time.Since(iterStart)
				if thinkTime := targetIterDur - elapsed; thinkTime > 0 {
					thinkTimeMu.Lock()
					thinkTimeSamples = append(thinkTimeSamples, thinkTime)
					thinkTimeMu.Unlock()
					time.Sleep(thinkTime)
				}
			}()
		}

		// ── Phase 1 + 2: Ramp-up and plateau detection ──────────────────────

		rampStart := time.Now()
		stableWindows := 0
		prevWindowRPS := 0.0
		maxReached := false

		for {
			windowItersBefore := atomic.LoadInt64(&totalIterations)
			ticker := time.NewTicker(time.Duration(float64(time.Second) / currentRPS))
			winTimer := time.NewTimer(stepWin)
		rampWindow:
			for {
				select {
				case <-winTimer.C:
					ticker.Stop()
					break rampWindow
				case <-ticker.C:
					atomic.AddInt64(&totalIterations, 1)
					spawnIteration(currentRPS)
				}
			}
			winTimer.Stop()

			windowIters := atomic.LoadInt64(&totalIterations) - windowItersBefore
			windowRPS := float64(windowIters) / stepWin.Seconds()

			log.Info("ramp-up window",
				zap.Float64("current_rps_target", currentRPS),
				zap.Float64("observed_rps", windowRPS),
				zap.Float64("target_rps", targetRPS),
			)

			// Check stability: is the observed RPS within threshold of previous window?
			if prevWindowRPS > 0 {
				variation := abs64((windowRPS - prevWindowRPS) / prevWindowRPS)
				if variation <= rampCfg.StabilityThreshold {
					stableWindows++
				} else {
					stableWindows = 0
				}
			}
			prevWindowRPS = windowRPS

			if stableWindows >= rampCfg.StabilityWindows {
				if windowRPS < targetRPS*0.95 {
					maxReached = true
					log.Warn("ramp-up: target RPS unreachable, running at max achievable",
						zap.Float64("target_rps", targetRPS),
						zap.Float64("achieved_rps", windowRPS),
					)
				}
				break
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
			// cap = ceil(currentRPS * p95_s * 1.5), minimum 8.
			allLatsMu.Lock()
			latSnap := make([]time.Duration, len(allLats))
			copy(latSnap, allLats)
			allLatsMu.Unlock()
			if len(latSnap) > 0 {
				stats := computeLatencyStats(latSnap)
				p95s := stats.P95 / 1000.0
				newCap := int(currentRPS*p95s*1.5) + 1
				if newCap < 8 {
					newCap = 8
				}
				// Grow the semaphore if needed (never shrink during ramp).
				if newCap > semCap {
					semCap = newCap
					sem = make(chan struct{}, semCap)
				}
			}
		}

		rampDuration := time.Since(rampStart)

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
		)

		// ── Phase 3: Analysis window ─────────────────────────────────────────

		analysisStart := time.Now()
		runCtx, cancel := context.WithTimeout(context.Background(), duration)
		defer cancel()

		analysisTicker := time.NewTicker(time.Duration(float64(time.Second) / currentRPS))
		defer analysisTicker.Stop()

	analysisLoop:
		for {
			select {
			case <-runCtx.Done():
				break analysisLoop
			case <-analysisTicker.C:
			atomic.AddInt64(&totalIterations, 1)
			spawnIteration(currentRPS)
			}
		}

		// ── Phase 4: Drain ───────────────────────────────────────────────────
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
		rpsAchieved = float64(totalIterations) / analysisDuration.Seconds()

		// Patch the extra report fields via a closure over the named vars.
		extraReportFields = func(r *cli_functions.FlowReport) {
			r.RampUpDurationS = rampDuration.Seconds()
			r.AnalysisDurationS = analysisDuration.Seconds()
			r.StableRPSTarget = targetRPS
			r.StableRPSAchieved = rpsAchieved
			r.StableRPSMaxReached = maxReached
			r.AvgConcurrency = avgConc
			r.MaxConcurrency = maxConc
			r.ThinkTimeAppliedMs = ttMean
		}
	}

	// Build per-step reports in declaration order.
	stepReports := make([]cli_functions.StepReport, 0, len(flow.Steps))
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
	// In test mode compute a simple rpsAchieved; load mode overrides via extraReportFields.
	if !opts.TestMode && totalIterations > 0 && rpsAchieved == 0 {
		rpsAchieved = float64(totalIterations) / duration.Seconds()
	}

	errMu.Lock()
	ebs := copyInt64Map(errByStatus)
	ebStep := copyInt64Map(errByStep)
	errMu.Unlock()

	report := cli_functions.FlowReport{
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

// ---------------------------------------------------------------------------
// Wire-flow execution
// ---------------------------------------------------------------------------

// executeWireFlow runs a wire-flow using the inherited parent context.
// It returns one StepReport per step. The step metrics are appended to the
// parent's Children field in the report.
func executeWireFlow(
	log *zap.Logger,
	wf configyml.WireFlow,
	authMap map[string]configyml.AuthProfile,
	parentCtx cli_functions.FlowContext,
	baseURL string,
) []cli_functions.StepReport {
	// Work on a snapshot copy so the parent context is not mutated.
	fCtx := copyContext(parentCtx)
	fCtx["_base_url"] = baseURL

	// Acquire auth for the wire-flow if it specifies one.
	if wf.Auth != "" {
		if auth, ok := authMap[wf.Auth]; ok {
			acquireToken(log, auth, baseURL, fCtx)
		}
	}

	reports := make([]cli_functions.StepReport, 0, len(wf.Steps))
	for _, step := range wf.Steps {
		id := stepID(step)
		res := executeStep(log, step, fCtx, authMap[wf.Auth])
		failed := res.Err != nil || res.StatusCode >= 400
		if failed {
			log.Warn("wire-flow step failed",
				zap.String("wire_flow", wf.Name),
				zap.String("step", id),
				zap.Int("status", res.StatusCode),
				zap.Error(res.Err),
			)
		} else {
			applyExtracts(step.Extract, res.Body, fCtx)
		}
		reports = append(reports, cli_functions.StepReport{
			StepID:   id,
			Requests: 1,
			Errors:   boolToInt64(failed),
			LatencyMS: computeLatencyStats([]time.Duration{res.Latency}),
		})
	}
	return reports
}

func boolToInt64(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// ---------------------------------------------------------------------------
// Step execution
// ---------------------------------------------------------------------------

// executeStep performs a single HTTP request described by step, using fCtx
// to resolve template variables and auth credentials.
func executeStep(log *zap.Logger, step configyml.Step, fCtx cli_functions.FlowContext, auth configyml.AuthProfile) stepResult {
	// Resolve endpoint template.
	endpoint := renderTemplate(step.Endpoint, fCtx)
	method := strings.ToUpper(step.Method)

	// Build request body.
	var bodyReader io.Reader
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

	req, err := http.NewRequest(method, url, bodyReader)
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
		StepID:     step.ID,
		StatusCode: resp.StatusCode,
		Latency:    latency,
		Body:       body,
	}
}

// ---------------------------------------------------------------------------
// Auth: token acquisition and injection
// ---------------------------------------------------------------------------

// acquireToken performs the initial login for an auth profile and populates
// the flow context with access_token, refresh_token, etc.
func acquireToken(log *zap.Logger, auth configyml.AuthProfile, baseURL string, fCtx cli_functions.FlowContext) {
	switch auth.Type {
	case "custom_login":
		payload := expandEnv(auth.Payload)
		req, err := http.NewRequest(strings.ToUpper(auth.Method), baseURL+auth.LoginEndpoint, strings.NewReader(payload))
		if err != nil {
			log.Warn("auth: failed to build login request", zap.Error(err))
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			log.Warn("auth: login request failed", zap.Error(err))
			return
		}
		defer resp.Body.Close() //nolint:errcheck
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode >= 400 {
			log.Warn("auth: login returned error",
				zap.Int("status", resp.StatusCode),
				zap.ByteString("body", body),
			)
			return
		}
		var parsed any
		if err := json.Unmarshal(body, &parsed); err == nil {
			for key, path := range auth.ExtractToken {
				parts := strings.Split(path, ".")
				if val := navigate(parsed, parts); val != nil {
					fCtx[key] = val
				}
			}
		}
		log.Info("auth: login successful", zap.String("profile", auth.Name))

	case "oauth2_password":
		form := fmt.Sprintf(
			"grant_type=password&client_id=%s&client_secret=%s&username=%s&password=%s&scope=%s",
			expandEnv(auth.ClientID),
			expandEnv(auth.ClientSecret),
			expandEnv(auth.Username),
			expandEnv(auth.Password),
			strings.Join(auth.Scopes, " "),
		)
		req, err := http.NewRequest("POST", auth.TokenURL, strings.NewReader(form))
		if err != nil {
			log.Warn("auth: oauth2 request build failed", zap.Error(err))
			return
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			log.Warn("auth: oauth2 request failed", zap.Error(err))
			return
		}
		defer resp.Body.Close() //nolint:errcheck
		body, _ := io.ReadAll(resp.Body)
		var parsed map[string]any
		if err := json.Unmarshal(body, &parsed); err == nil {
			for k, v := range parsed {
				fCtx[k] = v
			}
		}
		log.Info("auth: oauth2 token acquired", zap.String("profile", auth.Name))
	}
}

// injectAuth sets the appropriate authentication header/param on req.
func injectAuth(req *http.Request, auth configyml.AuthProfile, fCtx cli_functions.FlowContext) {
	switch auth.Type {
	case "api_key":
		val := expandEnv(auth.Value)
		if auth.InjectAs == "query" {
			q := req.URL.Query()
			q.Set(auth.QueryParam, val)
			req.URL.RawQuery = q.Encode()
		} else {
			name := auth.HeaderName
			if name == "" {
				name = "X-API-Key"
			}
			req.Header.Set(name, val)
		}
	case "basic":
		req.SetBasicAuth(expandEnv(auth.Username), expandEnv(auth.Password))
	default:
		if token, ok := fCtx["access_token"].(string); ok && token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}
}

// ---------------------------------------------------------------------------
// Extract + navigate helpers
// ---------------------------------------------------------------------------

// applyExtracts pulls values out of a JSON response body and stores them in fCtx.
func applyExtracts(extractors []configyml.Extractor, body []byte, fCtx cli_functions.FlowContext) {
	if len(extractors) == 0 || len(body) == 0 {
		return
	}
	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return
	}
	for _, e := range extractors {
		parts := strings.Split(e.Field, ".")
		if val := navigate(parsed, parts); val != nil {
			fCtx[e.As] = val
		}
	}
}

// navigate traverses nested maps/slices following dot-split path segments.
// Recognises "response" and "body" as pass-through prefixes, and supports
// array index notation such as "items[0]".
func navigate(v any, parts []string) any {
	for _, part := range parts {
		if part == "response" || part == "body" {
			continue
		}
		if idx := arrayIndex(part); idx >= 0 {
			name := part[:strings.Index(part, "[")]
			if name != "" {
				v = mapGet(v, name)
			}
			if arr, ok := v.([]any); ok && idx < len(arr) {
				v = arr[idx]
			} else {
				return nil
			}
			continue
		}
		v = mapGet(v, part)
		if v == nil {
			return nil
		}
	}
	return v
}

func mapGet(v any, key string) any {
	if m, ok := v.(map[string]any); ok {
		return m[key]
	}
	return nil
}

func arrayIndex(s string) int {
	start := strings.Index(s, "[")
	end := strings.Index(s, "]")
	if start < 0 || end < 0 || end <= start+1 {
		return -1
	}
	var idx int
	_, _ = fmt.Sscanf(s[start+1:end], "%d", &idx)
	return idx
}

// ---------------------------------------------------------------------------
// Template rendering
// ---------------------------------------------------------------------------

// renderTemplate expands ${ENV} variables and then renders s as a Go
// text/template against data.
func renderTemplate(tmplStr string, data map[string]any) string {
	tmplStr = os.ExpandEnv(tmplStr)
	tmpl, err := template.New("").Parse(tmplStr)
	if err != nil {
		return tmplStr
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return tmplStr
	}
	return buf.String()
}

// ---------------------------------------------------------------------------
// Random payload selection
// ---------------------------------------------------------------------------

// randomPayload picks a random .json file from dir.
func randomPayload(dir string) ([]byte, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no payload files in %s", dir)
	}
	return os.ReadFile(files[rand.Intn(len(files))]) //nolint:gosec
}

// ---------------------------------------------------------------------------
// Latency statistics
// ---------------------------------------------------------------------------

// computeLatencyStats computes the full latency statistics suite from a slice
// of raw durations. All values are expressed in milliseconds.
func computeLatencyStats(d []time.Duration) cli_functions.LatencyStats {
	if len(d) == 0 {
		return cli_functions.LatencyStats{}
	}
	sorted := make([]time.Duration, len(d))
	copy(sorted, d)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	pct := func(p float64) float64 {
		idx := int(float64(len(sorted)-1) * p)
		if idx >= len(sorted) {
			idx = len(sorted) - 1
		}
		return toMS(sorted[idx])
	}

	var sum float64
	for _, v := range sorted {
		sum += toMS(v)
	}

	return cli_functions.LatencyStats{
		Min:  toMS(sorted[0]),
		Max:  toMS(sorted[len(sorted)-1]),
		Mean: sum / float64(len(sorted)),
		P50:  pct(0.50),
		P90:  pct(0.90),
		P95:  pct(0.95),
		P99:  pct(0.99),
	}
}

func toMS(d time.Duration) float64 {
	return float64(d.Nanoseconds()) / 1e6
}

// ---------------------------------------------------------------------------
// Helper utilities
// ---------------------------------------------------------------------------

// stepID returns the canonical identifier for a step, preferring s.ID and
// falling back to "<METHOD>_<endpoint>".
func stepID(s configyml.Step) string {
	if s.ID != "" {
		return s.ID
	}
	return strings.ToLower(s.Method) + "_" + strings.ReplaceAll(s.Endpoint, "/", "_")
}

// copyContext returns a shallow copy of fCtx.
func copyContext(src cli_functions.FlowContext) cli_functions.FlowContext {
	dst := make(cli_functions.FlowContext, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// copyInt64Map returns a shallow copy of m.
func copyInt64Map(m map[string]int64) map[string]int64 {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]int64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// expandEnv is a thin wrapper around os.ExpandEnv.
func expandEnv(s string) string {
	return os.ExpandEnv(s)
}

// abs64 returns the absolute value of f.
func abs64(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
