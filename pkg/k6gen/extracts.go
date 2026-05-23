package k6gen

import (
	"fmt"
	"strings"

	"github.com/vhPedroGitHub/tya/pkg/configyml"
)

// GenerateExtractCode generates JS code that extracts values from an HTTP
// response (or request) body and stores them in the flow context. The code
// assumes:
//   - `res`     is the k6 HTTP response object
//   - `reqBody` is the raw request body string (may be empty)
//   - `ctx`     is the flow context object
//
// When an extractor has From == "request", the value is read from the parsed
// request body instead of the response body.
//
// When GlobalList is true and Expand is true, and the extracted value is an
// array, each element is pushed individually instead of the whole array.
func GenerateExtractCode(extractors []configyml.Extractor, responseVar string) string {
	if len(extractors) == 0 {
		return ""
	}

	var b strings.Builder
	// Split extractors by source so we parse each body at most once.
	var respExtractors, reqExtractors []configyml.Extractor
	for _, e := range extractors {
		if strings.EqualFold(e.From, "request") {
			reqExtractors = append(reqExtractors, e)
		} else {
			respExtractors = append(respExtractors, e)
		}
	}

	// Response extractors.
	if len(respExtractors) > 0 {
		fmt.Fprintf(&b, "    if (%s.status < 400) {\n", responseVar)
		b.WriteString("      try {\n")
		fmt.Fprintf(&b, "        const body = %s.json();\n", responseVar)
		for _, e := range respExtractors {
			jsField := goPathToJS(e.Field)
			writeExtractBlock(&b, e, "body", jsField)
		}
		b.WriteString("      } catch(e) { /* JSON parse error, skip extracts */ }\n")
		b.WriteString("    }\n")
	}

	// Request body extractors.
	if len(reqExtractors) > 0 {
		b.WriteString("    try {\n")
		b.WriteString("      const reqBodyParsed = JSON.parse(reqBody || '{}');\n")
		for _, e := range reqExtractors {
			jsField := goPathToJS(e.Field)
			writeExtractBlock(&b, e, "reqBodyParsed", jsField)
		}
		b.WriteString("    } catch(e) { /* request body parse error, skip extracts */ }\n")
	}

	return b.String()
}

// writeExtractBlock writes the JS block that navigates sourceVar using jsField
// and stores the result in ctx, global, and/or global_list.
func writeExtractBlock(b *strings.Builder, e configyml.Extractor, sourceVar, jsField string) {
	b.WriteString("        {\n")
	fmt.Fprintf(b, "          const val = navigate(%s, %s);\n", sourceVar, JsString(jsField))
	b.WriteString("          if (val !== null && val !== undefined) {\n")
	fmt.Fprintf(b, "            ctx['%s'] = val;\n", e.As)

	if e.Global {
		b.WriteString("            if (!ctx['__global__']) ctx['__global__'] = {};\n")
		fmt.Fprintf(b, "            if (!ctx['__global__']['%s']) ctx['__global__']['%s'] = {};\n", "flow", "flow")
		fmt.Fprintf(b, "            ctx['__global__']['%s']['%s'] = val;\n", "flow", e.As)
	}
	if e.GlobalList {
		b.WriteString("            if (!ctx['__global_lists__']) ctx['__global_lists__'] = {};\n")
		fmt.Fprintf(b, "            if (!ctx['__global_lists__']['%s']) ctx['__global_lists__']['%s'] = {};\n", "flow", "flow")
		fmt.Fprintf(b, "            if (!ctx['__global_lists__']['%s']['%s']) ctx['__global_lists__']['%s']['%s'] = [];\n", "flow", e.As, "flow", e.As)
		if e.Expand {
			// Expand: if val is an array, push each element individually.
			b.WriteString("            if (Array.isArray(val)) {\n")
			fmt.Fprintf(b, "              val.forEach(function(elem) { ctx['__global_lists__']['%s']['%s'].push(elem); });\n", "flow", e.As)
			b.WriteString("            } else {\n")
			fmt.Fprintf(b, "              ctx['__global_lists__']['%s']['%s'].push(val);\n", "flow", e.As)
			b.WriteString("            }\n")
		} else {
			fmt.Fprintf(b, "            ctx['__global_lists__']['%s']['%s'].push(val);\n", "flow", e.As)
		}
	}

	b.WriteString("          }\n")
	b.WriteString("        }\n")
}

// goPathToJS converts a TYA dot-path like "response.body.items[0].id" to
// the equivalent JS navigation path. Strips "response.body." and
// "request.body." prefixes.
func goPathToJS(path string) string {
	path = strings.TrimPrefix(path, "response.")
	path = strings.TrimPrefix(path, "request.")
	path = strings.TrimPrefix(path, "body.")
	return path
}

