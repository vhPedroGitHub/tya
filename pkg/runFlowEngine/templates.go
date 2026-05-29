package runflowengine

import (
	"bytes"
	crand "crypto/rand"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"text/template"
	"time"
)

// tyaFuncMap returns the template.FuncMap available in all TYA template strings.
//
// Available functions:
//
//	uuid         — returns a new random UUID v4 string (e.g. "a1b2c3d4-…")
//	randomInt    — returns a random non-negative int as a string
//	randomInt64  — returns a random non-negative int64 as a string
//	randomDigits n — returns a string of n random decimal digits
//	timestamp    — returns the current Unix timestamp in seconds as a string
//	timestampMs  — returns the current Unix timestamp in milliseconds as a string
//	upper s      — converts s to upper-case
//	lower s      — converts s to lower-case
//	globalGet flowName key — reads a value from the global bucket snapshot
//	                          stored in .global (equivalent to index .global flowName key)
//	globalGetList flowName key — reads a list from the global bucket snapshot
//	                              stored in .global_lists
func tyaFuncMap(data map[string]any) template.FuncMap {
	return template.FuncMap{
		"uuid": func() string {
			b := make([]byte, 16)
			_, _ = crand.Read(b)
			b[6] = (b[6] & 0x0f) | 0x40
			b[8] = (b[8] & 0x3f) | 0x80
			return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
				b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
		},
		"randomInt": func() string {
			return strconv.Itoa(rand.Int()) //nolint:gosec
		},
		"randomInt64": func() string {
			return strconv.FormatInt(rand.Int63(), 10) //nolint:gosec
		},
		"randomDigits": func(n int) string {
			if n <= 0 {
				return ""
			}
			digits := make([]byte, n)
			for i := range digits {
				digits[i] = '0' + byte(rand.Intn(10)) //nolint:gosec
			}
			return string(digits)
		},
		"timestamp": func() string {
			return strconv.FormatInt(time.Now().Unix(), 10)
		},
		"timestampMs": func() string {
			return strconv.FormatInt(time.Now().UnixMilli(), 10)
		},
		"upper": strings.ToUpper,
		"lower": strings.ToLower,
		// globalGet looks up a value from the global bucket snapshot injected
		// into the flow context as fCtx["global"]. It is a convenience
		// alternative to {{ index .global "flow-name" "key" }}.
		"globalGet": func(flowName, key string) any {
			if data == nil {
				return nil
			}
			g, ok := data["global"].(map[string]map[string]any)
			if !ok {
				return nil
			}
			ns, ok := g[flowName]
			if !ok {
				return nil
			}
			return ns[key]
		},
		// globalGetList looks up a list from the global bucket snapshot
		// injected into the flow context as fCtx["global_lists"].
		"globalGetList": func(flowName, key string) any {
			if data == nil {
				return nil
			}
			g, ok := data["global_lists"].(map[string]map[string][]any)
			if !ok {
				return nil
			}
			ns, ok := g[flowName]
			if !ok {
				return nil
			}
			return ns[key]
		},
	}
}

// renderTemplate expands ${ENV} variables and then renders s as a Go
// text/template against data. All functions from tyaFuncMap() are available.
func renderTemplate(tmplStr string, data map[string]any) string {
	tmplStr = os.ExpandEnv(tmplStr)
	tmpl, err := template.New("").Funcs(tyaFuncMap(data)).Parse(tmplStr)
	if err != nil {
		return tmplStr
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return tmplStr
	}
	return buf.String()
}

// setNestedJSON sets a value at a dot-notation path inside a JSON object.
// For example, path "address.city" sets obj["address"]["city"] = value.
// Intermediate maps are created as needed. Existing non-map values at
// intermediate nodes are overwritten.
func setNestedJSON(obj map[string]any, path string, value any) {
	parts := strings.SplitN(path, ".", 2)
	if len(parts) == 1 {
		obj[path] = value
		return
	}
	key, rest := parts[0], parts[1]
	child, ok := obj[key].(map[string]any)
	if !ok {
		child = map[string]any{}
		obj[key] = child
	}
	setNestedJSON(child, rest, value)
}
