package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"tya/pkg/cli_functions"
	"tya/pkg/configyml"
	"tya/pkg/models"

	"github.com/brianvoe/gofakeit/v6"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

// NewCreateCmd returns the cobra command for `tya create`.
func NewCreateCmd(log *zap.Logger) *cobra.Command {
	opts := &models.CreateOptions{}

	cmd := &cobra.Command{
		Use:   "create [openapi.yaml]",
		Short: "Parse an OpenAPI spec and generate payload fixtures",
		Long: `Parses an OpenAPI YAML spec and generates:
  - JSON model schemas under models/
  - Per-endpoint config.yml and payload JSON files under api/

Example:
  tya create openapi.yaml
  tya create openapi.yaml --config config-create.yml`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.SpecFile = args[0]
			return runCreate(log, opts)
		},
	}

	cmd.Flags().StringVar(&opts.ConfigFile, "config", "config-create.yml", "path to config-create.yml")

	return cmd
}

// ---------------------------------------------------------------------------
// OpenAPI parsing helpers (minimal, no external openapi lib dependency)
// ---------------------------------------------------------------------------

type openAPISpec struct {
	Info  struct{ Title string } `yaml:"info"`
	Paths map[string]pathItem   `yaml:"paths"`
}

type pathItem map[string]operationObj // key: get/post/put/patch/delete/...

type operationObj struct {
	OperationID string               `yaml:"operationId"`
	Parameters  []parameterObj       `yaml:"parameters"`
	RequestBody *requestBodyObj      `yaml:"requestBody"`
	Responses   map[string]yaml.Node `yaml:"responses"`
}

type parameterObj struct {
	Name     string    `yaml:"name"`
	In       string    `yaml:"in"` // path | query | header | cookie
	Required bool      `yaml:"required"`
	Schema   schemaObj `yaml:"schema"`
}

type requestBodyObj struct {
	Required bool                  `yaml:"required"`
	Content  map[string]mediaType  `yaml:"content"`
}

type mediaType struct {
	Schema schemaObj `yaml:"schema"`
}

type schemaObj struct {
	Type       string               `yaml:"type"`
	Format     string               `yaml:"format"`
	Properties map[string]schemaObj `yaml:"properties"`
	Items      *schemaObj           `yaml:"items"`
	Ref        string               `yaml:"$ref"`
	Minimum    *float64             `yaml:"minimum"`
	Maximum    *float64             `yaml:"maximum"`
	MinLength  *int                 `yaml:"minLength"`
	MaxLength  *int                 `yaml:"maxLength"`
	Enum       []any                `yaml:"enum"`
}

func loadSpec(path string) (*openAPISpec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read spec: %w", err)
	}
	var spec openAPISpec
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("parse spec: %w", err)
	}
	return &spec, nil
}

// ---------------------------------------------------------------------------
// Endpoint config.yml
// ---------------------------------------------------------------------------

type endpointConfig struct {
	Endpoint   string           `yaml:"endpoint"`
	Method     string           `yaml:"method"`
	Parameters []endpointParam  `yaml:"parameters,omitempty"`
}

type endpointParam struct {
	Name     string `yaml:"name"`
	In       string `yaml:"in"`
	Required bool   `yaml:"required"`
	Type     string `yaml:"type,omitempty"`
	Format   string `yaml:"format,omitempty"`
}

// ---------------------------------------------------------------------------
// Fake value generation
// ---------------------------------------------------------------------------

func fakeValue(s schemaObj) any {
	if len(s.Enum) > 0 {
		return s.Enum[gofakeit.Number(0, len(s.Enum)-1)]
	}
	switch s.Type {
	case "integer", "number":
		min, max := 1, 1000
		if s.Minimum != nil {
			min = int(*s.Minimum)
		}
		if s.Maximum != nil {
			max = int(*s.Maximum)
		}
		return gofakeit.Number(min, max)
	case "boolean":
		return gofakeit.Bool()
	case "array":
		if s.Items != nil {
			return []any{fakeValue(*s.Items), fakeValue(*s.Items)}
		}
		return []any{}
	case "object":
		obj := map[string]any{}
		for name, prop := range s.Properties {
			obj[name] = fakeValue(prop)
		}
		return obj
	default: // string
		switch s.Format {
		case "email":
			return gofakeit.Email()
		case "date":
			return gofakeit.Date().Format("2006-01-02")
		case "date-time":
			return gofakeit.Date().Format("2006-01-02T15:04:05Z")
		case "uuid":
			return gofakeit.UUID()
		case "uri", "url":
			return gofakeit.URL()
		case "password":
			return gofakeit.Password(true, true, true, true, false, 12)
		case "phone":
			return gofakeit.Phone()
		default:
			minLen := 5
			if s.MinLength != nil {
				minLen = *s.MinLength
			}
			maxLen := 30
			if s.MaxLength != nil {
				maxLen = *s.MaxLength
			}
			if maxLen < minLen {
				maxLen = minLen + 10
			}
			return gofakeit.LetterN(uint(gofakeit.Number(minLen, maxLen)))
		}
	}
}

