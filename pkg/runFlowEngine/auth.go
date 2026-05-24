package runflowengine

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/vhPedroGitHub/tya/pkg/configyml"
	"go.uber.org/zap"
)

// acquireToken performs the initial login for an auth profile and populates
// the flow context with access_token, refresh_token, etc.
func acquireToken(log *zap.Logger, auth configyml.AuthProfile, baseURL string, fCtx FlowContext) {
	switch auth.Type {
	case "custom_login":
		payload := expandEnv(auth.Payload)
		req, err := http.NewRequest(strings.ToUpper(auth.Method), baseURL+auth.LoginEndpoint, strings.NewReader(payload))
		if err != nil {
			log.Warn("auth: failed to build login request", zap.Error(err))
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			log.Warn("auth: login request failed", zap.Error(err))
			return
		}
		defer resp.Body.Close() //nolint:errcheck
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode >= 400 {
			log.Warn("auth: login returned error",
				zap.Int("status", resp.StatusCode),
				zap.ByteString("body", body),
			)
			return
		}
		var parsed any
		if err := json.Unmarshal(body, &parsed); err == nil {
			for key, path := range auth.ExtractToken {
				parts := strings.Split(path, ".")
				if val := navigate(parsed, parts); val != nil {
					fCtx[key] = val
				}
			}
		}
		log.Info("auth: login successful", zap.String("profile", auth.Name))

	case "oauth2_password":
		form := fmt.Sprintf(
			"grant_type=password&client_id=%s&client_secret=%s&username=%s&password=%s&scope=%s",
			expandEnv(auth.ClientID),
			expandEnv(auth.ClientSecret),
			expandEnv(auth.Username),
			expandEnv(auth.Password),
			strings.Join(auth.Scopes, " "),
		)
		req, err := http.NewRequest("POST", auth.TokenURL, strings.NewReader(form))
		if err != nil {
			log.Warn("auth: oauth2 request build failed", zap.Error(err))
			return
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			log.Warn("auth: oauth2 request failed", zap.Error(err))
			return
		}
		defer resp.Body.Close() //nolint:errcheck
		body, _ := io.ReadAll(resp.Body)
		var parsed map[string]any
		if err := json.Unmarshal(body, &parsed); err == nil {
			for k, v := range parsed {
				fCtx[k] = v
			}
		}
		log.Info("auth: oauth2 token acquired", zap.String("profile", auth.Name))
	}
}

// injectAuth sets the appropriate authentication header/param on req.
func injectAuth(req *http.Request, auth configyml.AuthProfile, fCtx FlowContext) {
	switch auth.Type {
	case "api_key":
		val := expandEnv(auth.Value)
		if auth.InjectAs == "query" {
			q := req.URL.Query()
			q.Set(auth.QueryParam, val)
			req.URL.RawQuery = q.Encode()
		} else {
			name := auth.HeaderName
			if name == "" {
				name = "X-API-Key"
			}
			req.Header.Set(name, val)
		}
	case "basic":
		req.SetBasicAuth(expandEnv(auth.Username), expandEnv(auth.Password))
	default:
		if token, ok := fCtx["access_token"].(string); ok && token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}
}
