package runflowengine

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ---------------------------------------------------------------------------
// stepResult — internal to this package
// ---------------------------------------------------------------------------

// stepResult holds the outcome of a single HTTP step execution.
type stepResult struct {
	StepID      string
	StatusCode  int
	Latency     time.Duration
	Err         error
	Body        []byte
	RequestBody []byte
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

func (b *stepMetricsBucket) toReport(id string) StepReport {
	b.mu.Lock()
	lats := make([]time.Duration, len(b.latencies))
	copy(lats, b.latencies)
	b.mu.Unlock()

	return StepReport{
		StepID:    id,
		Requests:  b.requests,
		Errors:    b.errors,
		LatencyMS: computeLatencyStats(lats),
	}
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
func computeLatencyStats(d []time.Duration) LatencyStats {
	if len(d) == 0 {
		return LatencyStats{}
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

	return LatencyStats{
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
