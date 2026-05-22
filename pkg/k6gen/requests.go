package k6gen

import (
	"fmt"
	"strings"

	"tya/pkg/configyml"
)

// GenerateStepCode generates the full k6 JavaScript code for a single step
// within a flow. The code executes an HTTP request, validates the response,
// extracts values, and records metrics.
func GenerateStepCode(step configyml.Step, flowName string, auth configyml.AuthProfile) string {
	var b strings.Builder
	stepID := step.ID
	if stepID == "" {
		stepID = strings.ToLower(step.Method) + "_" + strings.ReplaceAll(step.Endpoint, "/", "_")
	}

	method := strings.ToUpper(step.Method)
	fmt.Fprintf(&b, "    group('%s', function() {\n", stepID)

	// Build endpoint URL
	fmt.Fprintf(&b, "      let url = baseURL + %s;\n", goTemplateToJSEval(step.Endpoint))
	b.WriteString("      url = renderTemplate(url, ctx);\n")

	// Build headers
	b.WriteString("      const headers = { 'Content-Type': 'application/json' };\n")

	// Inject auth
	b.WriteString("      " + GenerateAuthInject(auth) + "\n")

	// Build payload
	payload := GeneratePayloadCode(step, "ctx")
	if method == "GET" || method == "DELETE" {
		// GET/DELETE typically have no body
		if step.PayloadStrategy != "" && step.PayloadStrategy != "random" {
			fmt.Fprintf(&b, "      const body = %s;\n", payload)
		}
	} else {
		fmt.Fprintf(&b, "      const body = %s;\n", payload)
	}

	// Execute request — also capture reqBody string for request-source extractors.
	b.WriteString("      const startTime = Date.now();\n")
	k6Method := k6HTTPMethod(method)
	switch method {
	case "GET":
		fmt.Fprintf(&b, "      const res = http.%s(url, { headers: headers });\n", k6Method)
		b.WriteString("      const reqBody = null;\n")
	case "DELETE":
		fmt.Fprintf(&b, "      const res = http.%s(url, null, { headers: headers });\n", k6Method)
		b.WriteString("      const reqBody = null;\n")
	default:
		b.WriteString("      const reqBody = body;\n")
		fmt.Fprintf(&b, "      const res = http.%s(url, body, { headers: headers });\n", k6Method)
	}
	b.WriteString("      const latency = Date.now() - startTime;\n")
	b.WriteString("      tyaStepLatency.add(latency);\n")

	// Check response
	b.WriteString("      const ok = check(res, {\n")
	fmt.Fprintf(&b, "        '%s %s: status < 400': (r) => r.status < 400,\n", method, stepID)
	b.WriteString("      });\n")
	b.WriteString("      if (!ok) { tyaErrors.add(1); }\n")

	// Extract values
	extractCode := GenerateExtractWithGlobalContext(step.Extract, "res", flowName)
	if extractCode != "" {
		b.WriteString(extractCode)
	}

	// Store response body for potential extraction by later steps
	b.WriteString("      ctx['" + stepID + "._body'] = res.body;\n")

	b.WriteString("    });\n")

	return b.String()
}

// goTemplateToJSEval converts a Go template string like
// "/persons/{{ .pid }}" to a JS expression like "`/persons/${ctx['pid']}`".
func goTemplateToJSEval(s string) string {
	// Check if it contains any Go template syntax
	if !strings.Contains(s, "{{") {
		return JsString(s)
	}
	return JSNameTemplate(s)
}

// GenerateIterateStepCode generates the k6 code for an iterate flow.
// Instead of running steps in a VU loop, it iterates over a list.
func GenerateIterateStepCode(steps []configyml.Step, flowName, itemVar, listSource string, auth configyml.AuthProfile) string {
	var b strings.Builder

	// The list is passed via setup data or read from a global context
	fmt.Fprintf(&b, "    const items = data['%s'] || [];\n", listSource)
	b.WriteString("    for (let __idx = 0; __idx < items.length; __idx++) {\n")
	fmt.Fprintf(&b, "      ctx['%s'] = items[__idx];\n", itemVar)
	b.WriteString("      ctx['__item_index__'] = __idx;\n")

	for _, step := range steps {
		b.WriteString(GenerateStepCode(step, flowName, auth))
	}

	b.WriteString("    }\n")
	return b.String()
}

