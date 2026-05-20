/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	"nvcf-cli/internal/selfhosted"
	"nvcf-cli/internal/selfhosted/kubectx"
	"nvcf-cli/internal/selfhosted/progress"
)

var (
	checkPre              bool
	checkControlPlane     bool
	checkComputePlane     bool
	checkAll              bool
	checkClusterName      string
	checkLocalOnly        bool
	checkSkipInotifyCheck bool
)

// newInotifyProberForSelfHosted is a package-level seam so unit tests can
// inject a fake prober without standing up a real Kubernetes client.
// Production callers use selfhosted.NewInotifyProber.
var newInotifyProberForSelfHosted = func() selfhosted.NodeInotifyProber {
	return selfhosted.NewInotifyProber()
}

var checkWriterIsTTY = isWriterTTY

var selfHostedCheckCmd = &cobra.Command{
	Use:          "check",
	Short:        "Run pre-flight, control-plane, and/or compute-plane health checks",
	RunE:         runSelfHostedCheck,
	SilenceUsage: true,
}

func init() {
	selfHostedCmd.AddCommand(selfHostedCheckCmd)
	selfHostedCheckCmd.Flags().BoolVar(&checkPre, "pre", false, "Run pre-flight (local-host + cluster readiness)")
	selfHostedCheckCmd.Flags().BoolVar(&checkControlPlane, "control-plane", false, "Run control-plane health checks")
	selfHostedCheckCmd.Flags().BoolVar(&checkComputePlane, "compute-plane", false, "Run compute-plane health checks (requires --cluster-name)")
	selfHostedCheckCmd.Flags().BoolVar(&checkAll, "all", false, "Run all check categories")
	selfHostedCheckCmd.Flags().StringVar(&checkClusterName, "cluster-name", "", "Cluster name for compute-plane checks")
	selfHostedCheckCmd.Flags().BoolVar(&checkLocalOnly, "local-only", false, "Run local-host checks only (no kubectl contact)")
	selfHostedCheckCmd.Flags().BoolVar(&checkSkipInotifyCheck, "skip-inotify-check", false,
		"Disable the per-node inotify-limits probe. Required in environments where the CLI's kubeconfig user "+
			"cannot create pods in the default namespace (PSA-restricted clusters, locked-down RBAC). "+
			"Env: NVCF_CLI_SELFHOSTED_SKIP_INOTIFY")
}

