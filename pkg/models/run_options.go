package models

// RunOptions holds configuration for the run command.
type RunOptions struct {
	// ConfigFile is the path to config-run.yml.
	ConfigFile string
	// TestMode runs each flow step exactly once (ignores RPS).
	TestMode bool
	// Flow filters execution to a specific named flow.
	Flow string
	// StepMode enables interactive step-through of executed steps when
	// running in test mode (-t). It lets the user inspect request/response
	// per step and navigate forward/backward.
	StepMode bool
	// LiveUI enables the in-place live dashboard that updates flow metrics
	// every second instead of streaming log lines.
	LiveUI bool
}
