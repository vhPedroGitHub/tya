package k6gen

import (
	"fmt"
	"strings"

	"github.com/vhPedroGitHub/tya/pkg/configyml"
)

// GenerateAuthSetup generates the k6 setup() function body that acquires
// authentication tokens. Returns the JS code for the setup function.
func GenerateAuthSetup(auth configyml.AuthProfile, baseURL string) string {
	var b strings.Builder

	switch auth.Type {
	case "custom_login":
		b.WriteString(generateCustomLoginAuth(auth, baseURL))
	case "oauth2_password":
		b.WriteString(generateOAuth2PasswordAuth(auth))
	case "oauth2_client_credentials":
		b.WriteString(generateOAuth2ClientCredentialsAuth(auth))
	case "api_key":
		b.WriteString(generateAPIKeyAuth(auth))
	case "basic":
		b.WriteString(generateBasicAuth(auth))
	default:
		b.WriteString("    return {};\n")
	}

	return b.String()
}

func generateCustomLoginAuth(auth configyml.AuthProfile, baseURL string) string {
	var b strings.Builder

	loginURL := fmt.Sprintf("`${baseURL}%s`", auth.LoginEndpoint)
	payload := strings.TrimSpace(auth.Payload)
	// Convert ${VAR} to k6 __ENV references using template literal interpolation
	payloadJS := envVarsToK6TemplateLiteral(payload)

	fmt.Fprintf(&b, "    const loginRes = http.request(%s, %s, %s, {\n",
		JsString(strings.ToUpper(auth.Method)),
		loginURL,
		payloadJS)
	b.WriteString("      headers: { 'Content-Type': 'application/json' },\n")
	b.WriteString("    });\n")
	b.WriteString("    if (loginRes.status !== 200) {\n")
	b.WriteString("      throw new Error('Auth login failed: ' + loginRes.status + ' ' + loginRes.body);\n")
	b.WriteString("    }\n")
	b.WriteString("    const loginBody = loginRes.json();\n")

	// Extract tokens
	b.WriteString("    const auth = {\n")
	for key, path := range auth.ExtractToken {
		// path is like "response.body.access_token"
		jsPath := strings.ReplaceAll(path, "response.body.", "")
		fmt.Fprintf(&b, "      %s: navigate(loginBody, '%s'),\n", key, jsPath)
	}
	b.WriteString("    };\n")

	// Store refresh info for potential refresh
	if auth.RefreshEndpoint != "" {
		fmt.Fprintf(&b, "    auth.__refreshEndpoint = '%s';\n", auth.RefreshEndpoint)
		fmt.Fprintf(&b, "    auth.__refreshMethod = '%s';\n", strings.ToUpper(auth.RefreshMethod))
		if auth.RefreshPayload != "" {
			fmt.Fprintf(&b, "    auth.__refreshPayload = '%s';\n", strings.TrimSpace(auth.RefreshPayload))
		}
		b.WriteString("    auth.__refreshExtract = {\n")
		for key, path := range auth.RefreshExtract {
			jsPath := strings.ReplaceAll(path, "response.body.", "")
			fmt.Fprintf(&b, "      %s: '%s',\n", key, jsPath)
		}
		b.WriteString("    };\n")
	}

	b.WriteString("    return { auth: auth };\n")
	return b.String()
}

