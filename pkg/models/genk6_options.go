package models

// GenK6Options holds options for the `tya genk6` command.
type GenK6Options struct {
	ConfigFile string // Path to config-run.yml
	OutputDir  string // Output directory for generated scripts
	APIDir     string // Path to api/ directory with payload fixtures
}
