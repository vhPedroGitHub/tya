package models

// RunOptions holds configuration for the run command.
type RunOptions struct {
	// ConfigFile is the path to config-run.yml.
	ConfigFile string
	// TestMode runs each flow step exactly once (ignores RPS).
	TestMode bool
	// Flow filters execution to a specific named flow.
	Flow string
}
