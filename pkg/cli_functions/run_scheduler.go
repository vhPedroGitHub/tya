// Package cli_functions — run_scheduler.go
//
// RunScheduler drives the execution of a set of flows respecting their
// depends_on DAG. Each flow runs in its own goroutine; flows with
// dependencies block until every dependency has signalled completion via a
// channel. After a parent flow finishes its goroutine pool, any declared
// children (wire-flows) are executed sequentially before the parent signals
// done to its dependents.
package cli_functions

import (
	"sync"

	"tya/pkg/configyml"

	"go.uber.org/zap"
)

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
	Name                 string       `json:"name"`
	Type                 string       `json:"type"`
	TotalRequests        int64        `json:"total_requests"`
	SuccessfulRequests   int64        `json:"successful_requests"`
	FailedRequests       int64        `json:"failed_requests"`
	RPSAchieved          float64      `json:"rps_achieved"`
	LatencyMS            LatencyStats `json:"latency_ms"`
	Steps                []StepReport `json:"steps"`
	Children             []StepReport `json:"children,omitempty"`
	ErrorsByStatus       map[string]int64 `json:"errors_by_status,omitempty"`
	ErrorsByStep         map[string]int64 `json:"errors_by_step,omitempty"`
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
	StepID     string       `json:"step_id"`
	Requests   int64        `json:"requests"`
	Errors     int64        `json:"errors"`
	LatencyMS  LatencyStats `json:"latency_ms"`
}

// RunScheduler executes a list of flows in dependency order. It starts each
// flow as soon as all of its dependencies have signalled completion. Children
// (wire-flows) are executed synchronously after the parent's goroutine pool
// drains and before the parent signals done.
//
// flowExec and wireExec are injectable so that the scheduler can be tested
// independently of HTTP concerns.
func RunScheduler(
	log *zap.Logger,
	flows []configyml.Flow,
	flowExec FlowExecutorFunc,
	wireExec WireFlowExecutorFunc,
) map[string]FlowReport {
	// One "done" channel per flow — closed when the flow (including its
	// wire-flow children) has finished.
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

			// Execute the main flow body.
			report, lastCtx := flowExec(f)
			report.Name = f.Name
			report.Type = f.Type

			// Execute wire-flow children sequentially.
			for _, child := range f.Children {
				log.Info("wire-flow starting",
					zap.String("parent", f.Name),
					zap.String("child", child.Name),
				)
				childSteps := wireExec(child, lastCtx)
				report.Children = append(report.Children, childSteps...)
				log.Info("wire-flow finished",
					zap.String("parent", f.Name),
					zap.String("child", child.Name),
					zap.Int("steps", len(childSteps)),
				)
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
