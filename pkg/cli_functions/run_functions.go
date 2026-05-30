package cli_functions

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"syscall"
	"time"

	"github.com/vhPedroGitHub/tya/pkg/configyml"
	"github.com/vhPedroGitHub/tya/pkg/models"
	runflowengine "github.com/vhPedroGitHub/tya/pkg/runFlowEngine"
	"github.com/vhPedroGitHub/tya/pkg/ui"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// runReport is the top-level JSON report structure written at the end of a run.
type runReport struct {
	RunID      string                              `json:"run_id"`
	StartedAt  time.Time                           `json:"started_at"`
	FinishedAt time.Time                           `json:"finished_at"`
	DurationS  float64                             `json:"duration_s"`
	Flows      map[string]runflowengine.FlowReport `json:"flows"`
}

// newRunID returns a short pseudo-unique run identifier.
func newRunID() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

// RunFlows is the main entry point for the run command.
func RunFlows(log *zap.Logger, opts *models.RunOptions) error {
	cfg, err := configyml.LoadRunConfig(opts.ConfigFile)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// If the user enabled interactive step mode, make sure test mode is also
	// enabled so we run a single sequential pass suitable for stepping.
	if opts.StepMode && !opts.TestMode {
		log.Info("step mode implies test mode; enabling test mode")
		opts.TestMode = true
	}

	// Build auth profile map.
	authMap := map[string]configyml.AuthProfile{}
	for _, a := range cfg.AuthProfiles {
		authMap[a.Name] = a
	}

	// Filter to a single flow if --flow is specified.
	flows := cfg.Flows
	if opts.Flow != "" {
		filtered := []configyml.Flow{}
		for _, f := range flows {
			if f.Name == opts.Flow {
				filtered = append(filtered, f)
			}
		}
		if len(filtered) == 0 {
			return fmt.Errorf("flow %q not found in config", opts.Flow)
		}
		flows = filtered
	}

	if len(flows) == 0 {
		log.Warn("no flows to run")
		return nil
	}

	// Validate dependency graph and exit early on any violation.
	if err := ValidateDependencyGraph(flows); err != nil {
		return fmt.Errorf("dependency graph error: %w", err)
	}

	// Sort flows into topological execution order.
	flows = TopologicalOrder(flows)

	// TYA_BASE_URL env var overrides config-run.yml base_url.
	baseURL := cfg.BaseURL
	if env := os.Getenv("TYA_BASE_URL"); env != "" {
		baseURL = env
	}
	startedAt := time.Now()

	// Create the global bucket shared across all flows.
	bucket := runflowengine.NewGlobalBucket()

	// Start live dashboard UI if requested. When the live UI is enabled we
	// redirect engine logging into a separate file so JSON logs don't spam the
	// terminal and interfere with the in-place dashboard rendering.
	if opts.LiveUI {
		// open log file for engine logs
		f, err := os.OpenFile("tya-live.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			log.Warn("could not open live log file, continuing with terminal logging", zap.Error(err))
		} else {
			// Forcibly redirect process stderr to the file so any library
			// writing to fd 2 does not pollute the live dashboard. This uses
			// a syscall and is Unix-specific. On non-Unix platforms we still
			// replace the global zap logger.
			var prevGlobal *zap.Logger
			if runtime.GOOS == "linux" || runtime.GOOS == "darwin" || runtime.GOOS == "freebsd" {
				// duplicate current stderr so we can restore it later
				oldFD, dupErr := syscall.Dup(int(os.Stderr.Fd()))
				if dupErr == nil {
					// replace fd 2 with our file
					if dup2Err := syscall.Dup2(int(f.Fd()), int(os.Stderr.Fd())); dup2Err == nil {
						// ensure restoration at the end
						// copy oldFD into local to capture by value
						old := oldFD
						defer func() {
							if err := syscall.Dup2(old, int(os.Stderr.Fd())); err != nil {
								log.Warn("failed to restore stderr", zap.Error(err))
							}
							if err := syscall.Close(old); err != nil {
								log.Warn("failed to close duplicated fd", zap.Error(err))
							}
							if err := f.Close(); err != nil {
								log.Warn("failed to close live log file", zap.Error(err))
							}
						}()
					} else {
						// cleanup oldFD if dup2 failed
						if err := syscall.Close(oldFD); err != nil {
							log.Warn("failed to close duplicated fd", zap.Error(err))
						}
					}
				}
			}

			// create a file-backed zap logger and replace globals so any
			// package using zap.L() also routes to the file.
			encCfg := zap.NewProductionEncoderConfig()
			enc := zapcore.NewJSONEncoder(encCfg)
			core := zapcore.NewCore(enc, zapcore.AddSync(f), zap.InfoLevel)
			fileLogger := zap.New(core)
			prevGlobal = zap.L()
			zap.ReplaceGlobals(fileLogger)
			// ensure we restore the previous global logger
			defer func() { zap.ReplaceGlobals(prevGlobal); _ = fileLogger.Sync() }()

			// use file logger for local flows as well
			log = fileLogger
		}

		if err := ui.StartDashboard(log); err != nil {
			log.Warn("could not start live dashboard", zap.Error(err))
		} else {
			defer ui.StopDashboard()
		}
	}

	// Build the executor functions that close over logger, authMap, opts, baseURL, bucket.
	flowExec := func(flow configyml.Flow) (runflowengine.FlowReport, runflowengine.FlowContext) {
		return runflowengine.ExecuteFlowv2(log, flow, authMap, opts, baseURL, bucket)
	}
	iterateExec := func(flow configyml.Flow) runflowengine.FlowReport {
		return runflowengine.ExecuteIterateFlow(log, flow, authMap, opts, baseURL, bucket)
	}

	results := runflowengine.RunScheduler(log, flows, flowExec, iterateExec)

	finishedAt := time.Now()

	report := runReport{
		RunID:      newRunID(),
		StartedAt:  startedAt,
		FinishedAt: finishedAt,
		DurationS:  finishedAt.Sub(startedAt).Seconds(),
		Flows:      results,
	}

	reportPath := fmt.Sprintf("tya-report-%s.json", startedAt.Format("20060102-150405"))
	data, _ := json.MarshalIndent(report, "", "  ")
	if err := os.WriteFile(reportPath, data, 0o644); err != nil {
		log.Warn("could not write report", zap.Error(err))
	} else {
		log.Info("report written", zap.String("path", reportPath))
	}

	return nil
}