// GenerateExtractWithGlobalContext generates extraction code that writes to a
// shared global context object, scoped by flow name, and emits TYA_GLOBAL
// sentinels for cross-subprocess state propagation (used by runk6s).
func GenerateExtractWithGlobalContext(extractors []configyml.Extractor, responseVar, flowName string) string {
	if len(extractors) == 0 {
		return ""
	}

	var b strings.Builder

	var respExtractors, reqExtractors []configyml.Extractor
	for _, e := range extractors {
		if strings.EqualFold(e.From, "request") {
			reqExtractors = append(reqExtractors, e)
		} else {
			respExtractors = append(respExtractors, e)
		}
	}

	// Response extractors.
	if len(respExtractors) > 0 {
		fmt.Fprintf(&b, "    if (%s.status < 400) {\n", responseVar)
		b.WriteString("      try {\n")
		fmt.Fprintf(&b, "        const body = %s.json();\n", responseVar)
		for _, e := range respExtractors {
			jsField := goPathToJS(e.Field)
			writeExtractWithSentinelBlock(&b, e, "body", jsField, flowName)
		}
		b.WriteString("      } catch(e) { /* JSON parse error, skip extracts */ }\n")
		b.WriteString("    }\n")
	}

	// Request body extractors.
	if len(reqExtractors) > 0 {
		b.WriteString("    try {\n")
		b.WriteString("      const reqBodyParsed = JSON.parse(reqBody || '{}');\n")
		for _, e := range reqExtractors {
			jsField := goPathToJS(e.Field)
			writeExtractWithSentinelBlock(&b, e, "reqBodyParsed", jsField, flowName)
		}
		b.WriteString("    } catch(e) { /* request body parse error, skip extracts */ }\n")
	}

	return b.String()
}

// writeExtractWithSentinelBlock is like writeExtractBlock but also emits
// TYA_GLOBAL console.log sentinels for runk6s cross-process propagation.
func writeExtractWithSentinelBlock(b *strings.Builder, e configyml.Extractor, sourceVar, jsField, flowName string) {
	b.WriteString("        {\n")
	fmt.Fprintf(b, "          const val = navigate(%s, %s);\n", sourceVar, JsString(jsField))
	b.WriteString("          if (val !== null && val !== undefined) {\n")
	fmt.Fprintf(b, "            ctx['%s'] = val;\n", e.As)

	if e.Global {
		b.WriteString("            if (!ctx['__global__']) ctx['__global__'] = {};\n")
		fmt.Fprintf(b, "            if (!ctx['__global__']['%s']) ctx['__global__']['%s'] = {};\n", flowName, flowName)
		fmt.Fprintf(b, "            ctx['__global__']['%s']['%s'] = val;\n", flowName, e.As)
		fmt.Fprintf(b, "            console.log('TYA_GLOBAL: ' + JSON.stringify({flow:%s, key:%s, value:val, list:false}));\n",
			JsString(flowName), JsString(e.As))
	}
	if e.GlobalList {
		b.WriteString("            if (!ctx['__global_lists__']) ctx['__global_lists__'] = {};\n")
		fmt.Fprintf(b, "            if (!ctx['__global_lists__']['%s']) ctx['__global_lists__']['%s'] = {};\n", flowName, flowName)
		fmt.Fprintf(b, "            if (!ctx['__global_lists__']['%s']['%s']) ctx['__global_lists__']['%s']['%s'] = [];\n", flowName, e.As, flowName, e.As)
		if e.Expand {
			// Expand: if val is an array, emit one sentinel per element.
			b.WriteString("            if (Array.isArray(val)) {\n")
			fmt.Fprintf(b, "              val.forEach(function(elem) {\n")
			fmt.Fprintf(b, "                ctx['__global_lists__']['%s']['%s'].push(elem);\n", flowName, e.As)
			fmt.Fprintf(b, "                console.log('TYA_GLOBAL: ' + JSON.stringify({flow:%s, key:%s, value:elem, list:true}));\n",
				JsString(flowName), JsString(e.As))
			b.WriteString("              });\n")
			b.WriteString("            } else {\n")
			fmt.Fprintf(b, "              ctx['__global_lists__']['%s']['%s'].push(val);\n", flowName, e.As)
			fmt.Fprintf(b, "              console.log('TYA_GLOBAL: ' + JSON.stringify({flow:%s, key:%s, value:val, list:true}));\n",
				JsString(flowName), JsString(e.As))
			b.WriteString("            }\n")
		} else {
			fmt.Fprintf(b, "            ctx['__global_lists__']['%s']['%s'].push(val);\n", flowName, e.As)
			fmt.Fprintf(b, "            console.log('TYA_GLOBAL: ' + JSON.stringify({flow:%s, key:%s, value:val, list:true}));\n",
				JsString(flowName), JsString(e.As))
		}
	}

	b.WriteString("          }\n")
	b.WriteString("        }\n")
}