func generatePayload(schema schemaObj) map[string]any {
	payload := map[string]any{}
	for name, prop := range schema.Properties {
		payload[name] = fakeValue(prop)
	}
	return payload
}

// ---------------------------------------------------------------------------
// openapi-generator-cli model generation
// ---------------------------------------------------------------------------

func runOpenAPIGenerator(log *zap.Logger, specFile, outDir string) error {
	jarURL := "https://repo1.maven.org/maven2/org/openapitools/openapi-generator-cli/7.4.0/openapi-generator-cli-7.4.0.jar"
	jarPath := "/tmp/openapi-generator-cli.jar"

	if !cli_functions.FileExists(jarPath) {
		log.Info("downloading openapi-generator-cli", zap.String("url", jarURL))
		cmd := exec.Command("curl", "-fsSL", "-o", jarPath, jarURL)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("download openapi-generator-cli: %w", err)
		}
	}

	log.Info("running openapi-generator-cli", zap.String("spec", specFile), zap.String("out", outDir))
	cmd := exec.Command("java", "-jar", jarPath,
		"generate",
		"-i", specFile,
		"-g", "go",
		"-o", outDir,
		"--skip-validate-spec",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// ---------------------------------------------------------------------------
// Main create logic
// ---------------------------------------------------------------------------

func runCreate(log *zap.Logger, opts *models.CreateOptions) error {
	// Load create config
	cfg, err := configyml.LoadCreateConfig(opts.ConfigFile)
	if err != nil {
		log.Warn("could not load config-create.yml, using defaults", zap.Error(err))
		cfg = configyml.DefaultCreateConfig()
	}

	// Load spec
	spec, err := loadSpec(opts.SpecFile)
	if err != nil {
		return fmt.Errorf("load spec: %w", err)
	}
	log.Info("loaded OpenAPI spec", zap.String("file", opts.SpecFile))

	// Try to run openapi-generator-cli (best effort)
	genOut := filepath.Join("models", "_generated")
	if genErr := runOpenAPIGenerator(log, opts.SpecFile, genOut); genErr != nil {
		log.Warn("openapi-generator-cli failed, skipping model generation", zap.Error(genErr))
	}

	// Process each path + method
	for path, methods := range spec.Paths {
		for method, op := range methods {
			method = strings.ToUpper(method)
			// Normalise path to directory name: /users/{id} -> users_{id}
			dirName := strings.Trim(path, "/")
			dirName = strings.ReplaceAll(dirName, "/", "_")
			if dirName == "" {
				dirName = "root"
			}

			endpointDir := filepath.Join("api", dirName)
			methodDir := filepath.Join(endpointDir, strings.ToLower(method))

			if err := cli_functions.EnsureDir(methodDir); err != nil {
				return fmt.Errorf("mkdir %s: %w", methodDir, err)
			}

			// Write endpoint config.yml
			configPath := filepath.Join(endpointDir, "config.yml")
			if !cli_functions.FileExists(configPath) {
				epCfg := endpointConfig{
					Endpoint: path,
					Method:   method,
				}
				for _, p := range op.Parameters {
					epCfg.Parameters = append(epCfg.Parameters, endpointParam{
						Name:     p.Name,
						In:       p.In,
						Required: p.Required,
						Type:     p.Schema.Type,
						Format:   p.Schema.Format,
					})
				}
				data, _ := yaml.Marshal(epCfg)
				if err := os.WriteFile(configPath, data, 0o644); err != nil {
					return fmt.Errorf("write %s: %w", configPath, err)
				}
				log.Info("wrote endpoint config", zap.String("path", configPath))
			}

			// Generate payload files
			count := cfg.PayloadCount(path, method)
			var bodySchema schemaObj
			if op.RequestBody != nil {
				for _, mt := range op.RequestBody.Content {
					bodySchema = mt.Schema
					break
				}
			}

			for i := 1; i <= count; i++ {
				payloadPath := filepath.Join(methodDir, fmt.Sprintf("payload_%d.json", i))
				if cli_functions.FileExists(payloadPath) {
					continue
				}
				var payload any
				if len(bodySchema.Properties) > 0 {
					payload = generatePayload(bodySchema)
				} else {
					payload = map[string]any{}
				}
				data, err := json.MarshalIndent(payload, "", "  ")
				if err != nil {
					return fmt.Errorf("marshal payload: %w", err)
				}
				if err := os.WriteFile(payloadPath, data, 0o644); err != nil {
					return fmt.Errorf("write payload %s: %w", payloadPath, err)
				}
				log.Info("wrote payload", zap.String("path", payloadPath))
			}

			log.Info("processed endpoint",
				zap.String("path", path),
				zap.String("method", method),
				zap.Int("payloads", count),
				zap.String("operationId", op.OperationID),
			)
		}
	}

	log.Info("create completed successfully")
	return nil
}
