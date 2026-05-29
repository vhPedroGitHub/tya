// Package cli_functions — run_scheduler.go
//
// RunScheduler drives the execution of a set of flows respecting their
// depends_on DAG. Each flow runs in its own goroutine; flows with
// dependencies block until every dependency has signalled completion via a
// channel.
// done to its dependents.
package runflowengine

import (
	"sync"

	"github.com/vhPedroGitHub/tya/pkg/configyml"

	"go.uber.org/zap"
)

// IterateFlowExecutorFunc runs an iterate flow that processes every item in
// a global-bucket list. The itemVar is the template key (default "item").
type IterateFlowExecutorFunc func(flow configyml.Flow) FlowReport

// FlowExecutorFunc is the signature of the function that actually runs a
// single flow and returns its report. The scheduler calls this function once
// per flow. The lastCtx parameter receives the final execution context of the
// parent flow (non-nil only when the flow is a dependency of a wire-flow
// invocation; for top-level flows it will be nil on entry).
type FlowExecutorFunc func(flow configyml.Flow) (FlowReport, FlowContext)

// WireFlowExecutorFunc runs a wire-flow given an inherited parent context
// and returns step-level metrics.
type WireFlowExecutorFunc func(wf configyml.WireFlow, parentCtx FlowContext) []StepReport

// FlowContext is a per-goroutine key-value map used to pass extracted values
// between steps and to wire-flows.
type FlowContext map[string]any

// FlowReport carries the metrics produced by a single flow execution.
type FlowReport struct {
	Name               string `json:"name"`
	Type               string `json:"type"`
	TotalRequests      int64  `json:"total_requests"`
	SuccessfulRequests int64  `json:"successful_requests"`
	FailedRequests     int64  `json:"failed_requests"`
	// RPSAchieved is the measured HTTP calls per second during the analysis window.
	RPSAchieved float64 `json:"rps_achieved"`
	// IterationsPerSecond is the measured flow iterations per second (= RPSAchieved / N steps).
	IterationsPerSecond float64          `json:"iterations_per_second,omitempty"`
	LatencyMS           LatencyStats     `json:"latency_ms"`
	ErrorsByStatus      map[string]int64 `json:"errors_by_status,omitempty"`
	ErrorsByStep        map[string]int64 `json:"errors_by_step,omitempty"`
	// Steps is a per-step breakdown included in the flow report.
	Steps []StepReport `json:"steps,omitempty"`

	// Ramp-up and adaptive engine fields.

	// RampUpDurationS is the wall-clock time (seconds) spent in the ramp-up
	// phase before the plateau was declared.
	RampUpDurationS float64 `json:"ramp_up_duration_s"`
	// AnalysisDurationS is the wall-clock time (seconds) of the stable
	// analysis window (duration timer starts after plateau is detected).
	AnalysisDurationS float64 `json:"analysis_duration_s"`
	// StableRPSTarget is the configured requests_per_second goal.
	StableRPSTarget float64 `json:"stable_rps_target"`
	// StableRPSAchieved is the measured RPS during the analysis window.
	StableRPSAchieved float64 `json:"stable_rps_achieved"`
	// StableRPSMaxReached is true when the system could not reach
	// StableRPSTarget and ran the analysis at the highest achievable rate.
	StableRPSMaxReached bool `json:"stable_rps_max_reached,omitempty"`
	// ForcedPlateau is true when the ramp-up was terminated early (either by
	// max_negative_resets or max_ramp_duration) and the analysis RPS was
	// derived from the best stable windows observed rather than a natural
	// plateau.
	ForcedPlateau bool `json:"forced_plateau,omitempty"`
	// ForcedPlateauReason describes why the plateau was forced:
	// "negative_resets" or "timeout".
	ForcedPlateauReason string `json:"forced_plateau_reason,omitempty"`
	// ForcedPlateauRPS is the averaged RPS used for the analysis window when
	// a forced plateau occurs.
	ForcedPlateauRPS float64 `json:"forced_plateau_rps,omitempty"`
	// NegativeResets is the total count of negative-reset windows observed
	// during the entire ramp-up phase. When this reaches max_negative_resets
	// a forced plateau is triggered (resets do not need to be consecutive).
	NegativeResets int `json:"negative_resets,omitempty"`
	// RampUpWindows holds per-window diagnostics recorded during ramp-up.
	RampUpWindows []RampUpWindow `json:"ramp_up_windows,omitempty"`
	// AvgConcurrency is the time-averaged number of goroutines running
	// concurrently during the analysis window.
	AvgConcurrency float64 `json:"avg_concurrency"`
	// MaxConcurrency is the peak number of goroutines observed concurrently
	// during the analysis window.
	MaxConcurrency int64 `json:"max_concurrency"`
	// ThinkTimeAppliedMs is the mean think-time sleep (ms) applied at the
	// end of each goroutine iteration to regulate the flow rhythm.
	ThinkTimeAppliedMs float64 `json:"think_time_applied_ms,omitempty"`
}