func generateOAuth2PasswordAuth(auth configyml.AuthProfile) string {
	var b strings.Builder

	fmt.Fprintf(&b, "    const tokenRes = http.post('%s', {\n", auth.TokenURL)
	b.WriteString("      grant_type: 'password',\n")
	fmt.Fprintf(&b, "      client_id: '%s',\n", auth.ClientID)
	fmt.Fprintf(&b, "      client_secret: %s,\n", envVarOrFallback(auth.ClientSecret))
	fmt.Fprintf(&b, "      username: %s,\n", envVarOrFallback(auth.Username))
	fmt.Fprintf(&b, "      password: %s,\n", envVarOrFallback(auth.Password))
	if len(auth.Scopes) > 0 {
		fmt.Fprintf(&b, "      scope: '%s',\n", strings.Join(auth.Scopes, " "))
	}
	b.WriteString("    });\n")
	b.WriteString("    if (tokenRes.status !== 200) {\n")
	b.WriteString("      throw new Error('OAuth2 token request failed: ' + tokenRes.status);\n")
	b.WriteString("    }\n")
	b.WriteString("    const tokenBody = tokenRes.json();\n")
	b.WriteString("    return { auth: {\n")
	b.WriteString("      access_token: tokenBody.access_token,\n")
	b.WriteString("      refresh_token: tokenBody.refresh_token,\n")
	b.WriteString("      expires_in: tokenBody.expires_in,\n")
	b.WriteString("    } };\n")

	return b.String()
}

func generateOAuth2ClientCredentialsAuth(auth configyml.AuthProfile) string {
	var b strings.Builder

	fmt.Fprintf(&b, "    const tokenRes = http.post('%s', {\n", auth.TokenURL)
	b.WriteString("      grant_type: 'client_credentials',\n")
	fmt.Fprintf(&b, "      client_id: '%s',\n", auth.ClientID)
	fmt.Fprintf(&b, "      client_secret: %s,\n", envVarOrFallback(auth.ClientSecret))
	if len(auth.Scopes) > 0 {
		fmt.Fprintf(&b, "      scope: '%s',\n", strings.Join(auth.Scopes, " "))
	}
	b.WriteString("    });\n")
	b.WriteString("    if (tokenRes.status !== 200) {\n")
	b.WriteString("      throw new Error('OAuth2 client_credentials failed: ' + tokenRes.status);\n")
	b.WriteString("    }\n")
	b.WriteString("    const tokenBody = tokenRes.json();\n")
	b.WriteString("    return { auth: {\n")
	b.WriteString("      access_token: tokenBody.access_token,\n")
	b.WriteString("      expires_in: tokenBody.expires_in,\n")
	b.WriteString("    } };\n")

	return b.String()
}

func generateAPIKeyAuth(auth configyml.AuthProfile) string {
	return fmt.Sprintf("    return { auth: { api_key: %s } };\n",
		envVarOrFallback(auth.Value))
}

func generateBasicAuth(auth configyml.AuthProfile) string {
	return fmt.Sprintf("    return { auth: { username: %s, password: %s } };\n",
		envVarOrFallback(auth.Username),
		envVarOrFallback(auth.Password))
}

// GenerateAuthInject generates the JS code that injects auth into a request.
func GenerateAuthInject(auth configyml.AuthProfile) string {
	switch auth.Type {
	case "api_key":
		if auth.InjectAs == "query" {
			param := auth.QueryParam
			if param == "" {
				param = "api_key"
			}
			return fmt.Sprintf("url += (url.includes('?') ? '&' : '?') + '%s=' + encodeURIComponent(auth.api_key);", param)
		}
		name := auth.HeaderName
		if name == "" {
			name = "X-API-Key"
		}
		return fmt.Sprintf("headers['%s'] = auth.api_key;", name)
	case "basic":
		return "headers['Authorization'] = 'Basic ' + encoding.b64Encode(auth.username + ':' + auth.password);"
	default:
		// custom_login, oauth2 — Bearer token
		return "if (auth.access_token) { headers['Authorization'] = 'Bearer ' + auth.access_token; }"
	}
}