// GenerateScenarioConfig generates the k6 options.scenarios block for a flow.
func GenerateScenarioConfig(flow configyml.Flow) string {
	var b strings.Builder

	duration := flow.Duration
	if duration == "" {
		duration = "30s"
	}
	rps := flow.RequestsPerSecond

	// alone flows with no rps and no duration configured → single-pass (1 iteration).
	aloneNoConfig := strings.EqualFold(flow.Type, "alone") && flow.Duration == "" && rps <= 0

	if aloneNoConfig {
		// Lone one-shot flows: single sequential pass, no rate target.
		b.WriteString("    scenario: {\n")
		b.WriteString("      executor: 'shared-iterations',\n")
		b.WriteString("      vus: 1,\n")
		b.WriteString("      iterations: 1,\n")
		fmt.Fprintf(&b, "      maxDuration: '%s',\n", duration)
		b.WriteString("    },\n")
		return b.String()
	}

	if flow.Type == "iterate" {
		// Iterate flows: constant-arrival-rate executor.
		// RPS = HTTP calls/s. Arrival rate for the executor = iterations/s = rps / nSteps.
		nSteps := float64(len(flow.Steps))
		if nSteps < 1 {
			nSteps = 1
		}
		iterRPS := rps / nSteps
		if iterRPS < 1 {
			iterRPS = 1
		}
		b.WriteString("    scenario: {\n")
		b.WriteString("      executor: 'constant-arrival-rate',\n")
		fmt.Fprintf(&b, "      rate: %d,\n", int(iterRPS))
		b.WriteString("      timeUnit: '1s',\n")
		b.WriteString("      preAllocatedVUs: 10,\n")
		b.WriteString("      maxVUs: 200,\n")
		fmt.Fprintf(&b, "      duration: '%s',\n", duration)
		b.WriteString("    },\n")
		return b.String()
	}

	if rps <= 0 {
		rps = 1
	}

	rampUp := flow.RampUp
	if rampUp != nil {
		rampUp = rampCfg(rampUp)
	}

	b.WriteString("    scenario: {\n")
	b.WriteString("      executor: 'ramping-arrival-rate',\n")
	fmt.Fprintf(&b, "      startRate: %d,\n", int(rps))
	b.WriteString("      timeUnit: '1s',\n")
	b.WriteString("      preAllocatedVUs: 10,\n")
	b.WriteString("      maxVUs: 200,\n")
	b.WriteString("      stages: [\n")

	if rampUp != nil {
		// Generate ramp-up stages
		initial := rampUp.InitialRPS
		factor := rampUp.Factor
		stepWin := rampUp.StepWindow
		if stepWin == "" {
			stepWin = "2s"
		}
		stabWindows := rampUp.StabilityWindows
		if stabWindows <= 0 {
			stabWindows = 3
		}

		current := initial
		for current < rps {
			next := current * factor
			if next >= rps {
				next = rps
			}
			fmt.Fprintf(&b, "        { target: %d, duration: '%s' },\n", int(next), stepWin)
			current = next
		}
		// Plateau at target for stability windows
		fmt.Fprintf(&b, "        { target: %d, duration: '%ds' },\n", int(rps), stabWindows*2)
	}

	// Analysis window
	fmt.Fprintf(&b, "        { target: %d, duration: '%s' },\n", int(rps), duration)

	// Drain
	b.WriteString("        { target: 0, duration: '5s' },\n")
	b.WriteString("      ],\n")
	b.WriteString("    },\n")

	return b.String()
}

func rampCfg(r *configyml.RampUp) *configyml.RampUp {
	if r == nil {
		r = &configyml.RampUp{}
	}
	return r
}

// k6HTTPMethod maps HTTP methods to k6's http module method names.
// In k6, DELETE is http.del(), not http.delete().
func k6HTTPMethod(method string) string {
	switch strings.ToUpper(method) {
	case "DELETE":
		return "del"
	default:
		return strings.ToLower(method)
	}
}
