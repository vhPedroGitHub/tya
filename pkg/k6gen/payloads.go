package k6gen

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/vhPedroGitHub/tya/pkg/configyml"
)

// PayloadPool holds pre-generated payloads for an endpoint+method.
type PayloadPool struct {
	Endpoint string
	Method   string
	Payloads []string // JSON strings
}

// LoadPayloadPools reads all payload JSON files from api/<endpoint>/<method>/
// directories for the given flow's steps. Returns a map keyed by "METHOD_endpoint".
func LoadPayloadPools(steps []configyml.Step, apiDir string) map[string]*PayloadPool {
	pools := map[string]*PayloadPool{}

	for _, step := range steps {
		method := strings.ToLower(step.Method)
		// Normalize endpoint to directory name: /persons → persons, /persons/{id} → persons_{id}
		dirName := strings.Trim(strings.ReplaceAll(step.Endpoint, "/", "_"), "_")
		dir := filepath.Join(apiDir, dirName, method)

		key := method + "_" + dirName
		if _, exists := pools[key]; exists {
			continue
		}

		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}

		pool := &PayloadPool{
			Endpoint: step.Endpoint,
			Method:   method,
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			pool.Payloads = append(pool.Payloads, string(data))
		}
		if len(pool.Payloads) > 0 {
			pools[key] = pool
		}
	}

	return pools
}

// GeneratePayloadPoolJS emits a JavaScript const array containing all
// pre-generated payloads for a pool.
func GeneratePayloadPoolJS(pool *PayloadPool) string {
	var b strings.Builder
	method := strings.ToUpper(pool.Method)
	dirName := strings.Trim(strings.ReplaceAll(pool.Endpoint, "/", "_"), "_")
	varName := fmt.Sprintf("payloads_%s_%s", method, dirName)

	fmt.Fprintf(&b, "const %s = [\n", varName)
	for _, p := range pool.Payloads {
		// Escape for JS string
		escaped, _ := json.Marshal(p)
		fmt.Fprintf(&b, "  %s,\n", string(escaped))
	}
	b.WriteString("];\n")
	fmt.Fprintf(&b, "function randomPayload_%s_%s() {\n", method, dirName)
	fmt.Fprintf(&b, "  return %s[Math.floor(Math.random() * %s.length)];\n", varName, varName)
	b.WriteString("}\n")

	return b.String()
}

// GeneratePayloadCode generates the JS code that builds a request body for a
// step according to its payload strategy. Returns a JS expression that
// evaluates to a string (the JSON body).
func GeneratePayloadCode(step configyml.Step, flowCtxVar string) string {
	switch step.PayloadStrategy {
	case "fixed":
		return generateFixedPayload(step)
	case "template":
		return generateTemplatePayload(step)
	case "extracted":
		return generateExtractedPayload(step)
	case "template-json":
		return generateTemplateJSONPayload(step)
	default: // "random" or empty
		return generateRandomPayload(step)
	}
}

func generateFixedPayload(step configyml.Step) string {
	// Read the file and embed it as a JS string
	data, err := os.ReadFile(step.PayloadFile)
	if err != nil {
		return "'{}'"
	}
	escaped, _ := json.Marshal(string(data))
	return string(escaped)
}

func generateTemplatePayload(step configyml.Step) string {
	tmpl := strings.TrimSpace(step.PayloadTemplate)
	// Convert Go template to JS template literal
	return JSNameTemplate(tmpl)
}

func generateExtractedPayload(step configyml.Step) string {
	if step.FromStep != "" {
		return fmt.Sprintf("JSON.stringify(%s['%s._body'] || {})", "ctx", step.FromStep)
	}
	return "'{}'"
}

func generateTemplateJSONPayload(step configyml.Step) string {
	var b strings.Builder

	// Load base: from file or random
	var baseExpr string
	if step.PayloadFile != "" {
		data, err := os.ReadFile(step.PayloadFile)
		if err != nil {
			baseExpr = "'{}'"
		} else {
			escaped, _ := json.Marshal(string(data))
			baseExpr = string(escaped)
		}
	} else {
		// Use random payload from pool
		method := strings.ToLower(step.Method)
		dirName := strings.Trim(strings.ReplaceAll(step.Endpoint, "/", "_"), "_")
		baseExpr = fmt.Sprintf("randomPayload_%s_%s()", strings.ToUpper(method), dirName)
	}

	b.WriteString("(function() {\n")
	fmt.Fprintf(&b, "  const base = JSON.parse(%s);\n", baseExpr)

	for path, tmplVal := range step.PayloadOverrides {
		jsTmpl := JSNameTemplate(tmplVal)
		varName := sanitizeJSVarName(path)
		fmt.Fprintf(&b, "  const val_%s = %s;\n", varName, jsTmpl)
		fmt.Fprintf(&b, "  try { setNestedJSON(base, '%s', JSON.parse(val_%s)); } catch(e) { setNestedJSON(base, '%s', val_%s); }\n",
			path, varName, path, varName)
	}

	b.WriteString("  return JSON.stringify(base);\n")
	b.WriteString("})()")

	return b.String()
}

func generateRandomPayload(step configyml.Step) string {
	method := strings.ToLower(step.Method)
	dirName := strings.Trim(strings.ReplaceAll(step.Endpoint, "/", "_"), "_")
	return fmt.Sprintf("randomPayload_%s_%s()", strings.ToUpper(method), dirName)
}

// sanitizeJSVarName converts a dot-path to a valid JS variable suffix.
func sanitizeJSVarName(s string) string {
	return strings.ReplaceAll(s, ".", "_")
}
