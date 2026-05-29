package runflowengine

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vhPedroGitHub/tya/pkg/configyml"
	"github.com/vhPedroGitHub/tya/pkg/models"
	"go.uber.org/zap"
)

// ExecuteFlowv2 runs a single flow according to its type and options.
// It returns a FlowReport and the last successful execution context
func ExecuteFlowv2(
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
	var totalRequests, totalErrors, totalIterations int64
	var allLatsMu sync.Mutex
	var allLats []time.Duration

	// errorsByStatus and errorsByStep for the report.
	errByStatus := make(map[string]int64)
	errByStep := make(map[string]int64)
	var errMu sync.Mutex

	// extraReportFields is set by the adaptive engine to populate the new report
	// fields; it is a no-op in test mode.
	extraReportFields := func(_ *FlowReport) {}

	// rpsAchieved is computed after the run; the adaptive engine overrides it.
	var rpsAchieved float64

	var lastCtxMu sync.Mutex
	var lastCtx FlowContext

	recordResult := func(id string, res stepResult) {
		RecordResult(id, res, &totalRequests, &totalErrors, &allLatsMu, &allLats, stepBuckets, errByStatus, errByStep, &errMu)
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
		// Delegate adaptive ramp-up + analysis to the refactored function.
		rampCfg := configyml.RampUp{}
		if flow.RampUp != nil {
			rampCfg = *flow.RampUp
		}
		rampCfg = rampCfg.Resolve()

		stepWin, err := time.ParseDuration(rampCfg.StepWindow)
		if err != nil {
			stepWin = 2 * time.Second
		}

		nSteps := float64(len(flow.Steps))
		if nSteps < 1 {
			nSteps = 1
		}

		initRPS := rampCfg.InitialRPS
		revisionerRampUpandExecuteFlow(log, flow, rampCfg, initRPS, flow.RequestsPerSecond, stepWin, nSteps, bucket, authMap, baseURL, lastCtx, duration)
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

	// Build per-step reports.
	stepReports := make([]StepReport, 0, len(flow.Steps))
	for _, s := range flow.Steps {
		id := stepID(s)
		stepReports = append(stepReports, stepBuckets[id].toReport(id))
	}

	report := FlowReport{
		TotalRequests:      totalRequests,
		SuccessfulRequests: successful,
		FailedRequests:     totalErrors,
		RPSAchieved:        rpsAchieved,
		LatencyMS:          computeLatencyStats(lats),
		ErrorsByStatus:     ebs,
		ErrorsByStep:       ebStep,
		Steps:              stepReports,
	}
	extraReportFields(&report)

	lastCtxMu.Lock()
	ctx := lastCtx
	lastCtxMu.Unlock()

	return report, ctx
}
