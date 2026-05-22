package k6gen

import (
	"fmt"
	"strings"

	"tya/pkg/configyml"
)

// GenerateExtractCode generates JS code that extracts values from an HTTP
// response body and stores them in the flow context. The code assumes:
//   - `res` is the k6 HTTP response
//   - `ctx` is the flow context object
func GenerateExtractCode(extractors []configyml.Extractor, responseVar string) string {
	if len(extractors) == 0 {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "    if (%s.status < 400) {\n", responseVar)
	b.WriteString("      try {\n")
	fmt.Fprintf(&b, "        const body = %s.json();\n", responseVar)

	for _, e := range extractors {
		jsField := goPathToJS(e.Field)
		b.WriteString("        {\n")
		fmt.Fprintf(&b, "          const val = navigate(body, %s);\n", JsString(jsField))
		b.WriteString("          if (val !== null && val !== undefined) {\n")
		fmt.Fprintf(&b, "            ctx['%s'] = val;\n", e.As)

		if e.Global {
			b.WriteString("            if (!ctx['__global__']) ctx['__global__'] = {};\n")
			fmt.Fprintf(&b, "            if (!ctx['__global__']['%s']) ctx['__global__']['%s'] = {};\n", "flow", "flow")
			fmt.Fprintf(&b, "            ctx['__global__']['%s']['%s'] = val;\n", "flow", e.As)
		}
		if e.GlobalList {
			b.WriteString("            if (!ctx['__global_lists__']) ctx['__global_lists__'] = {};\n")
			fmt.Fprintf(&b, "            if (!ctx['__global_lists__']['%s']) ctx['__global_lists__']['%s'] = {};\n", "flow", "flow")
			fmt.Fprintf(&b, "            if (!ctx['__global_lists__']['%s']['%s']) ctx['__global_lists__']['%s']['%s'] = [];\n", "flow", e.As, "flow", e.As)
			fmt.Fprintf(&b, "            ctx['__global_lists__']['%s']['%s'].push(val);\n", "flow", e.As)
		}

		b.WriteString("          }\n")
		b.WriteString("        }\n")
	}

	b.WriteString("      } catch(e) { /* JSON parse error, skip extracts */ }\n")
	b.WriteString("    }\n")

	return b.String()
}

// goPathToJS converts a TYA dot-path like "response.body.items[0].id" to
// the equivalent JS navigation path. Strips "response.body." prefix.
func goPathToJS(path string) string {
	// Strip response.body. prefix
	path = strings.TrimPrefix(path, "response.")
	path = strings.TrimPrefix(path, "body.")
	return path
}

// GenerateExtractWithGlobalContext generates extraction code that writes to a
// shared global context object, scoped by flow name.
func GenerateExtractWithGlobalContext(extractors []configyml.Extractor, responseVar, flowName string) string {
	if len(extractors) == 0 {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "    if (%s.status < 400) {\n", responseVar)
	b.WriteString("      try {\n")
	fmt.Fprintf(&b, "        const body = %s.json();\n", responseVar)

	for _, e := range extractors {
		jsField := goPathToJS(e.Field)
		b.WriteString("        {\n")
		fmt.Fprintf(&b, "          const val = navigate(body, %s);\n", JsString(jsField))
		b.WriteString("          if (val !== null && val !== undefined) {\n")
		fmt.Fprintf(&b, "            ctx['%s'] = val;\n", e.As)

		if e.Global {
			b.WriteString("            if (!ctx['__global__']) ctx['__global__'] = {};\n")
			fmt.Fprintf(&b, "            if (!ctx['__global__']['%s']) ctx['__global__']['%s'] = {};\n", flowName, flowName)
			fmt.Fprintf(&b, "            ctx['__global__']['%s']['%s'] = val;\n", flowName, e.As)
		}
		if e.GlobalList {
			b.WriteString("            if (!ctx['__global_lists__']) ctx['__global_lists__'] = {};\n")
			fmt.Fprintf(&b, "            if (!ctx['__global_lists__']['%s']) ctx['__global_lists__']['%s'] = {};\n", flowName, flowName)
			fmt.Fprintf(&b, "            if (!ctx['__global_lists__']['%s']['%s']) ctx['__global_lists__']['%s']['%s'] = [];\n", flowName, e.As, flowName, e.As)
			fmt.Fprintf(&b, "            ctx['__global_lists__']['%s']['%s'].push(val);\n", flowName, e.As)
		}

		b.WriteString("          }\n")
		b.WriteString("        }\n")
	}

	b.WriteString("      } catch(e) { /* JSON parse error, skip extracts */ }\n")
	b.WriteString("    }\n")

	return b.String()
}
