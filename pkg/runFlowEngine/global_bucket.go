package runflowengine

import "sync"

// GlobalBucket is a thread-safe, cross-flow key-value store. Values are
// namespaced by the name of the flow that wrote them, so keys from different
// flows never collide. A single GlobalBucket instance is created in runFlows
// and passed to every executeFlow call.
//
// Two kinds of data can be stored:
//   - Scalar values via Set (last-write-wins) — written when global: true
//   - List values via AppendList (append-only) — written when global_list: true
//
// Write: applyExtracts writes a value when the extractor has Global/GlobalList.
// Read:  at the start of each goroutine iteration the bucket snapshot is
//
//	injected into fCtx["global"] / fCtx["global_lists"], making values
//	accessible in templates via globalGet / globalGetList.
type GlobalBucket struct {
	mu    sync.RWMutex
	data  map[string]map[string]any
	lists map[string]map[string][]any
}

// NewGlobalBucket returns an empty, ready-to-use GlobalBucket.
func NewGlobalBucket() *GlobalBucket {
	return &GlobalBucket{
		data:  make(map[string]map[string]any),
		lists: make(map[string]map[string][]any),
	}
}

// Set stores value under the namespace of flowName with the given key.
// Concurrent writes use last-write-wins semantics.
func (b *GlobalBucket) Set(flowName, key string, value any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.data[flowName] == nil {
		b.data[flowName] = make(map[string]any)
	}
	b.data[flowName][key] = value
}

// AppendList appends value to the list stored under flowName/key.
// Concurrent appends are safe; the list grows atomically.
func (b *GlobalBucket) AppendList(flowName, key string, value any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.lists[flowName] == nil {
		b.lists[flowName] = make(map[string][]any)
	}
	b.lists[flowName][key] = append(b.lists[flowName][key], value)
}

// GetList returns a copy of the list stored under flowName/key.
func (b *GlobalBucket) GetList(flowName, key string) []any {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.lists[flowName] == nil {
		return nil
	}
	src := b.lists[flowName][key]
	if len(src) == 0 {
		return nil
	}
	cp := make([]any, len(src))
	copy(cp, src)
	return cp
}

// Snapshot returns a deep copy of all scalar values as
// map[string]map[string]any. The copy is safe to embed in a flow context
// without holding the lock.
func (b *GlobalBucket) Snapshot() map[string]map[string]any {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make(map[string]map[string]any, len(b.data))
	for flow, ns := range b.data {
		cp := make(map[string]any, len(ns))
		for k, v := range ns {
			cp[k] = v
		}
		out[flow] = cp
	}
	return out
}

// SnapshotLists returns a deep copy of all list values as
// map[string]map[string][]any.
func (b *GlobalBucket) SnapshotLists() map[string]map[string][]any {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make(map[string]map[string][]any, len(b.lists))
	for flow, ns := range b.lists {
		cp := make(map[string][]any, len(ns))
		for k, src := range ns {
			dst := make([]any, len(src))
			copy(dst, src)
			cp[k] = dst
		}
		out[flow] = cp
	}
	return out
}