func runSelfHostedCheck(c *cobra.Command, _ []string) error {
	if !checkPre && !checkControlPlane && !checkComputePlane && !checkAll {
		return fmt.Errorf("at least one of --pre, --control-plane, --compute-plane, or --all is required")
	}

	ctx, cancel := context.WithTimeout(c.Context(), 2*time.Minute)
	defer cancel()

	// Legacy --output=json: warn and treat as --json.
	// The flag will be removed in v2; for now it falls through to --json behavior.
	if selfHostedOutput == "json" && !selfHostedJSON {
		fmt.Fprintln(c.ErrOrStderr(), "warning: --output=json is deprecated; use --json (will be removed in v2)")
		selfHostedJSON = true
	}

	// Determine effective LocalOnly: explicit flag, per-check flag, or env var.
	localOnly := checkLocalOnly || os.Getenv("NVCF_CLI_SELFHOSTED_LOCAL_ONLY") != ""

	cfg := selfhosted.PreflightConfig{
		LocalOnly: localOnly,
		Tools:     selfHostedPreflightTools(),
	}

	sink, err := selectCheckRenderer(c.ErrOrStderr(), selfHostedWait != "")
	if err != nil {
		return err
	}
	defer sink.Close()
	if starter, ok := sink.(interface{ Start() }); ok {
		starter.Start()
	}

	var lastResults []selfhosted.CheckResult

	runOnce := func() []selfhosted.CheckResult {
		var results []selfhosted.CheckResult
		if checkPre || checkAll {
			results = append(results, runPreflightByRole(ctx, cfg, sink)...)
		}
		// Inject force-fail seam for tests.
		if os.Getenv("NVCF_CLI_SELFHOSTED_FORCE_FAIL") != "" {
			results = append([]selfhosted.CheckResult{{
				ID:       "force-fail-test-seam",
				Category: "test",
				Severity: "error",
				Passed:   false,
				Message:  "forced failure (test seam)",
			}}, results...)
		}
		// control-plane / compute-plane wired in M3/M4 — placeholder no-op for M2.
		return results
	}

	if selfHostedWait == "" {
		// Single-shot mode.
		lastResults = runOnce()
		emitCheckFinal(ctx, sink, lastResults)
		if anyFailed(lastResults) {
			return &ExitCodeError{Code: 2, Msg: "pre-flight checks failed"}
		}
		return nil
	}

	// --wait mode: poll every 5s until all checks pass or the duration elapses.
	dur, err := time.ParseDuration(selfHostedWait)
	if err != nil {
		return fmt.Errorf("invalid --wait duration %q: %w", selfHostedWait, err)
	}

	deadline := time.After(dur)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		lastResults = runOnce()
		if !anyFailed(lastResults) {
			emitCheckFinal(ctx, sink, lastResults)
			return nil
		}

		select {
		case <-deadline:
			emitCheckFinal(ctx, sink, lastResults)
			return &ExitCodeError{Code: 5, Msg: "wait timeout: checks still failing after " + selfHostedWait}
		case <-ticker.C:
			// continue polling
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func selectCheckRenderer(w io.Writer, wait bool) (progress.EventSink, error) {
	if !wait && !selfHostedJSON && !selfHostedPlain && !selfHostedAccessible && checkWriterIsTTY(w) {
		return progress.NewCheckOneShotRenderer(w, progress.ModelOpts{
			Mode:                progress.ModeCheck,
			Output:              w,
			Cluster:             checkClusterName,
			ControlPlaneContext: selfHostedControlPlaneContext,
			ComputePlaneContext: selfHostedComputePlaneContext,
		}), nil
	}

	sink, _, err := progress.SelectRenderer(w, progress.RenderOpts{
		JSON:                selfHostedJSON,
		Plain:               selfHostedPlain,
		Accessible:          selfHostedAccessible,
		Mode:                progress.ModeCheck,
		Cluster:             checkClusterName,
		ControlPlaneContext: selfHostedControlPlaneContext,
		ComputePlaneContext: selfHostedComputePlaneContext,
	})
	return sink, err
}

// runPreflightByRole dispatches RunPreflightForRole using the role(s) derived
// from the context-flag combination per SRD/SDD §5.4:
//
//   - --local-only or cfg.LocalOnly          → RoleLocalOnly only
//   - ModeSingle (no context flags)           → RoleControlPlane + RoleComputePlane sequentially
//   - ModeSplit  (both context flags set)     → RoleControlPlane + RoleComputePlane in parallel
func runPreflightByRole(ctx context.Context, cfg selfhosted.PreflightConfig, sink progress.EventSink) []selfhosted.CheckResult {
	// LocalOnly: skip all cluster probes.
	if cfg.LocalOnly {
		return selfhosted.RunPreflightForRole(ctx, cfg, selfhosted.RoleLocalOnly, selfhosted.RoleConfig{}, sink)
	}

	icmsURL := ""
	if checkAll || checkComputePlane || !checkPre {
		icmsURL = resolveICMSURL(selfHostedICMSURL)
	}
	mode := kubectx.SelectMode(selfHostedControlPlaneContext, selfHostedComputePlaneContext)

	skipInotify := checkSkipInotifyCheck || os.Getenv("NVCF_CLI_SELFHOSTED_SKIP_INOTIFY") != ""
	var inotifyProber selfhosted.NodeInotifyProber
	if !skipInotify {
		inotifyProber = newInotifyProberForSelfHosted()
	}

	switch mode {
	case kubectx.ModeSplit:
		// Run both roles in parallel; each gets its own kubeconfig context.
		var (
			cpResults  []selfhosted.CheckResult
			gpuResults []selfhosted.CheckResult
		)
		eg, egCtx := errgroup.WithContext(ctx)
		eg.Go(func() error {
			rc := selfhosted.RoleConfig{KubeContext: selfHostedControlPlaneContext}
			cpResults = selfhosted.RunPreflightForRole(egCtx, cfg, selfhosted.RoleControlPlane, rc, sink)
			return nil
		})
		eg.Go(func() error {
			rc := selfhosted.RoleConfig{
				KubeContext:   selfHostedComputePlaneContext,
				SISURL:        icmsURL,
				InotifyProber: inotifyProber,
			}
			gpuResults = selfhosted.RunPreflightForRole(egCtx, cfg, selfhosted.RoleComputePlane, rc, sink)
			return nil
		})
		_ = eg.Wait()
		return append(cpResults, gpuResults...)

	default: // ModeSingle — no context flags; union both role check sets sequentially.
		cpRC := selfhosted.RoleConfig{SISURL: icmsURL}
		gpuRC := selfhosted.RoleConfig{SISURL: icmsURL, InotifyProber: inotifyProber}
		cpResults := selfhosted.RunPreflightForRole(ctx, cfg, selfhosted.RoleControlPlane, cpRC, sink)
		gpuResults := selfhosted.RunPreflightForRole(ctx, cfg, selfhosted.RoleComputePlane, gpuRC, sink)
		return append(cpResults, gpuResults...)
	}
}

// emitCheckFinal emits a Final event with check-mode verdict fields derived
// from the result slice. Called once per run (or once per wait-loop exit).
func emitCheckFinal(ctx context.Context, sink progress.EventSink, results []selfhosted.CheckResult) {
	var passed, failed int
	for _, r := range results {
		if r.Passed {
			passed++
		} else {
			failed++
		}
	}
	verdict := "ok"
	if failed > 0 {
		verdict = "failed"
	}
	_ = sink.Emit(ctx, progress.Final{
		Success:     failed == 0,
		Verdict:     verdict,
		TotalChecks: len(results),
		PassedCount: passed,
		FailedCount: failed,
	})
}

// anyFailed returns true if any check failed at error severity. Warnings do
// not trigger non-zero exit per spec §6.3.
func anyFailed(results []selfhosted.CheckResult) bool {
	for _, r := range results {
		if !r.Passed && r.Severity == "error" {
			return true
		}
	}
	return false
}
