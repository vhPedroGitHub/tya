package runflowengine

import "sync"

// updateFn is an optional callback invoked with periodic flow snapshots.
// Consumers may register a function to receive live FlowReport updates.
var (
	updateMu sync.RWMutex
	updateFn func(flowName string, r FlowReport)
)

// RegisterUpdateFunc registers a callback to receive live FlowReport updates.
// Passing nil clears the callback.
func RegisterUpdateFunc(fn func(flowName string, r FlowReport)) {
	updateMu.Lock()
	defer updateMu.Unlock()
	updateFn = fn
}

// ClearUpdateFunc removes any registered update callback.
func ClearUpdateFunc() {
	RegisterUpdateFunc(nil)
}

// sendUpdate invokes the registered callback if present.
func sendUpdate(flowName string, r FlowReport) {
	updateMu.RLock()
	fn := updateFn
	updateMu.RUnlock()
	if fn != nil {
		// call without holding the lock
		fn(flowName, r)
	}
}
