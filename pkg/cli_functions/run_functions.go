package cli_functions

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/vhPedroGitHub/tya/pkg/configyml"
	"github.com/vhPedroGitHub/tya/pkg/models"
	runflowengine "github.com/vhPedroGitHub/tya/pkg/runFlowEngine"
	"go.uber.org/zap"
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