// LatencyStats holds the full suite of latency percentiles (in milliseconds).
type LatencyStats struct {
	Min  float64 `json:"min"`
	Max  float64 `json:"max"`
	Mean float64 `json:"mean"`
	P50  float64 `json:"p50"`
	P90  float64 `json:"p90"`
	P95  float64 `json:"p95"`
	P99  float64 `json:"p99"`
}

// StepReport holds per-step metrics for a single flow run.
type StepReport struct {
	StepID    string       `json:"step_id"`
	Requests  int64        `json:"requests"`
	Errors    int64        `json:"errors"`
	LatencyMS LatencyStats `json:"latency_ms"`
}

// RampUpWindow records the observed metrics for a single ramp-up step window.
type RampUpWindow struct {
	// WindowIndex is the 1-based sequence number of this window.
	WindowIndex int `json:"window_index"`
	// TargetRPS is the HTTP calls/s the engine was aiming for in this window.
	TargetRPS float64 `json:"target_rps"`
	// ObservedRPS is the measured HTTP calls/s during this window.
	ObservedRPS float64 `json:"observed_rps"`
	// Variation is the relative change vs the previous window (0 on first window).
	Variation float64 `json:"variation"`
	// Stable is true when variation <= stability_threshold.
	Stable bool `json:"stable"`
	// NegativeReset is true when this window's RPS dropped below the previous
	// window's RPS (a downward instability event).
	NegativeReset bool `json:"negative_reset,omitempty"`
	// ConsecutiveNegativeResets is the running count of consecutive negative
	// resets at the end of this window.
	ConsecutiveNegativeResets int `json:"consecutive_negative_resets,omitempty"`
}

// RunScheduler executes a list of flows in dependency order. It starts each
// flow as soon as all of its dependencies have signalled completion.
//
// flowExec and iterateExec are injectable so that the scheduler
// can be tested independently of HTTP concerns.
func RunScheduler(
	log *zap.Logger,
	flows []configyml.Flow,
	flowExec FlowExecutorFunc,
	iterateExec IterateFlowExecutorFunc,
) map[string]FlowReport {
	// One "done" channel per flow — closed when the flow (including its
	done := make(map[string]chan struct{}, len(flows))
	for _, f := range flows {
		done[f.Name] = make(chan struct{})
	}

	results := make(map[string]FlowReport, len(flows))
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, f := range flows {
		f := f // capture for goroutine
		wg.Add(1)
		go func() {
			defer wg.Done()

			// Block until every dependency signals done.
			for _, dep := range f.DependsOn {
				if ch, ok := done[dep]; ok {
					<-ch
				}
			}

			log.Info("flow starting",
				zap.String("flow", f.Name),
				zap.String("type", f.Type),
				zap.Strings("depends_on", f.DependsOn),
			)

			var report FlowReport

			if f.Type == "iterate" {
				report = iterateExec(f)
				report.Name = f.Name
				report.Type = f.Type
			}

			mu.Lock()
			results[f.Name] = report
			mu.Unlock()

			log.Info("flow finished",
				zap.String("flow", f.Name),
				zap.Int64("total_requests", report.TotalRequests),
				zap.Int64("errors", report.FailedRequests),
			)

			// Signal dependents.
			close(done[f.Name])
		}()
	}

	wg.Wait()
	return results
}
