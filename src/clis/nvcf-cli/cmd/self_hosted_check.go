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
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/sync/errgroup"

	"nvcf-cli/internal/selfhosted"
	"nvcf-cli/internal/selfhosted/kubectx"
	"nvcf-cli/internal/selfhosted/progress"
)

var (
	checkPre                        bool
	checkControlPlane               bool
	checkComputePlane               bool
	checkAll                        bool
	checkClusterName                string
	checkLocalOnly                  bool
	checkSkipInotifyCheck           bool
	checkSkipClusterValidation      bool
	checkClusterValidatorImage      string
	checkClusterValidatorPullSecret string
	checkClusterValidatorNoCleanup  bool
	checkClusterValidatorRegistries []string
	checkShowLogs                   bool
)

// Test seam.
var newInotifyProberForSelfHosted = func() selfhosted.NodeInotifyProber {
	return selfhosted.NewInotifyProber()
}

// Test seam.
var newClusterValidatorForSelfHosted = func() selfhosted.ClusterValidator {
	return selfhosted.NewClusterValidator()
}

// Test seam.
var newStaleNamespaceProberForSelfHosted = func() selfhosted.StaleNamespaceProber {
	return selfhosted.NewStaleNamespaceProber()
}

// Test seam. Tests stub this to skip the registry network call.
var resolveLatestValidatorTagForSelfHosted = selfhosted.ResolveLatestValidatorTag

// Test seam.
var newRegistryCredentialCheckerForSelfHosted = func() selfhosted.RegistryCredentialChecker {
	return selfhosted.NewRegistryCredentialChecker()
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
	selfHostedCheckCmd.Flags().BoolVar(&checkComputePlane, "compute-plane", false,
		"Run compute-plane health checks. Requires --cluster-name.")
	selfHostedCheckCmd.Flags().BoolVar(&checkAll, "all", false, "Run all check categories")
	selfHostedCheckCmd.Flags().StringVar(&checkClusterName, "cluster-name", "", "Cluster name for compute-plane checks")
	selfHostedCheckCmd.Flags().BoolVar(&checkLocalOnly, "local-only", false, "Run local-host checks only (no kubectl contact)")
	selfHostedCheckCmd.Flags().BoolVar(&checkSkipInotifyCheck, "skip-inotify-check", false,
		"Disable the per-node inotify-limits probe. Required when the kubeconfig user "+
			"cannot create pods in 'default'. Env: NVCF_CLI_SELFHOSTED_SKIP_INOTIFY")
	selfHostedCheckCmd.Flags().BoolVar(&checkSkipClusterValidation, "skip-cluster-validation", false,
		"Disable the in-cluster cluster-validator probe. "+
			"Env: NVCF_CLI_SELFHOSTED_SKIP_CLUSTER_VALIDATION")
	selfHostedCheckCmd.Flags().StringVar(&checkClusterValidatorImage, "cluster-validator-image", "",
		"Cluster-validator container image. Resolved from --cluster-validator-image > "+
			"NVCF_CLI_CLUSTER_VALIDATOR_IMAGE > nvcf-cli config (cluster_validator_image). "+
			"If unset everywhere, the validator probe is skipped with a warning. "+
			"When the value has no tag, the latest is discovered from the registry.")
	_ = viper.BindPFlag("cluster_validator_image",
		selfHostedCheckCmd.Flags().Lookup("cluster-validator-image"))
	selfHostedCheckCmd.Flags().StringVar(&checkClusterValidatorPullSecret, "cluster-validator-pull-secret", "",
		"Name of a docker-registry Secret in the 'default' namespace to pull the validator image. "+
			"When empty, the runner scans NVCF namespaces for a matching secret (mirroring it into "+
			"'default' if found elsewhere) and falls back to auto-creating one from NGC_API_KEY. "+
			"Set to force a specific name.")
	selfHostedCheckCmd.Flags().BoolVar(&checkClusterValidatorNoCleanup, "no-cleanup", false,
		"Disable the validator Job's TTL so the Job persists for debugging. "+
			"The next run still deletes prior Jobs via the singleton sweep.")
	selfHostedCheckCmd.Flags().StringSliceVar(&checkClusterValidatorRegistries, "cluster-validator-registries", nil,
		"Additional container registries to probe for reachability in the control-plane validator. "+
			"Format: host:port (e.g. harbor.company.internal:443,ghcr.io:443). "+
			"nvcr.io is always included. Env: NVCF_CLI_CLUSTER_VALIDATOR_REGISTRIES. "+
			"Can also be set in nvcf-cli config as cluster_validator_registries (list).")
	_ = viper.BindPFlag("cluster_validator_registries",
		selfHostedCheckCmd.Flags().Lookup("cluster-validator-registries"))
	selfHostedCheckCmd.Flags().BoolVar(&checkShowLogs, "show-logs", false,
		"Print the cleaned cluster-validator transcript to stderr after the check events. "+
			"Useful when piping --json output to a script that also wants the transcript.")
}

