// Package configyml provides structs, validation, load and write helpers
// for TYA's YAML configuration files (config-create.yml and config-run.yml).
package configyml

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// config-create.yml
// ---------------------------------------------------------------------------

// CreateConfig is the schema for config-create.yml.
type CreateConfig struct {
	// PayloadsPerMethod is the default number of payloads generated per endpoint+method.
	PayloadsPerMethod int `yaml:"payloads_per_method"`
	// Overrides allow per-endpoint/method payload count overrides.
	Overrides []CreateOverride `yaml:"overrides,omitempty"`
}

// CreateOverride allows overriding payload count for a specific endpoint + method.
type CreateOverride struct {
	Endpoint string `yaml:"endpoint"`
	Method   string `yaml:"method"`
	Count    int    `yaml:"count"`
}

// DefaultCreateConfig returns a CreateConfig populated with sensible defaults.
func DefaultCreateConfig() CreateConfig {
	return CreateConfig{
		PayloadsPerMethod: 5,
	}
}

// LoadCreateConfig reads and parses config-create.yml from path.
func LoadCreateConfig(path string) (CreateConfig, error) {
	cfg := DefaultCreateConfig()
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, nil
}

// WriteCreateConfig serialises cfg to path.
func WriteCreateConfig(path string, cfg CreateConfig) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// PayloadCount returns the number of payloads to generate for the given endpoint+method,
// respecting per-endpoint overrides.
func (c CreateConfig) PayloadCount(endpoint, method string) int {
	for _, o := range c.Overrides {
		if o.Endpoint == endpoint && o.Method == method {
			return o.Count
		}
	}
	return c.PayloadsPerMethod
}

// ---------------------------------------------------------------------------
// config-run.yml
// ---------------------------------------------------------------------------

// RunConfig is the schema for config-run.yml.
type RunConfig struct {
	BaseURL      string        `yaml:"base_url,omitempty"`
	AuthProfiles []AuthProfile `yaml:"auth_profiles,omitempty"`
	Flows        []Flow        `yaml:"flows"`
}

// AuthProfile describes one authentication configuration.
type AuthProfile struct {
	Name               string `yaml:"name"`
	Type               string `yaml:"type"` // oauth2_password | oauth2_client_credentials | api_key | basic | custom_login

	// OAuth2 password / client_credentials
	TokenURL     string   `yaml:"token_url,omitempty"`
	ClientID     string   `yaml:"client_id,omitempty"`
	ClientSecret string   `yaml:"client_secret,omitempty"`
	Username     string   `yaml:"username,omitempty"`
	Password     string   `yaml:"password,omitempty"`
	Scopes       []string `yaml:"scopes,omitempty"`

	// Injection
	InjectAs   string `yaml:"inject_as,omitempty"`   // bearer | header | query
	HeaderName string `yaml:"header_name,omitempty"` // default: Authorization
	QueryParam string `yaml:"query_param,omitempty"`

	// Token lifecycle
	RefreshBeforeExpiry string `yaml:"refresh_before_expiry,omitempty"`
	RetryOn401          bool   `yaml:"retry_on_401,omitempty"`

	// API key
	Value string `yaml:"value,omitempty"`

	// custom_login
	LoginEndpoint   string            `yaml:"login_endpoint,omitempty"`
	Method          string            `yaml:"method,omitempty"`
	Payload         string            `yaml:"payload,omitempty"`
	ExtractToken    map[string]string `yaml:"extract_token,omitempty"`
	RefreshEndpoint string            `yaml:"refresh_endpoint,omitempty"`
	RefreshMethod   string            `yaml:"refresh_method,omitempty"`
	RefreshPayload  string            `yaml:"refresh_payload,omitempty"`
	RefreshExtract  map[string]string `yaml:"refresh_extract,omitempty"`
}

// Flow describes a named test/load flow.
type Flow struct {
	Name              string     `yaml:"name"`
	Type              string     `yaml:"type"` // end-to-end | alone
	Duration          string     `yaml:"duration,omitempty"`
	RequestsPerSecond float64    `yaml:"requests_per_second,omitempty"`
	Auth              string     `yaml:"auth,omitempty"`
	Steps             []Step     `yaml:"steps"`
	// DependsOn lists flow names that must complete successfully before this
	// flow starts. TYA validates the list at startup and rejects cycles.
	DependsOn []string   `yaml:"depends_on,omitempty"`
	// Children holds wire-flows that run exactly once after the parent
	// completes, inheriting the parent's last execution context.
	Children  []WireFlow `yaml:"children,omitempty"`
}

// WireFlow is a one-shot flow that runs after its parent flow completes.
// It inherits the parent's final execution context and cannot be referenced
// in depends_on lists or have its own children.
type WireFlow struct {
	Name  string `yaml:"name"`
	Type  string `yaml:"type"` // must be "wire-flow"
	Auth  string `yaml:"auth,omitempty"`
	Steps []Step `yaml:"steps"`
}

// Step is one HTTP request inside a flow.
type Step struct {
	ID              string      `yaml:"id,omitempty"`
	Endpoint        string      `yaml:"endpoint"`
	Method          string      `yaml:"method"`
	PayloadStrategy string      `yaml:"payload_strategy,omitempty"` // random | fixed | template | extracted
	PayloadFile     string      `yaml:"payload_file,omitempty"`
	PayloadTemplate string      `yaml:"payload_template,omitempty"`
	FromStep        string      `yaml:"from_step,omitempty"`
	Extract         []Extractor `yaml:"extract,omitempty"`
}

// Extractor pulls a value from a step's response into the flow context.
type Extractor struct {
	Field string `yaml:"field"`
	As    string `yaml:"as"`
}

// DefaultRunConfig returns a minimal RunConfig skeleton.
func DefaultRunConfig() RunConfig {
	return RunConfig{
		AuthProfiles: []AuthProfile{},
		Flows:        []Flow{},
	}
}

// LoadRunConfig reads and parses config-run.yml from path.
func LoadRunConfig(path string) (RunConfig, error) {
	cfg := DefaultRunConfig()
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, nil
}

// WriteRunConfig serialises cfg to path.
func WriteRunConfig(path string, cfg RunConfig) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
