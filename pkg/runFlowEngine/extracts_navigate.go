package runflowengine

import (
	"encoding/json"
	"strings"

	"github.com/vhPedroGitHub/tya/pkg/configyml"
)

// applyExtracts pulls values out of a JSON response (or request) body and
// stores them in fCtx and optionally in the GlobalBucket.
//
// When an extractor has From == "request", the value is extracted from
// requestBody instead of responseBody.
//
// When GlobalList is true and Expand is true, and the extracted value is a
// JSON array ([]any), each element of the array is appended individually to
// the GlobalBucket list instead of storing the whole array as a single item.
func applyExtracts(extractors []configyml.Extractor, responseBody []byte, requestBody []byte, fCtx FlowContext, flowName string, bucket *GlobalBucket) {
	if len(extractors) == 0 {
		return
	}

	// Parse response body once (may be nil/empty for non-JSON responses).
	var parsedResponse any
	if len(responseBody) > 0 {
		_ = json.Unmarshal(responseBody, &parsedResponse)
	}

	// Parse request body once.
	var parsedRequest any
	if len(requestBody) > 0 {
		_ = json.Unmarshal(requestBody, &parsedRequest)
	}

	for _, e := range extractors {
		// Select source document.
		source := parsedResponse
		if strings.EqualFold(e.From, "request") {
			source = parsedRequest
			// For request paths, strip "request.body." prefix via navigate's
			// existing "response"/"body" pass-through logic — we reuse the same
			// helper by treating "request" as a skip-word too.
		}
		if source == nil {
			continue
		}

		parts := strings.Split(e.Field, ".")
		val := navigate(source, parts)
		if val == nil {
			continue
		}

		fCtx[e.As] = val

		if e.Global {
			bucket.Set(flowName, e.As, val)
		}

		if e.GlobalList {
			if e.Expand {
				// If the value is a []any, expand each element as a separate entry.
				if arr, ok := val.([]any); ok {
					for _, elem := range arr {
						bucket.AppendList(flowName, e.As, elem)
					}
				} else {
					// Not an array — fall back to appending the value as-is.
					bucket.AppendList(flowName, e.As, val)
				}
			} else {
				bucket.AppendList(flowName, e.As, val)
			}
		}
	}
}

// navigate traverses nested maps/slices following dot-split path segments.
// Recognises "response", "request", and "body" as pass-through prefixes, and
// supports array index notation such as "items[0]".
func navigate(v any, parts []string) any {
	for _, part := range parts {
		if part == "response" || part == "request" || part == "body" {
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
