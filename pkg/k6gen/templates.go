// Package k6gen generates k6 JavaScript load-test scripts from TYA
// config-run.yml configurations. The generated scripts handle authentication,
// payload generation, data extraction between steps, and flow dependencies.
package k6gen

import (
	"fmt"
	"strings"

	"github.com/vhPedroGitHub/tya/pkg/configyml"
)

// JSHelperFuncs returns the JavaScript helper functions embedded in every
// generated k6 script. These provide the same capabilities as TYA's Go-side
// template functions (uuid, randomDigits, navigate, renderTemplate, etc.).
func JSHelperFuncs() string {
	return `// ─── TYA k6 helpers ────────────────────────────────────────────────────────

function uuidv4() {
  const b = new Uint8Array(16);
  for (let i = 0; i < 16; i++) b[i] = Math.floor(Math.random() * 256);
  b[6] = (b[6] & 0x0f) | 0x40;
  b[8] = (b[8] & 0x3f) | 0x80;
  const h = Array.from(b).map(x => x.toString(16).padStart(2, '0')).join('');
  return h.slice(0,8) + '-' + h.slice(8,12) + '-' + h.slice(12,16) + '-' + h.slice(16,20) + '-' + h.slice(20);
}

function randomDigits(n) {
  let s = '';
  for (let i = 0; i < n; i++) s += Math.floor(Math.random() * 10);
  return s;
}

function randomInt() {
  return Math.floor(Math.random() * 2147483647).toString();
}

function timestamp() {
  return Math.floor(Date.now() / 1000).toString();
}

function timestampMs() {
  return Date.now().toString();
}

function navigate(obj, path) {
  if (!path || !obj) return null;
  const parts = path.split('.');
  let current = obj;
  for (const part of parts) {
    if (part === 'response' || part === 'body') continue;
    const arrMatch = part.match(/^(\w+)\[(\d+)\]$/);
    if (arrMatch) {
      current = current[arrMatch[1]];
      if (Array.isArray(current)) {
        current = current[parseInt(arrMatch[2])];
      } else {
        return null;
      }
    } else {
      current = current[part];
    }
    if (current === undefined || current === null) return null;
  }
  return current;
}

function setNestedJSON(obj, path, value) {
  const parts = path.split('.');
  let current = obj;
  for (let i = 0; i < parts.length - 1; i++) {
    if (!current[parts[i]] || typeof current[parts[i]] !== 'object') {
      current[parts[i]] = {};
    }
    current = current[parts[i]];
  }
  current[parts[parts.length - 1]] = value;
}

function renderTemplate(tmpl, ctx) {
  return tmpl.replace(/\{\{\s*\.(\w+(?:\.\w+)*)\s*\}\}/g, (_, key) => {
    const val = navigate(ctx, key);
    return val !== null && val !== undefined ? val : '';
  }).replace(/\{\{\s*(uuid)\s*\}\}/g, () => uuidv4())
    .replace(/\{\{\s*randomDigits\s+(\d+)\s*\}\}/g, (_, n) => randomDigits(parseInt(n)))
    .replace(/\{\{\s*randomInt\s*\}\}/g, () => randomInt())
    .replace(/\{\{\s*timestamp\s*\}\}/g, () => timestamp())
    .replace(/\{\{\s*timestampMs\s*\}\}/g, () => timestampMs());
}

function expandEnv(str, env) {
  return str.replace(/\$\{(\w+)\}/g, (_, key) => env[key] || '');
}

// ─── end TYA helpers ─────────────────────────────────────────────────────
`
}

// JSMetricsHeader returns the k6 import and custom metric declarations.
func JSMetricsHeader() string {
	return `import http from 'k6/http';
import { check, group, sleep } from 'k6';
import { Counter, Trend } from 'k6/metrics';

const tyaErrors = new Counter('tya_errors');
const tyaStepLatency = new Trend('tya_step_latency', true);
`
}

// EnvVarsFromAuth extracts ${ENV_VAR} references from an auth profile and
// returns the k6 env var names needed.
func EnvVarsFromAuth(auth configyml.AuthProfile) []string {
	vars := []string{}
	candidates := []string{
		auth.ClientSecret, auth.Username, auth.Password, auth.Value,
		auth.Payload, auth.RefreshPayload,
	}
	for _, c := range candidates {
		vars = append(vars, extractEnvVars(c)...)
	}
	// Deduplicate
	seen := map[string]bool{}
	var unique []string
	for _, v := range vars {
		if !seen[v] {
			seen[v] = true
			unique = append(unique, v)
		}
	}
	return unique
}

