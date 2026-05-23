package cli_functions

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/brianvoe/gofakeit/v6"
	"github.com/pb33f/libopenapi"
	"github.com/pb33f/libopenapi/datamodel/high/base"
	v3high "github.com/pb33f/libopenapi/datamodel/high/v3"
	"github.com/vhPedroGitHub/tya/pkg/configyml"
	"github.com/vhPedroGitHub/tya/pkg/models"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// Spec loading
// ---------------------------------------------------------------------------

func loadSpec(path string) (*v3high.Document, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read spec file: %w", err)
	}

	doc, err := libopenapi.NewDocument(data)
	if err != nil {
		return nil, fmt.Errorf("parse spec: %w", err)
	}

	v3Model, err := doc.BuildV3Model()
	if err != nil {
		if v3Model == nil {
			return nil, fmt.Errorf("build v3 model: %w", err)
		}
	}

	return &v3Model.Model, nil
}

// ---------------------------------------------------------------------------
// Endpoint config.yml types
// ---------------------------------------------------------------------------

type endpointConfig struct {
	Endpoint string         `yaml:"endpoint"`
	Methods  []methodConfig `yaml:"methods"`
}

type methodConfig struct {
	Method     string          `yaml:"method"`
	Parameters []endpointParam `yaml:"parameters,omitempty"`
}

type endpointParam struct {
	Name     string `yaml:"name"`
	In       string `yaml:"in"`
	Required bool   `yaml:"required"`
	Type     string `yaml:"type,omitempty"`
	Format   string `yaml:"format,omitempty"`
}

// ---------------------------------------------------------------------------
// schemaObj — internal flat representation used by fakeValue / generatePayload.
// Built from libopenapi's high-level base.Schema so the generation logic
// stays decoupled from the parser library.
// ---------------------------------------------------------------------------

type schemaObj struct {
	Type       string
	Format     string
	Enum       []any
	Minimum    *float64
	Maximum    *float64
	MinLength  *int
	MaxLength  *int
	Items      *schemaObj
	Properties map[string]schemaObj
}

