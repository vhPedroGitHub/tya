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
				res := executeStep(log, step, fCtx, authMap[flow.Auth])
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
					res := executeStep(log, step, fCtx, authMap[flow.Auth])
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
		ErrorsByStatus:     ebs,
		ErrorsByStep:       ebStep,
	}
}

// ---------------------------------------------------------------------------
// Step execution
// ---------------------------------------------------------------------------

// executeStep performs a single HTTP request described by step, using fCtx
// to resolve template variables and auth credentials.
func executeStep(log *zap.Logger, step configyml.Step, fCtx FlowContext, auth configyml.AuthProfile) stepResult {
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

	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return stepResult{Err: fmt.Errorf("build request: %w", err)}
	}
	if bodyReader != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	// If a run-context was attached to the flow context, use it to allow
	// cancellation of in-flight HTTP requests.
	if rc, ok := fCtx["_run_ctx"]; ok {
		if rctx, ok2 := rc.(context.Context); ok2 && rctx != nil {
			req = req.WithContext(rctx)
		}
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