// EnvVarsFromSteps extracts ${ENV_VAR} references from step templates.
func EnvVarsFromSteps(steps []configyml.Step) []string {
	vars := []string{}
	for _, s := range steps {
		vars = append(vars, extractEnvVars(s.Endpoint)...)
		vars = append(vars, extractEnvVars(s.PayloadTemplate)...)
		vars = append(vars, extractEnvVars(s.PayloadFile)...)
		for _, o := range s.PayloadOverrides {
			vars = append(vars, extractEnvVars(o)...)
		}
	}
	seen := map[string]bool{}
	var unique []string
	for _, v := range vars {
		if !seen[v] {
			seen[v] = true
			unique = append(unique, v)
		}
	}
	return unique
}

// extractEnvVars finds ${VAR} references in a string.
func extractEnvVars(s string) []string {
	var vars []string
	for {
		idx := strings.Index(s, "${")
		if idx < 0 {
			break
		}
		end := strings.Index(s[idx:], "}")
		if end < 0 {
			break
		}
		vars = append(vars, s[idx+2:idx+end])
		s = s[idx+end+1:]
	}
	return vars
}

// JSNameString returns a JavaScript string literal, escaping as needed.
func JSNameString(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "'", "\\'")
	s = strings.ReplaceAll(s, "\n", "\\n")
	s = strings.ReplaceAll(s, "\r", "\\r")
	return "'" + s + "'"
}

// JSNameTemplate converts TYA Go template syntax to JavaScript template literal:
// {{ .key }} → ${ctx.key}
// {{ uuid }} → ${uuidv4()}
// {{ randomDigits N }} → ${randomDigits(N)}
func JSNameTemplate(tmpl string) string {
	// {{ .key }} or {{ .key.subkey }} → ${ctx['key']} or ${navigate(ctx, 'key.subkey')}
	tmpl = goTemplateToJS(tmpl)
	// Wrap in backticks for JS template literal
	return "`" + tmpl + "`"
}

// goTemplateToJS converts Go template expressions to JS expressions.
func goTemplateToJS(s string) string {
	var result strings.Builder
	i := 0
	for i < len(s) {
		if i+1 < len(s) && s[i:i+2] == "{{" {
			end := strings.Index(s[i:], "}}")
			if end >= 0 {
				expr := strings.TrimSpace(s[i+2 : i+end])
				result.WriteString("${")
				result.WriteString(templateExprToJS(expr))
				result.WriteString("}")
				i = i + end + 2
				continue
			}
		}
		result.WriteByte(s[i])
		i++
	}
	return result.String()
}

// templateExprToJS converts a single Go template expression to JS.
func templateExprToJS(expr string) string {
	// {{ .key }} → ctx['key']
	if strings.HasPrefix(expr, ".") {
		key := strings.TrimPrefix(expr, ".")
		// Handle nested: .key.subkey → navigate(ctx, 'key.subkey')
		if strings.Contains(key, ".") {
			return fmt.Sprintf("navigate(ctx, '%s')", key)
		}
		return fmt.Sprintf("ctx['%s']", key)
	}
	// {{ uuid }} → uuidv4()
	if expr == "uuid" {
		return "uuidv4()"
	}
	// {{ randomDigits N }} → randomDigits(N)
	if strings.HasPrefix(expr, "randomDigits ") {
		n := strings.TrimPrefix(expr, "randomDigits ")
		return fmt.Sprintf("randomDigits(%s)", n)
	}
	if expr == "randomInt" {
		return "randomInt()"
	}
	if expr == "timestamp" {
		return "timestamp()"
	}
	if expr == "timestampMs" {
		return "timestampMs()"
	}
	// globalGet "flow" "key" → ctx['__global__']['flow']['key']
	if strings.HasPrefix(expr, "globalGet ") {
		parts := parseQuotedArgs(expr[len("globalGet "):])
		if len(parts) == 2 {
			return fmt.Sprintf("navigate(ctx['__global__'], '%s.%s')", parts[0], parts[1])
		}
	}
	// Fallback: return as-is
	return expr
}

// parseQuotedArgs parses 'arg1' 'arg2' from a string.
func parseQuotedArgs(s string) []string {
	var args []string
	s = strings.TrimSpace(s)
	for len(s) > 0 {
		if s[0] == '\'' || s[0] == '"' {
			quote := s[0]
			end := strings.IndexByte(s[1:], quote)
			if end >= 0 {
				args = append(args, s[1:1+end])
				s = strings.TrimSpace(s[2+end:])
				continue
			}
		}
		// Unquoted word
		sp := strings.IndexByte(s, ' ')
		if sp < 0 {
			args = append(args, s)
			break
		}
		args = append(args, s[:sp])
		s = strings.TrimSpace(s[sp+1:])
	}
	return args
}
