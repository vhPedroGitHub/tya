package models

// CreateOptions holds configuration for the create command.
type CreateOptions struct {
	// SpecFile is the path to the OpenAPI YAML spec.
	SpecFile string
	// ConfigFile is the path to config-create.yml.
	ConfigFile string
}