func runSelfHostedCheck(c *cobra.Command, _ []string) error {
	if !checkPre && !checkControlPlane && !checkComputePlane && !checkAll {
		return fmt.Errorf("at least one of --pre, --control-plane, --compute-plane, or --all is required")
	}

	localOnly := checkLocalOnly || os.Getenv("NVCF_CLI_SELFHOSTED_LOCAL_ONLY") != ""
	skipClusterValidation := checkSkipClusterValidation || os.Getenv("NVCF_CLI_SELFHOSTED_SKIP_CLUSTER_VALIDATION") != ""

	// Mode is needed before image resolution so computePlaneIsTargeted can
	// gate the registry round trip. ValidateFlags in PersistentPreRunE
	// guarantees mode is ModeSingle or ModeSplit here.
	mode := kubectx.SelectMode(selfHostedControlPlaneContext, selfHostedComputePlaneContext)

	// Resolve the validator image up-front so we can right-size the
	// outer timeout (only when the validator actually runs) and emit a
	// one-shot stderr note up-front explaining why no validator row
	// appears in the output. Empty == not configured anywhere.
	// One image covers both roles (VALIDATOR_ROLE selects the check set).
	// *IsVisited, matching the dispatch below. --pre in ModeSplit visits both
	// clusters but targets neither role, so gating image resolution on
	// *IsTargeted left the validator unresolved and both probes nil, with no
	// note explaining why.
	anyValidatorIsTargeted := !localOnly && !skipClusterValidation &&
		(computePlaneIsVisited(mode) || controlPlaneIsVisited(mode))
	clusterValidatorImage := ""
	if anyValidatorIsTargeted {
		if img, ok := resolveClusterValidatorImage(c.Context()); ok {
			clusterValidatorImage = img
		}
	}
	clusterValidatorWillRun := anyValidatorIsTargeted && clusterValidatorImage != ""

	outerTimeout := 2 * time.Minute
	if clusterValidatorWillRun {
		// Each validator Job has a 5m internal budget. ModeSingle runs both
		// validators sequentially (two 5m runs); ModeSplit runs them in
		// parallel so one 6m ceiling covers both.
		if mode == kubectx.ModeSingle && controlPlaneIsTargeted(mode) && computePlaneIsTargeted(mode) {
			outerTimeout = 12 * time.Minute
		} else {
			outerTimeout = 6 * time.Minute
		}
	}
	// --wait polls for the declared duration. The outer ctx has to outlive
	// the last iteration, so add waitDur on top of a single iteration's
	// worth.
	if selfHostedWait != "" {
		if waitDur, err := time.ParseDuration(selfHostedWait); err == nil {
			outerTimeout += waitDur
		}
	}
	ctx, cancel := context.WithTimeout(c.Context(), outerTimeout)
	defer cancel()

	// Legacy --output=json: warn and treat as --json.
	if selfHostedOutput == "json" && !selfHostedJSON {
		fmt.Fprintln(c.ErrOrStderr(), "warning: --output=json is deprecated; use --json (will be removed in v2)")
		selfHostedJSON = true
	}

	// Surface the skip as a stderr note so operators don't conflate "no
	// validator row" with "validator silently dropped". Print at most one
	// reason; --skip-cluster-validation takes precedence over missing
	// config since it's the explicit operator choice.
	if !localOnly && (computePlaneIsVisited(mode) || controlPlaneIsVisited(mode)) {
		switch {
		case skipClusterValidation:
			fmt.Fprintln(c.ErrOrStderr(), "note: cluster-validator skipped (--skip-cluster-validation)")
		case clusterValidatorImage == "":
			fmt.Fprintln(c.ErrOrStderr(), "note: cluster-validator skipped (cluster_validator_image not set in nvcf-cli config)")
		}
	}

	// Enumerate registries for the local credential check. Skipped when
	// local-only (no network) or when no validator image is configured.
	// Uses the same extras list as the in-cluster ConfigMap reachability check.
	var (
		credEntries     []selfhosted.RegistryEntry
		registryChecker selfhosted.RegistryCredentialChecker
	)
	// Run credential checks whenever not local-only. The validator image is
	// optional: EnumerateRegistries handles an empty image ref and still picks
	// up global.image.registry from the stack values file and any
	// --cluster-validator-registries extras independently of the image config.
	if !localOnly {
		extraRegistries := configuredValidatorRegistries()
		stackValuesFile := resolveStackValuesFile()
		credEntries = selfhosted.EnumerateRegistries(
			clusterValidatorImage, stackValuesFile, extraRegistries,
		)
		if len(credEntries) > 0 {
			registryChecker = newRegistryCredentialCheckerForSelfHosted()
		}
	}

	cfg := selfhosted.PreflightConfig{
		LocalOnly:       localOnly,
		Tools:           selfHostedPreflightTools(),
		Registries:      credEntries,
		RegistryChecker: registryChecker,
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
		if checkPre || checkAll || checkControlPlane || checkComputePlane {
			results = append(results, runPreflightByRole(ctx, cfg, sink, mode, clusterValidatorImage)...)
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
		return results
	}

	if selfHostedWait == "" {
		// Single-shot mode.
		lastResults = runOnce()
		emitCheckFinal(ctx, sink, lastResults)
		maybeShowClusterValidatorLogs(c.ErrOrStderr(), lastResults)
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
			maybeShowClusterValidatorLogs(c.ErrOrStderr(), lastResults)
			return nil
		}

		select {
		case <-deadline:
			emitCheckFinal(ctx, sink, lastResults)
			maybeShowClusterValidatorLogs(c.ErrOrStderr(), lastResults)
			return &ExitCodeError{Code: 5, Msg: "wait timeout: checks still failing after " + selfHostedWait}
		case <-ticker.C:
			// continue polling
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// resolveStackValuesFile returns the environment values YAML to read
// global.image.registry from, or "" when none is available.
//
// It prefers --control-plane-stack, the same source every sibling command uses,
// and only falls back to walking up from the working directory. Within a stack
// it tries HELMFILE_ENV (default "default") and then base.yaml, which is where
// global.image.registry actually lives. The previous version looked only for
// environments/local.yaml, which is not a tracked file, so on any clean
// checkout os.Stat never succeeded and this whole lookup was dead.
func resolveStackValuesFile() string {
	var roots []string
	if selfHostedControlPlaneStack != "" {
		roots = append(roots, selfHostedControlPlaneStack)
	}
	if cwd, err := os.Getwd(); err == nil {
		dir := cwd
		for i := 0; i < 6; i++ {
			roots = append(roots, dir)
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}

	// selfHostedEnv (--env, default "local"), not HELMFILE_ENV: that variable
	// is only ever injected into the helmfile subprocess, so reading it here
	// always returned "" and fell through to base.yaml. An operator whose
	// local.yaml points global.image.registry at their mirror would get
	// base.yaml's nvcr.io, and the preflight would credential-check a registry
	// the install never pulls from.
	env := strings.TrimSpace(selfHostedEnv)
	if env == "" {
		env = "local"
	}
	names := []string{env + ".yaml", "base.yaml"}

	for _, root := range roots {
		for _, sub := range [][]string{
			{"deploy", "stacks", "self-managed", "environments"},
			{"environments"}, // a stack dir passed directly
		} {
			for _, name := range names {
				candidate := filepath.Join(append(append([]string{root}, sub...), name)...)
				if _, err := os.Stat(candidate); err == nil {
					return candidate
				}
			}
		}
	}
	return ""
}

// localStackDir returns src when it points at a readable local directory.
// Remote sources (oci://, git@, https://...git) are not fetched here: the
// stale-namespace check falls back to its static list rather than making
// preflight depend on a network round trip.
func localStackDir(src string) string {
	if src == "" {
		return ""
	}
	src = strings.TrimPrefix(src, "file://")
	if strings.Contains(src, "://") || strings.HasPrefix(src, "git@") {
		return ""
	}
	if fi, err := os.Stat(src); err == nil && fi.IsDir() {
		return src
	}
	return ""
}

// configuredValidatorRegistries returns the extra registries from the flag, env
// var, or config file, normalized to one entry per registry.
//
// viper.GetStringSlice splits a raw env string on whitespace, so the documented
// comma form "a:443,b:443" arrives as a single element. Left as-is it reaches
// net.SplitHostPort as "a:443,b:443", which errors with "too many colons" and is
// then passed through verbatim as a host, producing https://a:443,b:443/v2/.
func configuredValidatorRegistries() []string {
	var out []string
	for _, raw := range viper.GetStringSlice("cluster_validator_registries") {
		for _, part := range strings.Split(raw, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

// computePlaneIsTargeted reports whether the compute-plane validator should run:
// --compute-plane, --all, or --pre in ModeSingle. --pre in ModeSplit does not
// target it because separate clusters have no implicit compute-plane role.
func computePlaneIsTargeted(mode kubectx.Mode) bool {
	return checkComputePlane || checkAll || (checkPre && mode == kubectx.ModeSingle)
}

// controlPlaneIsTargeted mirrors computePlaneIsTargeted but for the control
// plane. Runs when: --control-plane, --all, or --pre in ModeSingle.
func controlPlaneIsTargeted(mode kubectx.Mode) bool {
	return checkControlPlane || checkAll || (checkPre && mode == kubectx.ModeSingle)
}

// The *IsVisited pair reports whether a cluster should be contacted at all,
// which is broader than whether its role-specific check set runs. --pre in
// ModeSplit visits both clusters for the shared pre-install checks (stale
// namespaces) without targeting either role, so the targeting predicates alone
// cannot gate the dispatch. In ModeSingle --pre already targets both roles and
// these are equivalent to their *IsTargeted counterparts.
func computePlaneIsVisited(mode kubectx.Mode) bool {
	return computePlaneIsTargeted(mode) || checkPre
}

func controlPlaneIsVisited(mode kubectx.Mode) bool {
	return controlPlaneIsTargeted(mode) || checkPre
}

// withoutHostLocalChecks returns a copy of cfg with the inputs for the checks
// that run on the operator's machine cleared: local tool versions and registry
// credentials. Neither contacts a cluster, so exactly one role invocation must
// carry them. Without this both roles emit the same check IDs, which
// double-counts them in the pass/fail totals and repeats them in --json.
func withoutHostLocalChecks(cfg selfhosted.PreflightConfig) selfhosted.PreflightConfig {
	cfg.Tools = nil
	cfg.Registries = nil
	cfg.RegistryChecker = nil
	return cfg
}

// maybeShowClusterValidatorLogs prints the cleaned cluster-validator transcript
// to the given writer when --show-logs is set, framed by markers so operators
// can find it in mixed CLI output. Silent no-op when:
//   - --show-logs is not set,
//   - the cluster-validator check did not run (--skip-cluster-validation, or
//     no compute-plane category was selected),
//   - the runner returned no Logs (Job never produced output).
//
// Runs after emitCheckFinal in every exit path so the transcript appears
// after the structured events / table, regardless of which renderer was
// selected.
func maybeShowClusterValidatorLogs(w io.Writer, results []selfhosted.CheckResult) {
	if !checkShowLogs {
		return
	}
	for _, r := range results {
		if r.ID != "cluster-validator" || r.Logs == "" {
			continue
		}
		fmt.Fprintln(w, "--- cluster-validator logs ---")
		fmt.Fprint(w, r.Logs)
		fmt.Fprintln(w, "--- end cluster-validator logs ---")
		return
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
//
// mode is the already-resolved kubectx.Mode (hoisted to the caller so image
// resolution and timeout sizing share the same answer). clusterValidatorImage
// is the already-resolved validator image (empty when not configured).
func runPreflightByRole(ctx context.Context, cfg selfhosted.PreflightConfig, sink progress.EventSink, mode kubectx.Mode, clusterValidatorImage string) []selfhosted.CheckResult {
	// LocalOnly: skip all cluster probes.
	if cfg.LocalOnly {
		return selfhosted.RunPreflightForRole(ctx, cfg, selfhosted.RoleLocalOnly, selfhosted.RoleConfig{}, sink)
	}

	// SIS reachability is a compute-plane concern, and it is skipped pre-install
	// because SIS is not up yet. The previous `!checkPre` form was also true
	// whenever --control-plane was passed, so a control-plane run fired an HTTP
	// request at SIS that the operator never asked for.
	icmsURL := ""
	if computePlaneIsTargeted(mode) && !checkPre {
		icmsURL = resolveICMSURL(selfHostedICMSURL)
	}

	skipInotify := checkSkipInotifyCheck || os.Getenv("NVCF_CLI_SELFHOSTED_SKIP_INOTIFY") != ""
	var inotifyProber selfhosted.NodeInotifyProber
	// Visited, not targeted: the inotify limit is exactly what --pre exists to
	// catch before NVCA bootstrap, so it must run for --pre in ModeSplit too.
	if computePlaneIsVisited(mode) && !skipInotify {
		inotifyProber = newInotifyProberForSelfHosted()
	}

	// clusterValidatorImage is resolved by the caller. Empty value means
	// either the operator explicitly opted out (--skip-cluster-validation /
	// env) or no image is configured (no flag / env / config-file value).
	// Either way, leave clusterValidator nil so the validator row is
	// omitted from the check stream; the caller already emitted a
	// one-line stderr notice explaining which case applies.
	// Gate on the role predicate as well as the image, mirroring
	// cpClusterValidator below. Without this, --control-plane in ModeSplit
	// still creates a ServiceAccount, cluster-wide ClusterRole/CRB, pull secret
	// and validator Job in the compute cluster.
	var clusterValidator selfhosted.ClusterValidator
	if computePlaneIsVisited(mode) && clusterValidatorImage != "" {
		clusterValidator = newClusterValidatorForSelfHosted()
	}

	staleNSProber := newStaleNamespaceProberForSelfHosted()

	// Additional registries to probe in the control-plane validator ConfigMap.
	// Priority: flag > env > config file.
	registries := configuredValidatorRegistries()

	// The cluster-validator image is the same for both roles; VALIDATOR_ROLE
	// in the Job env selects which check set runs inside the binary.
	// Gate on the image, not on the compute-plane validator being constructed:
	// in ModeSplit with --control-plane the compute role is not targeted, so
	// clusterValidator is nil and keying off it would drop the control-plane
	// validator check from the run entirely.
	var cpClusterValidator selfhosted.ClusterValidator
	if controlPlaneIsVisited(mode) && clusterValidatorImage != "" {
		cpClusterValidator = newClusterValidatorForSelfHosted()
	}

	// Local tool versions and registry credentials are checked on the
	// operator's machine, so they belong to one invocation only. The control
	// plane carries them when it runs; otherwise the compute plane does, so
	// neither is silently dropped by a compute-plane-only invocation.
	runControlPlane := controlPlaneIsVisited(mode)
	runComputePlane := computePlaneIsVisited(mode)
	cpCfg, gpuCfg := cfg, cfg
	if runControlPlane {
		gpuCfg = withoutHostLocalChecks(cfg)
	}

	switch mode {
	case kubectx.ModeSplit:
		// Run both roles in parallel; each gets its own kubeconfig context.
		// Each dispatch is gated: with only one role selected the other
		// cluster is never contacted, so its stale-namespace probe cannot
		// report a failure the operator did not ask about.
		var (
			cpResults  []selfhosted.CheckResult
			gpuResults []selfhosted.CheckResult
		)
		eg, egCtx := errgroup.WithContext(ctx)
		if runControlPlane {
			eg.Go(func() error {
				rc := selfhosted.RoleConfig{
					KubeContext:                selfHostedControlPlaneContext,
					ClusterValidator:           cpClusterValidator,
					ClusterValidatorImage:      clusterValidatorImage,
					ClusterValidatorPullSecret: checkClusterValidatorPullSecret,
					ClusterValidatorNoCleanup:  checkClusterValidatorNoCleanup,
					ClusterValidatorRegistries: registries,
					StaleNamespaceProber:       staleNSProber,
					StackDir:                   localStackDir(selfHostedControlPlaneStack),
				}
				cpResults = selfhosted.RunPreflightForRole(egCtx, cpCfg, selfhosted.RoleControlPlane, rc, sink)
				return nil
			})
		}
		if runComputePlane {
			eg.Go(func() error {
				rc := selfhosted.RoleConfig{
					KubeContext:                selfHostedComputePlaneContext,
					SISURL:                     icmsURL,
					InotifyProber:              inotifyProber,
					ClusterValidator:           clusterValidator,
					ClusterValidatorImage:      clusterValidatorImage,
					ClusterValidatorPullSecret: checkClusterValidatorPullSecret,
					ClusterValidatorNoCleanup:  checkClusterValidatorNoCleanup,
					StaleNamespaceProber:       staleNSProber,
					StackDir:                   localStackDir(selfHostedComputePlaneStack),
				}
				gpuResults = selfhosted.RunPreflightForRole(egCtx, gpuCfg, selfhosted.RoleComputePlane, rc, sink)
				return nil
			})
		}
		_ = eg.Wait()
		return append(cpResults, gpuResults...)

	default: // ModeSingle - one cluster; run only the roles the flags target.
		var results []selfhosted.CheckResult

		// The stale-namespace probe goes to exactly one role. Both roles share
		// the cluster here, so handing it to both emits two check_completed
		// events with the same ID and loads the kubeconfig twice, which means a
		// second exec-credential-plugin prompt.
		//
		// The two roles probe disjoint namespace lists, so the skipped role's
		// namespaces are merged into the surviving probe rather than dropped.
		// Without that, a kai-scheduler or nvca-operator namespace wedged
		// Terminating by a failed teardown reports "no stale NVCF namespaces
		// detected" and the next install fails into it.
		staleForControlPlane := staleNSProber
		staleForComputePlane := staleNSProber
		var cpExtraNamespaces, gpuExtraNamespaces []string
		if runControlPlane {
			staleForComputePlane = nil
			cpExtraNamespaces = selfhosted.ComputePlaneStaleNamespaces(
				localStackDir(selfHostedComputePlaneStack))
		} else {
			staleForControlPlane = nil
			gpuExtraNamespaces = selfhosted.ControlPlaneStaleNamespaces(
				localStackDir(selfHostedControlPlaneStack))
		}

		if runControlPlane {
			cpRC := selfhosted.RoleConfig{
				SISURL:                     icmsURL,
				ClusterValidator:           cpClusterValidator,
				ClusterValidatorImage:      clusterValidatorImage,
				ClusterValidatorPullSecret: checkClusterValidatorPullSecret,
				ClusterValidatorNoCleanup:  checkClusterValidatorNoCleanup,
				ClusterValidatorRegistries: registries,
				StaleNamespaceProber:       staleForControlPlane,
				StackDir:                   localStackDir(selfHostedControlPlaneStack),
				ExtraStaleNamespaces:       cpExtraNamespaces,
			}
			results = append(results,
				selfhosted.RunPreflightForRole(ctx, cpCfg, selfhosted.RoleControlPlane, cpRC, sink)...)
		}
		if runComputePlane {
			gpuRC := selfhosted.RoleConfig{
				SISURL:                     icmsURL,
				InotifyProber:              inotifyProber,
				ClusterValidator:           clusterValidator,
				ClusterValidatorImage:      clusterValidatorImage,
				ClusterValidatorPullSecret: checkClusterValidatorPullSecret,
				ClusterValidatorNoCleanup:  checkClusterValidatorNoCleanup,
				StaleNamespaceProber:       staleForComputePlane,
				StackDir:                   localStackDir(selfHostedComputePlaneStack),
				ExtraStaleNamespaces:       gpuExtraNamespaces,
			}
			results = append(results,
				selfhosted.RunPreflightForRole(ctx, gpuCfg, selfhosted.RoleComputePlane, gpuRC, sink)...)
		}
		return results
	}
}

// resolveClusterValidatorImage resolves the validator image from flag > env >
// config-file. Returns ("", false) when unconfigured. When only a repo is
// given, discovers the latest stable tag (1h cached; falls back on failure).
func resolveClusterValidatorImage(ctx context.Context) (string, bool) {
	image := viper.GetString("cluster_validator_image")
	if image == "" {
		return "", false
	}
	if discovered, ok := resolveLatestValidatorTagForSelfHosted(ctx, image); ok {
		return discovered, true
	}
	return image, true
}

// emitCheckFinal emits a Final event with check-mode verdict fields derived
// from the result slice. Called once per run (or once per wait-loop exit).
func emitCheckFinal(ctx context.Context, sink progress.EventSink, results []selfhosted.CheckResult) {
	// Success and FailedCount must agree with the process exit code, which is
	// driven by anyFailed. Counting every non-pass as a failure made a
	// warning-severity result emit success:false / verdict:failed while the
	// process exited 0, so a CI gate on final.success broke for anyone whose
	// registry credentials live in a Docker credential helper. "warnings" is
	// already part of the documented Verdict vocabulary; it was never emitted.
	var passed, failed, warned int
	for _, r := range results {
		switch {
		case r.Passed:
			passed++
		case isBlockingFailure(r):
			failed++
		default:
			warned++
		}
	}
	verdict := "ok"
	switch {
	case failed > 0:
		verdict = "failed"
	case warned > 0:
		verdict = "warnings"
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
// isBlockingFailure is the single definition of "this fails the run". Both the
// exit code and the JSON verdict derive from it, so they cannot disagree.
func isBlockingFailure(r selfhosted.CheckResult) bool {
	return !r.Passed && r.Severity == selfhosted.SeverityError
}

func anyFailed(results []selfhosted.CheckResult) bool {
	for _, r := range results {
		if isBlockingFailure(r) {
			return true
		}
	}
	return false
}
