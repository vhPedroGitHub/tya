package runflowengine

import (
	"fmt"
	"os"
	"strings"

	"github.com/vhPedroGitHub/tya/pkg/configyml"
)

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

// stepID returns the canonical identifier for a step, preferring s.ID and
// falling back to "<METHOD>_<endpoint>".
func stepID(s configyml.Step) string {
	if s.ID != "" {
		return s.ID
	}
	return strings.ToLower(s.Method) + "_" + strings.ReplaceAll(s.Endpoint, "/", "_")
}

// copyContext returns a shallow copy of fCtx.
func copyContext(src FlowContext) FlowContext {
	dst := make(FlowContext, len(src))
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