// schemaFromHigh converts a libopenapi high-level *base.Schema into schemaObj.
// libopenapi's Schema.Type is a *base.SchemaDynamicValue[string, bool] in 3.1,
// but the high-level helper Schema.Type is already a []string — we take [0].
func schemaFromHigh(s *base.SchemaProxy) schemaObj {
	if s == nil {
		return schemaObj{}
	}

	schema := s.Schema()
	if schema == nil {
		return schemaObj{}
	}

	obj := schemaObj{
		Format: schema.Format,
	}

	// Type: libopenapi high-level exposes []string (handles both 3.0 and 3.1)
	if len(schema.Type) > 0 {
		obj.Type = schema.Type[0]
	}

	// Enum values
	for _, e := range schema.Enum {
		if e != nil {
			obj.Enum = append(obj.Enum, e.Value)
		}
	}

	// Numeric bounds (float64 pointers in libopenapi high model)
	if schema.Minimum != nil {
		obj.Minimum = schema.Minimum
	}
	if schema.Maximum != nil {
		obj.Maximum = schema.Maximum
	}

	// String length bounds
	if schema.MinLength != nil {
		ml := int(*schema.MinLength)
		obj.MinLength = &ml
	}
	if schema.MaxLength != nil {
		ml := int(*schema.MaxLength)
		obj.MaxLength = &ml
	}

	// Array items
	if schema.Items != nil && schema.Items.IsA() {
		items := schemaFromHigh(schema.Items.A)
		obj.Items = &items
	}

	// Object properties — libopenapi uses an ordered map
	if schema.Properties != nil && schema.Properties.Len() > 0 {
		obj.Properties = make(map[string]schemaObj, schema.Properties.Len())
		for pair := schema.Properties.First(); pair != nil; pair = pair.Next() {
			obj.Properties[pair.Key()] = schemaFromHigh(pair.Value())
		}
	}

	return obj
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

// exampleFromSchema builds an example value from a schemaObj. It returns
// either a map (for objects), a slice (for arrays) or a primitive value.
func exampleFromSchema(s schemaObj) any {
	switch s.Type {
	case "object":
		m := map[string]any{}
		for k, p := range s.Properties {
			m[k] = exampleFromSchema(p)
		}
		return m
	case "array":
		if s.Items != nil {
			return []any{exampleFromSchema(*s.Items), exampleFromSchema(*s.Items)}
		}
		return []any{}
	default:
		return fakeValue(s)
	}
}

// ---------------------------------------------------------------------------
// operationsByMethod — iterate the 8 standard HTTP methods on a PathItem.
// libopenapi exposes them as named fields, not as a map.
// ---------------------------------------------------------------------------

type methodOp struct {
	Method string
	Op     *v3high.Operation
}

func operationsByMethod(item *v3high.PathItem) []methodOp {
	candidates := []methodOp{
		{"GET", item.Get},
		{"POST", item.Post},
		{"PUT", item.Put},
		{"PATCH", item.Patch},
		{"DELETE", item.Delete},
		{"HEAD", item.Head},
		{"OPTIONS", item.Options},
		{"TRACE", item.Trace},
	}
	var out []methodOp
	for _, c := range candidates {
		if c.Op != nil {
			out = append(out, c)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// RunCreate — main entry point
// ---------------------------------------------------------------------------

func RunCreate(log *zap.Logger, opts *models.CreateOptions) error {
	// Load create config
	cfg, err := configyml.LoadCreateConfig(opts.ConfigFile)
	if err != nil {
		log.Warn("could not load config-create.yml, using defaults", zap.Error(err))
		cfg = configyml.DefaultCreateConfig()
	}

	// Load and parse the OpenAPI spec
	spec, err := loadSpec(opts.SpecFile)
	if err != nil {
		return fmt.Errorf("load spec: %w", err)
	}
	log.Info("loaded OpenAPI spec", zap.String("file", opts.SpecFile))

	// Guard: spec may have no paths defined
	if spec.Paths == nil {
		log.Warn("spec has no paths defined")
		return nil
	}

	// Iterate paths — libopenapi uses an ordered map; First()/Next() preserves
	// document order, which also makes output deterministic.
	for pair := spec.Paths.PathItems.First(); pair != nil; pair = pair.Next() {
		path := pair.Key()
		pathItem := pair.Value()

		if pathItem == nil {
			continue
		}

		// Normalise path to directory name: /users/{id} -> users_{id}
		dirName := strings.Trim(path, "/")
		dirName = strings.ReplaceAll(dirName, "/", "_")
		if dirName == "" {
			dirName = "root"
		}

		endpointDir := filepath.Join("api", dirName)

		for _, entry := range operationsByMethod(pathItem) {
			method := entry.Method
			op := entry.Op

			// Manage endpoint config.yml: load, merge method entry, write back if needed
			configPath := filepath.Join(endpointDir, "config.yml")

			// Create dir
			if err := os.MkdirAll(endpointDir, 0755); err != nil {
				return err
			}

			var epCfg endpointConfig

			if FileExists(configPath) {
				data, err := os.ReadFile(configPath)
				if err != nil {
					return fmt.Errorf("read config %s: %w", configPath, err)
				}
				if err := yaml.Unmarshal(data, &epCfg); err != nil {
					return fmt.Errorf("unmarshal config %s: %w", configPath, err)
				}
			} else {
				epCfg = endpointConfig{
					Endpoint: path,
					Methods:  []methodConfig{},
				}
			}

			// Check if method already exists (case-insensitive)
			methodExists := false
			for _, m := range epCfg.Methods {
				if strings.EqualFold(m.Method, method) {
					methodExists = true
					break
				}
			}

			if !methodExists {
				mCfg := methodConfig{
					Method:     method,
					Parameters: []endpointParam{},
				}

				for _, p := range op.Parameters {
					if p == nil {
						continue
					}
					var pType, pFormat string
					if p.Schema != nil {
						s := schemaFromHigh(p.Schema)
						pType = s.Type
						pFormat = s.Format
					}

					required := false
					if p.Required != nil {
						required = *p.Required
					}

					mCfg.Parameters = append(mCfg.Parameters, endpointParam{
						Name:     p.Name,
						In:       p.In,
						Required: required,
						Type:     pType,
						Format:   pFormat,
					})
				}

				epCfg.Methods = append(epCfg.Methods, mCfg)

				data, err := yaml.Marshal(epCfg)
				if err != nil {
					return fmt.Errorf("marshal config %s: %w", configPath, err)
				}
				if err := os.WriteFile(configPath, data, 0o644); err != nil {
					return fmt.Errorf("write config %s: %w", configPath, err)
				}

				log.Info("updated endpoint config",
					zap.String("path", configPath),
					zap.String("method", method),
				)
			}

			// Build the body schema for payload generation.
			// op.RequestBody is *v3high.RequestBody in libopenapi.
			// Content is map[string]*v3high.MediaType.
			var bodySchema schemaObj
			if op.RequestBody != nil && op.RequestBody.Content != nil {
				for contentPair := op.RequestBody.Content.First(); contentPair != nil; contentPair = contentPair.Next() {
					mt := contentPair.Value()
					if mt != nil && mt.Schema != nil {
						bodySchema = schemaFromHigh(mt.Schema)
						break
					}
				}
			}

			// Only generate payloads (and create method dir) if a request body schema exists
			hasRequestBody := len(bodySchema.Properties) > 0
			payloadsCreated := 0
			if hasRequestBody {
				methodDir := filepath.Join(endpointDir, strings.ToLower(method))
				if err := EnsureDir(methodDir); err != nil {
					return fmt.Errorf("mkdir %s: %w", methodDir, err)
				}

				count := cfg.PayloadCount(path, method)
				for i := 1; i <= count; i++ {
					payloadPath := filepath.Join(methodDir, fmt.Sprintf("payload_%d.json", i))
					if FileExists(payloadPath) {
						payloadsCreated++
						continue
					}

					payload := generatePayload(bodySchema)

					data, err := json.MarshalIndent(payload, "", "  ")
					if err != nil {
						return fmt.Errorf("marshal payload: %w", err)
					}
					if err := os.WriteFile(payloadPath, data, 0o644); err != nil {
						return fmt.Errorf("write payload %s: %w", payloadPath, err)
					}
					payloadsCreated++
					log.Info("wrote payload", zap.String("path", payloadPath))
				}
			}

			log.Info("processed endpoint",
				zap.String("path", path),
				zap.String("method", method),
				zap.Int("payloads", payloadsCreated),
				zap.String("operationId", op.OperationId),
			)
		}
	}

	// Create all json example models in models folder

	// check if model folder exist
	dir, _ := os.Getwd()
	modelsDir := filepath.Join(dir, "models")
	if err := os.MkdirAll(modelsDir, 0o755); err != nil {
		return err
	}

	// Iterate component schemas and write one example per model unless exists
	if spec.Components != nil && spec.Components.Schemas != nil {
		for pair := spec.Components.Schemas.First(); pair != nil; pair = pair.Next() {
			name := pair.Key()
			comp := pair.Value()
			if comp == nil {
				continue
			}

			// Build schemaObj from the component schema proxy
			s := schemaFromHigh(comp)

			// Prepare example value
			example := exampleFromSchema(s)

			// Write file only if not present
			modelPath := filepath.Join(modelsDir, fmt.Sprintf("%s.json", name))
			if FileExists(modelPath) {
				continue
			}

			data, err := json.MarshalIndent(example, "", "  ")
			if err != nil {
				return fmt.Errorf("marshal model %s: %w", name, err)
			}
			if err := os.WriteFile(modelPath, data, 0o644); err != nil {
				return fmt.Errorf("write model %s: %w", modelPath, err)
			}
			log.Info("wrote model", zap.String("path", modelPath))
		}
	}

	log.Info("create completed successfully")
	return nil
}