// GenerateAuthSetupWithGlobal generates the k6 setup() function body that
// acquires authentication tokens and also returns the globalState so it is
// available to the VU default function via data.globalState.
func GenerateAuthSetupWithGlobal(auth configyml.AuthProfile, baseURL string) string {
	// Delegate to the inner helpers but capture result in a variable, then
	// return with globalState merged in.
	var b strings.Builder

	switch auth.Type {
	case "custom_login":
		b.WriteString(generateCustomLoginAuthWithGlobal(auth, baseURL))
	case "oauth2_password":
		b.WriteString(generateOAuth2PasswordAuthWithGlobal(auth))
	case "oauth2_client_credentials":
		b.WriteString(generateOAuth2ClientCredentialsAuthWithGlobal(auth))
	case "api_key":
		fmt.Fprintf(&b, "  return { auth: { api_key: %s }, globalState: globalState };\n",
			envVarOrFallback(auth.Value))
	case "basic":
		fmt.Fprintf(&b, "  return { auth: { username: %s, password: %s }, globalState: globalState };\n",
			envVarOrFallback(auth.Username),
			envVarOrFallback(auth.Password))
	default:
		b.WriteString("  return { globalState: globalState };\n")
	}

	return b.String()
}

func generateCustomLoginAuthWithGlobal(auth configyml.AuthProfile, baseURL string) string {
	var b strings.Builder

	loginURL := fmt.Sprintf("`${baseURL}%s`", auth.LoginEndpoint)
	payload := strings.TrimSpace(auth.Payload)
	payloadJS := envVarsToK6TemplateLiteral(payload)

	fmt.Fprintf(&b, "  const loginRes = http.request(%s, %s, %s, {\n",
		JsString(strings.ToUpper(auth.Method)),
		loginURL,
		payloadJS)
	b.WriteString("    headers: { 'Content-Type': 'application/json' },\n")
	b.WriteString("  });\n")
	b.WriteString("  if (loginRes.status !== 200) {\n")
	b.WriteString("    throw new Error('Auth login failed: ' + loginRes.status + ' ' + loginRes.body);\n")
	b.WriteString("  }\n")
	b.WriteString("  const loginBody = loginRes.json();\n")
	b.WriteString("  const auth = {\n")
	for key, path := range auth.ExtractToken {
		jsPath := strings.ReplaceAll(path, "response.body.", "")
		fmt.Fprintf(&b, "    %s: navigate(loginBody, '%s'),\n", key, jsPath)
	}
	b.WriteString("  };\n")
	if auth.RefreshEndpoint != "" {
		fmt.Fprintf(&b, "  auth.__refreshEndpoint = '%s';\n", auth.RefreshEndpoint)
		fmt.Fprintf(&b, "  auth.__refreshMethod = '%s';\n", strings.ToUpper(auth.RefreshMethod))
		if auth.RefreshPayload != "" {
			fmt.Fprintf(&b, "  auth.__refreshPayload = '%s';\n", strings.TrimSpace(auth.RefreshPayload))
		}
		b.WriteString("  auth.__refreshExtract = {\n")
		for key, path := range auth.RefreshExtract {
			jsPath := strings.ReplaceAll(path, "response.body.", "")
			fmt.Fprintf(&b, "    %s: '%s',\n", key, jsPath)
		}
		b.WriteString("  };\n")
	}
	b.WriteString("  return { auth: auth, globalState: globalState };\n")
	return b.String()
}

func generateOAuth2PasswordAuthWithGlobal(auth configyml.AuthProfile) string {
	var b strings.Builder
	fmt.Fprintf(&b, "  const tokenRes = http.post('%s', {\n", auth.TokenURL)
	b.WriteString("    grant_type: 'password',\n")
	fmt.Fprintf(&b, "    client_id: '%s',\n", auth.ClientID)
	fmt.Fprintf(&b, "    client_secret: %s,\n", envVarOrFallback(auth.ClientSecret))
	fmt.Fprintf(&b, "    username: %s,\n", envVarOrFallback(auth.Username))
	fmt.Fprintf(&b, "    password: %s,\n", envVarOrFallback(auth.Password))
	if len(auth.Scopes) > 0 {
		fmt.Fprintf(&b, "    scope: '%s',\n", strings.Join(auth.Scopes, " "))
	}
	b.WriteString("  });\n")
	b.WriteString("  if (tokenRes.status !== 200) {\n")
	b.WriteString("    throw new Error('OAuth2 token request failed: ' + tokenRes.status);\n")
	b.WriteString("  }\n")
	b.WriteString("  const tokenBody = tokenRes.json();\n")
	b.WriteString("  return { auth: {\n")
	b.WriteString("    access_token: tokenBody.access_token,\n")
	b.WriteString("    refresh_token: tokenBody.refresh_token,\n")
	b.WriteString("    expires_in: tokenBody.expires_in,\n")
	b.WriteString("  }, globalState: globalState };\n")
	return b.String()
}

func generateOAuth2ClientCredentialsAuthWithGlobal(auth configyml.AuthProfile) string {
	var b strings.Builder
	fmt.Fprintf(&b, "  const tokenRes = http.post('%s', {\n", auth.TokenURL)
	b.WriteString("    grant_type: 'client_credentials',\n")
	fmt.Fprintf(&b, "    client_id: '%s',\n", auth.ClientID)
	fmt.Fprintf(&b, "    client_secret: %s,\n", envVarOrFallback(auth.ClientSecret))
	if len(auth.Scopes) > 0 {
		fmt.Fprintf(&b, "    scope: '%s',\n", strings.Join(auth.Scopes, " "))
	}
	b.WriteString("  });\n")
	b.WriteString("  if (tokenRes.status !== 200) {\n")
	b.WriteString("    throw new Error('OAuth2 client_credentials failed: ' + tokenRes.status);\n")
	b.WriteString("  }\n")
	b.WriteString("  const tokenBody = tokenRes.json();\n")
	b.WriteString("  return { auth: {\n")
	b.WriteString("    access_token: tokenBody.access_token,\n")
	b.WriteString("    expires_in: tokenBody.expires_in,\n")
	b.WriteString("  }, globalState: globalState };\n")
	return b.String()
}

// GenerateAuthImport returns extra k6 imports needed for auth types.
func GenerateAuthImport(auth configyml.AuthProfile) string {
	if auth.Type == "basic" {
		return "import encoding from 'k6/encoding';\n"
	}
	return ""
}

// envVarsToK6TemplateLiteral converts a string with ${VAR} references to a
// JavaScript template literal using __ENV for variable substitution.
func envVarsToK6TemplateLiteral(s string) string {
	var result strings.Builder
	result.WriteString("`")
	for {
		idx := strings.Index(s, "${")
		if idx < 0 {
			result.WriteString(s)
			break
		}
		result.WriteString(s[:idx])
		end := strings.Index(s[idx:], "}")
		if end < 0 {
			result.WriteString(s)
			break
		}
		varName := s[idx+2 : idx+end]
		result.WriteString("${__ENV." + varName + " || ''}")
		s = s[idx+end+1:]
	}
	result.WriteString("`")
	return result.String()
}

// envVarName extracts a clean env var name from a ${VAR} reference.
func envVarName(s string) string {
	s = strings.TrimPrefix(s, "${")
	s = strings.TrimSuffix(s, "}")
	return strings.TrimSpace(s)
}

// envVarOrFallback returns a JavaScript expression for an auth value.
//   - Pure env var: "Bearer ${TOKEN}" → template literal `Bearer ${__ENV.TOKEN || ”}`
//   - Simple env var: "${TOKEN}"       → __ENV.TOKEN || '${TOKEN}'
//   - No env var:    "static-key"      → __ENV. || 'static-key' (plain literal)
//
// For mixed literal+env var strings, a template literal is always used to
// produce valid JS.
func envVarOrFallback(s string) string {
	if strings.Contains(s, "${") {
		return envVarsToK6TemplateLiteral(s)
	}
	return fmt.Sprintf("__ENV.%s || '%s'", envVarName(s), s)
}

// JsString wraps s in single quotes for JavaScript.
func JsString(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "'", "\\'")
	s = strings.ReplaceAll(s, "\n", "\\n")
	s = strings.ReplaceAll(s, "\r", "\\r")
	return "'" + s + "'"
}
