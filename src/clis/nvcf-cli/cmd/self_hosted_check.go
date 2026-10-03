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
	"cmp"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	"nvcf-cli/internal/selfhosted"
	"nvcf-cli/internal/selfhosted/kubectx"
	"nvcf-cli/internal/selfhosted/progress"
)

var (
	checkPre                         bool
	checkControlPlane                bool
	checkComputePlane                bool
	checkAll                         bool
	checkClusterName                 string
	checkLocalOnly                   bool
	checkSkipInotifyCheck            bool
	checkSkipClusterValidation       bool
	checkClusterValidatorImage       string
	checkClusterValidatorPullSecret  string
	checkClusterValidatorNoCleanup   bool
	checkClusterValidatorRegistries  []string
	checkClusterValidatorTolerations []string
	checkClusterValidatorProbeImage  string
	checkShowLogs                    bool
)

// Test seam.
var newInotifyProberForSelfHosted = func(image string) selfhosted.NodeInotifyProber {
	return selfhosted.NewInotifyProber(image)
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
			"cannot create pods in 'default', or when the probe image comes from a registry that "+
			"needs credentials: the probe pods get no pull secret. Env: NVCF_CLI_SELFHOSTED_SKIP_INOTIFY")
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
			"When empty, the runner scans NVCF namespaces for a matching secret and copies it into "+
			"'default' for the run if it lives elsewhere. Failing that, and only when the image is on "+
			"an NGC registry, it mints one from NGC_API_KEY. Set to force a specific name.")
	selfHostedCheckCmd.Flags().BoolVar(&checkClusterValidatorNoCleanup, "no-cleanup", false,
		"Keep the validator Job, its pod, RBAC, pull secret and ConfigMap for debugging instead of "+
			"removing them after the run. A kept Job whose pod cannot pull its image is suspended, which "+
			"deletes the pod. They are reclaimed by a later check after 24 hours; the result prints the "+
			"kubectl command that removes them now.")
	selfHostedCheckCmd.Flags().StringSliceVar(&checkClusterValidatorRegistries, "cluster-validator-registries", nil,
		"Additional container registries to probe for reachability in the control-plane validator. "+
			"Format: host:port (e.g. harbor.company.internal:443,ghcr.io:443). "+
			"Added to the registries the install pulls from, which are probed with the same "+
			"criticality as the local credential check. Env: NVCF_CLI_CLUSTER_VALIDATOR_REGISTRIES. "+
			"Can also be set in nvcf-cli config as cluster_validator_registries (list).")
	_ = viper.BindPFlag("cluster_validator_registries",
		selfHostedCheckCmd.Flags().Lookup("cluster-validator-registries"))
	selfHostedCheckCmd.Flags().StringSliceVar(&checkClusterValidatorTolerations, "cluster-validator-tolerations", nil,
		"Tolerations added to the validator Job, for clusters whose nodes carry taints other than the "+
			"control-plane ones it always tolerates. Format: key[=value][:effect], effect one of NoSchedule, "+
			"PreferNoSchedule or NoExecute (e.g. dedicated=infra:NoSchedule). Repeatable or comma-separated. "+
			"Env: NVCF_CLI_CLUSTER_VALIDATOR_TOLERATIONS. "+
			"Can also be set in nvcf-cli config as cluster_validator_tolerations (list).")
	_ = viper.BindPFlag("cluster_validator_tolerations",
		selfHostedCheckCmd.Flags().Lookup("cluster-validator-tolerations"))
	selfHostedCheckCmd.Flags().StringVar(&checkClusterValidatorProbeImage, "cluster-validator-probe-image", "",
		"Image for the node inotify probe and the control-plane validator's node-to-node overlay probe "+
			"(needs sh and busybox-style nc). Defaults to busybox:1.36 from Docker Hub; set a mirror for "+
			"air-gapped clusters. "+
			"Env: NVCF_CLI_CLUSTER_VALIDATOR_PROBE_IMAGE. "+
			"Can also be set in nvcf-cli config as cluster_validator_probe_image.")
	_ = viper.BindPFlag("cluster_validator_probe_image",
		selfHostedCheckCmd.Flags().Lookup("cluster-validator-probe-image"))
	selfHostedCheckCmd.Flags().BoolVar(&checkShowLogs, "show-logs", false,
		"Print the cleaned cluster-validator transcript to stderr after the check events. "+
			"Useful when piping --json output to a script that also wants the transcript.")
}

func runSelfHostedCheck(c *cobra.Command, _ []string) error {
	if !checkPre && !checkControlPlane && !checkComputePlane && !checkAll {
		return fmt.Errorf("at least one of --pre, --control-plane, --compute-plane, or --all is required")
	}
	if checkClusterValidatorNoCleanup && selfHostedWait != "" {
		// Each poll is a new validator run, so --no-cleanup would keep a full
		// set of RBAC, Secret, ConfigMap and Job for every poll.
		return fmt.Errorf("--no-cleanup keeps one run's objects for debugging and cannot be combined with --wait")
	}
	if _, err := configuredValidatorTolerations(); err != nil {
		return err
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
		(computePlaneIsVisited() || controlPlaneIsVisited())
	clusterValidatorImage, unresolvedImage := "", ""
	if anyValidatorIsTargeted {
		clusterValidatorImage, unresolvedImage = resolveClusterValidatorImage(c.Context())
	}
	// An image whose tag did not resolve is still sized for: --wait resolves
	// it again on every poll, and the validator runs once it does.
	clusterValidatorConfigured := anyValidatorIsTargeted && (clusterValidatorImage != "" || unresolvedImage != "")

	outerTimeout := checkProbeShare
	if clusterValidatorConfigured {
		// A role's share is the time for the probes that run first, plus the
		// longest a validator can take, which includes the wait after its own
		// timeout for the Job's deadline to end the pod. Sizing on the
		// validator's timeout alone ran the budget out while that wait was
		// still in progress, reporting "not every check ran" in place of the
		// validator's own failure. ModeSingle runs both validators in turn;
		// ModeSplit runs them in parallel, so one share covers both.
		perRole := checkProbeShare + selfhosted.ClusterValidatorRunCeiling()
		if mode == kubectx.ModeSingle && controlPlaneIsTargeted(mode) && computePlaneIsTargeted(mode) {
			outerTimeout = 2 * perRole
		} else {
			outerTimeout = perRole
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
	// Every validator run that may leave objects in a cluster registers its
	// removal command here, across --wait polls, so every exit can print it.
	ledger := &selfhosted.CleanupLedger{}
	errOut := c.ErrOrStderr()

	// Catch Ctrl-C, SIGTERM and SIGHUP (a closed terminal or a dropped SSH
	// session) so an interrupted run unwinds through its deferred cleanup.
	// Without this the process exits on the signal and the validator's
	// cluster-wide ClusterRole, bound to a ServiceAccount in default, stays
	// behind until a later check's orphan sweep.
	sigCtx, stop := signal.NotifyContext(c.Context(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	// After the first signal or quit key, say at once how to remove what the
	// runs in progress may leave, before anything can end the process. A
	// second Ctrl-C then exits at once, through the default handler. SIGTERM
	// and SIGHUP stay caught until the bounded teardown ends: CI follows its
	// SIGINT with a SIGTERM a few seconds later.
	held := make(chan os.Signal, 1)
	defer signal.Stop(held)
	var noteOnce sync.Once
	noteInterrupt := func() {
		noteOnce.Do(func() { printInterruptCleanup(errOut, ledger.Outstanding()) })
	}
	returned := make(chan struct{})
	defer close(returned)
	go func() {
		select {
		case <-returned:
			return
		case <-sigCtx.Done():
		}
		signal.Notify(held, syscall.SIGTERM, syscall.SIGHUP)
		stop()
		noteInterrupt()
	}()
	// interrupted is an explicit cancel, not the timeout below, which is a
	// child of sigCtx and leaves it live.
	interrupted := func() bool { return sigCtx.Err() != nil }
	ctx, cancel := context.WithTimeout(sigCtx, checkBudget(outerTimeout))
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
	// --local-only overrides the required scope flag, so say which checks
	// that drops rather than print a clean host-only result for --all.
	if localOnly {
		fmt.Fprintln(c.ErrOrStderr(), "note: --local-only runs the local host checks only; cluster checks for the requested scope are skipped")
	}
	if !localOnly && (computePlaneIsVisited() || controlPlaneIsVisited()) {
		switch {
		case skipClusterValidation:
			fmt.Fprintln(c.ErrOrStderr(), "note: cluster-validator skipped (--skip-cluster-validation)")
		case unresolvedImage != "":
			// Reported as a failed check row instead: a note on stderr is
			// lost to a consumer of the JSON stream.
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
		// The configured image when its tag did not resolve: its registry,
		// and the repository the credentials must reach, are the same.
		credEntries = selfhosted.EnumerateRegistries(
			cmp.Or(clusterValidatorImage, unresolvedImage), resolveStackValuesFiles(), extraRegistries,
		)
		if len(credEntries) > 0 {
			registryChecker = newRegistryCredentialCheckerForSelfHosted()
		}
	}

	cfg := selfhosted.PreflightConfig{
		LocalOnly:       localOnly,
		Tools:           checkPreflightTools(),
		Registries:      credEntries,
		RegistryChecker: registryChecker,
		Interrupted:     interrupted,
	}

	// A quit key in the dashboard cancels the run the way a signal does.
	sink, err := selectCheckRendererFn(errOut, selfHostedWait != "", stop)
	if err != nil {
		return err
	}
	if starter, ok := sink.(interface{ Start() }); ok {
		starter.Start()
	}
	var lastResults []selfhosted.CheckResult
	runErr := func() error {
		// An interrupted run still ends the stream with a final event, marked
		// cancelled as up marks its own, so a --json consumer is not left waiting
		// for one. Emitted on a fresh ctx: the run's ctx is already cancelled.
		exitInterrupted := func() error {
			// The note goes first, whichever of this and the signal handler
			// gets there first.
			noteInterrupt()
			_ = sink.Emit(context.Background(), progress.Final{Cancelled: true, Cleanup: ledger.Outstanding()})
			return &ExitCodeError{Code: 130, Msg: "interrupted"}
		}

		runOnce := func() []selfhosted.CheckResult {
			var results []selfhosted.CheckResult
			if unresolvedImage != "" {
				// Discovery fails transiently too, and each --wait poll is a new
				// chance to run the validator.
				clusterValidatorImage, unresolvedImage = resolveClusterValidatorImage(ctx)
			}
			if checkPre || checkAll || checkControlPlane || checkComputePlane {
				results = append(results,
					runPreflightByRole(ctx, cfg, sink, mode, clusterValidatorImage, unresolvedImage, ledger)...)
			}
			// Inject force-fail seam for tests.
			if os.Getenv("NVCF_CLI_SELFHOSTED_FORCE_FAIL") != "" {
				results = append([]selfhosted.CheckResult{{
					ID:       "force-fail-test-seam",
					Category: "test",
					Severity: selfhosted.SeverityError,
					Passed:   false,
					Message:  "forced failure (test seam)",
				}}, results...)
			}
			return results
		}

		// The outer budget stopped a check: one never started, or ran out of time
		// while it ran. Its row is no finding, so the verdict is a timeout rather
		// than whatever the partial set would grade as. A budget that ran out
		// only after every check had its result changes nothing.
		exitBudgetSpent := func() error {
			emitCheckTimeout(sink, lastResults, ledger.Outstanding())
			return &ExitCodeError{Code: 5, Msg: "timed out: the check budget ran out before every check finished"}
		}

		if selfHostedWait == "" {
			// Single-shot mode.
			lastResults = runOnce()
			if interrupted() {
				return exitInterrupted()
			}
			if anyCutShort(lastResults) {
				return exitBudgetSpent()
			}
			emitCheckFinal(ctx, sink, lastResults, ledger.Outstanding())
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

		// Every exit 5 reports success:false, so a gate on the final event agrees
		// with the exit code: a run still waiting on a rollout has not passed.
		waitTimeout := func() error {
			emitCheckTimeout(sink, lastResults, ledger.Outstanding())
			if anyFailed(lastResults) {
				return &ExitCodeError{Code: 5, Msg: "wait timeout: checks still failing after " + selfHostedWait}
			}
			return &ExitCodeError{Code: 5, Msg: "wait timeout: a rollout was still in progress after " + selfHostedWait}
		}
		for {
			lastResults = runOnce()
			if interrupted() {
				return exitInterrupted()
			}
			// An iteration the budget cut short has incomplete results: never a
			// pass.
			if anyCutShort(lastResults) {
				return exitBudgetSpent()
			}
			if !anyFailed(lastResults) && !anyWarningToWaitOn(lastResults) {
				emitCheckFinal(ctx, sink, lastResults, ledger.Outstanding())
				return nil
			}

			// The deadline is checked first: with several cases ready, select
			// picks at random, and the ticker then re-ran the checks on a spent
			// budget.
			select {
			case <-deadline:
				return waitTimeout()
			default:
			}
			select {
			case <-deadline:
				return waitTimeout()
			case <-ticker.C:
				// continue polling
			case <-ctx.Done():
				if interrupted() {
					return exitInterrupted()
				}
				return waitTimeout()
			}
		}
	}()
	// Every exit closes the dashboard first, so nothing printed after it is
	// wiped, then prints the transcripts --show-logs asks for and the
	// commands that remove what the runs kept in the cluster.
	_ = sink.Close()
	maybeShowClusterValidatorLogs(errOut, lastResults)
	printKeptValidatorObjects(errOut, ledger.Outstanding())
	return runErr
}

// printInterruptCleanup says, as soon as a run is interrupted, how to remove
// what its validator runs may leave in the cluster if the teardown is cut
// short.
func printInterruptCleanup(w io.Writer, commands []string) {
	if len(commands) == 0 {
		return
	}
	fmt.Fprintln(w, "note: interrupted; stopping the cluster-validator and removing its objects. "+
		"A second Ctrl-C exits at once and leaves them; remove them with:")
	for _, cmd := range commands {
		fmt.Fprintln(w, "  "+cmd)
	}
}

// printKeptValidatorObjects prints, as the command ends, how to remove what
// its validator runs kept in the cluster.
func printKeptValidatorObjects(w io.Writer, commands []string) {
	if len(commands) == 0 {
		return
	}
	fmt.Fprintln(w, "note: the cluster-validator left objects in the cluster; remove them with:")
	for _, cmd := range commands {
		fmt.Fprintln(w, "  "+cmd)
	}
}

// validatorStackValues returns the stack values forwarded to the validator, or
// none unless the install's environment file was read from the stack the
// command points at: a local --control-plane-stack, or with no stack flag the
// checkout found above the working directory. helmfile refuses to install
// without that file, so base.yaml alone does not describe the install. With a
// remote stack, what a walk up from the working directory finds is another
// stack. Another stack's Gateways would have the validator judge Gateways the
// install never created.
func validatorStackValues() selfhosted.StackValues {
	dir := localStackDir(selfHostedControlPlaneStack)
	if selfHostedControlPlaneStack != "" && dir == "" {
		return selfhosted.StackValues{}
	}
	files := resolveStackValuesFiles()
	env := resolveStackEnv() + ".yaml"
	for _, f := range files {
		if filepath.Base(f) == env && (dir == "" || isUnder(dir, f)) {
			return selfhosted.LoadStackValues(files)
		}
	}
	return selfhosted.StackValues{}
}

// isUnder reports whether path is inside dir.
func isUnder(dir, path string) bool {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absDir, absPath)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolveStackValuesFiles returns the stack values files preflight reads, in
// the order helmfile layers them: base.yaml first, then the environment file
// over it. Returns nil when no stack is found.
//
// It prefers --control-plane-stack, the same source every sibling command uses,
// and only falls back to walking up from the working directory. The
// environment follows resolveStackEnv.
func resolveStackValuesFiles() []string {
	var roots []string
	// localStackDir strips file:// and drops remote sources, which
	// filepath.Join would otherwise turn into a path that never exists.
	if dir := localStackDir(selfHostedControlPlaneStack); dir != "" {
		roots = append(roots, dir)
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

	env := resolveStackEnv()
	for _, root := range roots {
		for _, sub := range [][]string{
			{"deploy", "stacks", "self-managed", "environments"},
			{"environments"}, // a stack dir passed directly
		} {
			dir := filepath.Join(append([]string{root}, sub...)...)
			var files []string
			for _, name := range []string{"base.yaml", env + ".yaml"} {
				candidate := filepath.Join(dir, name)
				if _, err := os.Stat(candidate); err == nil {
					files = append(files, candidate)
				}
			}
			if len(files) > 0 {
				return files
			}
		}
	}
	return nil
}

// resolveStackEnv picks the helmfile environment preflight reads: an explicit
// --env, then HELMFILE_ENV for an operator who exports it and runs helmfile
// directly, then --env's default. When the CLI runs helmfile itself it passes
// --env as HELMFILE_ENV, so an explicit flag must win.
func resolveStackEnv() string {
	if f := selfHostedCmd.PersistentFlags().Lookup("env"); f != nil && f.Changed {
		if env := strings.TrimSpace(selfHostedEnv); env != "" {
			return env
		}
	}
	if env := strings.TrimSpace(os.Getenv("HELMFILE_ENV")); env != "" {
		return env
	}
	if env := strings.TrimSpace(selfHostedEnv); env != "" {
		return env
	}
	return "local"
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

// configuredValidatorTolerations parses the validator Job's extra tolerations
// from the flag, env var, or config file. Each is key[=value][:effect]: with
// a value it matches that value, without one any; with no effect it matches
// every effect.
func configuredValidatorTolerations() ([]corev1.Toleration, error) {
	var out []corev1.Toleration
	for _, raw := range viper.GetStringSlice("cluster_validator_tolerations") {
		for _, entry := range strings.Split(raw, ",") {
			if entry = strings.TrimSpace(entry); entry == "" {
				continue
			}
			t, err := parseToleration(entry)
			if err != nil {
				return nil, fmt.Errorf("--cluster-validator-tolerations %q: %w", entry, err)
			}
			out = append(out, t)
		}
	}
	return out, nil
}

var tolerationEffects = map[string]corev1.TaintEffect{
	"noschedule":       corev1.TaintEffectNoSchedule,
	"prefernoschedule": corev1.TaintEffectPreferNoSchedule,
	"noexecute":        corev1.TaintEffectNoExecute,
}

func parseToleration(entry string) (corev1.Toleration, error) {
	t := corev1.Toleration{Operator: corev1.TolerationOpExists}
	if rest, effect, ok := strings.Cut(entry, ":"); ok {
		e, known := tolerationEffects[strings.ToLower(effect)]
		if !known {
			return t, fmt.Errorf("effect must be NoSchedule, PreferNoSchedule or NoExecute, got %q", effect)
		}
		t.Effect, entry = e, rest
	}
	if key, value, ok := strings.Cut(entry, "="); ok {
		t.Operator, t.Value, entry = corev1.TolerationOpEqual, value, key
	}
	if t.Key = entry; t.Key == "" {
		return t, fmt.Errorf("a taint key is required")
	}
	// The apiserver applies the same rules when the Job is created, after the
	// run's RBAC, Secret and ConfigMap already exist.
	if errs := validation.IsQualifiedName(t.Key); len(errs) > 0 {
		return t, fmt.Errorf("invalid taint key %q: %s", t.Key, strings.Join(errs, "; "))
	}
	if errs := validation.IsValidLabelValue(t.Value); len(errs) > 0 {
		return t, fmt.Errorf("invalid taint value %q: %s", t.Value, strings.Join(errs, "; "))
	}
	return t, nil
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
// cannot gate the dispatch.
//
// No mode parameter, unlike *IsTargeted: "(X || (pre && single)) || pre"
// absorbs to "X || pre", so the mode cannot change the answer. Taking one
// would imply a mode-dependence that does not exist.
func computePlaneIsVisited() bool {
	return checkComputePlane || checkAll || checkPre
}

func controlPlaneIsVisited() bool {
	return checkControlPlane || checkAll || checkPre
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
// Runs once the sink has closed, on every exit path, interrupt included, so
// the transcript appears after the structured events or table, whichever
// renderer was selected.
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
		// No early return: both roles produce a cluster-validator result under
		// the same ID, so stopping at the first drops the other transcript
		// entirely from --all --show-logs.
	}
}

// selectCheckRendererFn is a test seam over selectCheckRenderer.
var selectCheckRendererFn = selectCheckRenderer

// Test seams over the renderers selectCheckRenderer picks from.
var (
	selectProgressRenderer  = progress.SelectRenderer
	newCheckOneShotRenderer = func(w io.Writer, opts progress.ModelOpts) progress.EventSink {
		return progress.NewCheckOneShotRenderer(w, opts)
	}
)

func selectCheckRenderer(w io.Writer, wait bool, onQuit func()) (progress.EventSink, error) {
	if !wait && !selfHostedJSON && !selfHostedPlain && !selfHostedAccessible && checkWriterIsTTY(w) {
		return newCheckOneShotRenderer(w, progress.ModelOpts{
			Mode:                progress.ModeCheck,
			Output:              w,
			Cluster:             checkClusterName,
			ControlPlaneContext: selfHostedControlPlaneContext,
			ComputePlaneContext: selfHostedComputePlaneContext,
			OnQuit:              onQuit,
		}), nil
	}

	sink, _, err := selectProgressRenderer(w, progress.RenderOpts{
		JSON:                selfHostedJSON,
		Plain:               selfHostedPlain,
		Accessible:          selfHostedAccessible,
		Mode:                progress.ModeCheck,
		Cluster:             checkClusterName,
		ControlPlaneContext: selfHostedControlPlaneContext,
		ComputePlaneContext: selfHostedComputePlaneContext,
		OnQuit:              onQuit,
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
func runPreflightByRole(
	ctx context.Context, cfg selfhosted.PreflightConfig, sink progress.EventSink, mode kubectx.Mode,
	clusterValidatorImage, unresolvedImage string, ledger *selfhosted.CleanupLedger,
) []selfhosted.CheckResult {
	// LocalOnly: skip all cluster probes.
	if cfg.LocalOnly {
		return selfhosted.RunPreflightForRole(ctx, cfg, selfhosted.RoleLocalOnly, selfhosted.RoleConfig{}, sink)
	}

	// SIS reachability is a compute-plane, post-install concern: only an
	// explicit --all or --compute-plane asks for it. A bare --pre skips it
	// because SIS is not up before install, and --control-plane does not
	// target the compute plane.
	icmsURL := ""
	if checkAll || checkComputePlane {
		icmsURL = resolveICMSURL(selfHostedICMSURL)
	}

	skipInotify := checkSkipInotifyCheck || os.Getenv("NVCF_CLI_SELFHOSTED_SKIP_INOTIFY") != ""
	var inotifyProber selfhosted.NodeInotifyProber
	// Visited, not targeted: the inotify limit is exactly what --pre exists to
	// catch before NVCA bootstrap, so it must run for --pre in ModeSplit too.
	if computePlaneIsVisited() && !skipInotify {
		inotifyProber = newInotifyProberForSelfHosted(configuredProbeImage())
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
	if computePlaneIsVisited() && clusterValidatorImage != "" {
		clusterValidator = newClusterValidatorForSelfHosted()
	}

	staleNSProber := newStaleNamespaceProberForSelfHosted()

	// The control-plane validator probes the same registries, with the same
	// criticality, as the local credential check, extras included.
	registries := cfg.Registries
	validatorEnv := clusterValidatorJobEnv(validatorStackValues())
	// Validated before the run starts.
	validatorTolerations, _ := configuredValidatorTolerations()
	cpValidatorEnv := validatorEnvForRole(validatorEnv, checkControlPlane)
	gpuValidatorEnv := validatorEnvForRole(validatorEnv, checkComputePlane)

	// The cluster-validator image is the same for both roles; VALIDATOR_ROLE
	// in the Job env selects which check set runs inside the binary.
	// Gate on the image, not on the compute-plane validator being constructed:
	// in ModeSplit with --control-plane the compute role is not targeted, so
	// clusterValidator is nil and keying off it would drop the control-plane
	// validator check from the run entirely.
	var cpClusterValidator selfhosted.ClusterValidator
	if controlPlaneIsVisited() && clusterValidatorImage != "" {
		cpClusterValidator = newClusterValidatorForSelfHosted()
	}

	// Local tool versions and registry credentials are checked on the
	// operator's machine, so they belong to one invocation only. The control
	// plane carries them when it runs; otherwise the compute plane does, so
	// neither is silently dropped by a compute-plane-only invocation.
	runControlPlane := controlPlaneIsVisited()
	runComputePlane := computePlaneIsVisited()
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
					KubeContext:                     selfHostedControlPlaneContext,
					ClusterValidator:                cpClusterValidator,
					ClusterValidatorImage:           clusterValidatorImage,
					ClusterValidatorPullSecret:      checkClusterValidatorPullSecret,
					ClusterValidatorNoCleanup:       checkClusterValidatorNoCleanup,
					ClusterValidatorEnv:             cpValidatorEnv,
					ClusterValidatorTolerations:     validatorTolerations,
					ClusterValidatorUnresolvedImage: unresolvedImage,
					ClusterValidatorRegistries:      registries,
					ValidatorCleanup:                ledger,
					StaleNamespaceProber:            staleNSProber,
					StackDir:                        localStackDir(selfHostedControlPlaneStack),
				}
				cpResults = selfhosted.RunPreflightForRole(egCtx, cpCfg, selfhosted.RoleControlPlane, rc, sink)
				return nil
			})
		}
		if runComputePlane {
			eg.Go(func() error {
				rc := selfhosted.RoleConfig{
					KubeContext:                     selfHostedComputePlaneContext,
					SISURL:                          icmsURL,
					InotifyProber:                   inotifyProber,
					ClusterValidator:                clusterValidator,
					ClusterValidatorImage:           clusterValidatorImage,
					ClusterValidatorPullSecret:      checkClusterValidatorPullSecret,
					ClusterValidatorNoCleanup:       checkClusterValidatorNoCleanup,
					ClusterValidatorEnv:             gpuValidatorEnv,
					ClusterValidatorTolerations:     validatorTolerations,
					ClusterValidatorUnresolvedImage: unresolvedImage,
					ValidatorCleanup:                ledger,
					StaleNamespaceProber:            staleNSProber,
					StackDir:                        localStackDir(selfHostedComputePlaneStack),
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
		// Merged only when both roles run: a --control-plane run must not
		// report a stuck kai-scheduler namespace, nor --compute-plane a stuck
		// vault-system, which is the rule ModeSplit already follows.
		staleForControlPlane := staleNSProber
		staleForComputePlane := staleNSProber
		var cpExtraNamespaces []string
		if runControlPlane {
			staleForComputePlane = nil
			if runComputePlane {
				cpExtraNamespaces = selfhosted.ComputePlaneStaleNamespaces(
					localStackDir(selfHostedComputePlaneStack))
			}
		} else {
			staleForControlPlane = nil
		}

		if runControlPlane {
			cpRC := selfhosted.RoleConfig{
				SISURL:                          icmsURL,
				ClusterValidator:                cpClusterValidator,
				ClusterValidatorImage:           clusterValidatorImage,
				ClusterValidatorPullSecret:      checkClusterValidatorPullSecret,
				ClusterValidatorNoCleanup:       checkClusterValidatorNoCleanup,
				ClusterValidatorEnv:             cpValidatorEnv,
				ClusterValidatorTolerations:     validatorTolerations,
				ClusterValidatorUnresolvedImage: unresolvedImage,
				ClusterValidatorRegistries:      registries,
				ValidatorCleanup:                ledger,
				StaleNamespaceProber:            staleForControlPlane,
				StackDir:                        localStackDir(selfHostedControlPlaneStack),
				ExtraStaleNamespaces:            cpExtraNamespaces,
			}
			results = append(results,
				selfhosted.RunPreflightForRole(ctx, cpCfg, selfhosted.RoleControlPlane, cpRC, sink)...)
		}
		if runComputePlane {
			gpuRC := selfhosted.RoleConfig{
				SISURL:                          icmsURL,
				InotifyProber:                   inotifyProber,
				ClusterValidator:                clusterValidator,
				ClusterValidatorImage:           clusterValidatorImage,
				ClusterValidatorPullSecret:      checkClusterValidatorPullSecret,
				ClusterValidatorNoCleanup:       checkClusterValidatorNoCleanup,
				ClusterValidatorEnv:             gpuValidatorEnv,
				ClusterValidatorTolerations:     validatorTolerations,
				ClusterValidatorUnresolvedImage: unresolvedImage,
				ValidatorCleanup:                ledger,
				StaleNamespaceProber:            staleForComputePlane,
				StackDir:                        localStackDir(selfHostedComputePlaneStack),
			}
			results = append(results,
				selfhosted.RunPreflightForRole(ctx, gpuCfg, selfhosted.RoleComputePlane, gpuRC, sink)...)
		}
		return results
	}
}

// validatorEnvForRole returns env for one role's validator. A role whose own
// flag is passed alongside --pre (--pre --control-plane) is checked as
// installed, the way --all is, so its validator is told the run is
// post-install. env itself is not modified.
func validatorEnvForRole(env map[string]string, roleFlag bool) map[string]string {
	if !roleFlag || env["VALIDATOR_POST_INSTALL"] != "" {
		return env
	}
	out := make(map[string]string, len(env)+1)
	for k, v := range env {
		out[k] = v
	}
	out["VALIDATOR_POST_INSTALL"] = "true"
	return out
}

// clusterValidatorJobEnv is what the validator container needs from the CLI's
// resolved configuration, matching what the chart CronJob forwards:
//   - VALIDATOR_POST_INSTALL on every run except a bare --pre, so an empty
//     control plane fails after install instead of passing as pre-install
//     (validatorEnvForRole adds it for a role its own flag targets);
//   - relocated OpenBao and Envoy Gateway namespaces, without which the Tier
//     rows assess the defaults and miss the real components;
//   - NVCF_GATEWAY_NAMES, the override for the NVCF Gateway discovery;
//   - the overlay probe image, so a mirrored cluster does not pull busybox
//     from Docker Hub.
func clusterValidatorJobEnv(stack selfhosted.StackValues) map[string]string {
	env := map[string]string{}
	if !checkPre || checkAll {
		env["VALIDATOR_POST_INSTALL"] = "true"
	}
	if ns := configValue("NVCF_OPENBAO_NAMESPACE"); ns != "" {
		env["NVCF_OPENBAO_NAMESPACE"] = ns
	}
	envoyNS := configValue("NVCF_ENVOY_GATEWAY_NAMESPACE")
	if envoyNS == "" {
		envoyNS = stack.EnvoyGatewayNamespace
	}
	if envoyNS != "" {
		env["NVCF_ENVOY_GATEWAY_NAMESPACE"] = envoyNS
	}
	// The launcher knows the NVCF Gateways from the stack, so the validator
	// need not rediscover them from route labels, and after install can fail
	// a named Gateway that has no proxy. An explicit setting still wins.
	if names := configValue("NVCF_GATEWAY_NAMES"); names != "" {
		env["NVCF_GATEWAY_NAMES"] = names
	} else if len(stack.Gateways) > 0 {
		env["NVCF_GATEWAY_NAMES"] = strings.Join(stack.Gateways, ",")
	}
	if probe := configuredProbeImage(); probe != "" {
		env["NVCF_N2N_PROBE_IMAGE"] = probe
	}
	return env
}

// configuredProbeImage is the busybox-style image the probe pods the check
// launches use, from --cluster-validator-probe-image and its env and config
// key, or else the validator's own NVCF_N2N_PROBE_IMAGE setting. Empty leaves
// each probe on its default.
func configuredProbeImage() string {
	if probe := strings.TrimSpace(viper.GetString("cluster_validator_probe_image")); probe != "" {
		return probe
	}
	return configValue("NVCF_N2N_PROBE_IMAGE")
}

// configValue reads a setting the way the CLI's cluster configuration does
// (environment, then the nvcf-cli config file). A YAML list in the config
// file is joined with commas, the form the validator's list settings parse;
// GetString alone returns "" for a list and would drop it silently.
func configValue(key string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	if !viper.IsSet(key) {
		return ""
	}
	if _, isList := viper.Get(key).([]any); isList {
		var items []string
		for _, item := range viper.GetStringSlice(key) {
			if item = strings.TrimSpace(item); item != "" {
				items = append(items, item)
			}
		}
		return strings.Join(items, ",")
	}
	return strings.TrimSpace(viper.GetString(key))
}

// resolveClusterValidatorImage resolves the validator image from flag > env >
// config-file. Returns ("", false) when unconfigured. When only a repo is
// given, discovers the latest stable tag (1h cached; falls back on failure).
//
// An unpinned image whose tag could not be discovered is not launched: the
// kubelet would pull it as :latest, which the validator repository does not
// publish, and fail the run on a pull error instead of saying why.
func resolveClusterValidatorImage(ctx context.Context) (image string, unresolved string) {
	configured := viper.GetString("cluster_validator_image")
	if configured == "" {
		return "", ""
	}
	if discovered, ok := resolveLatestValidatorTagForSelfHosted(ctx, configured); ok {
		return discovered, ""
	}
	if !selfhosted.ImageRefIsPinned(configured) {
		return "", configured
	}
	return configured, ""
}

// emitCheckFinal emits a Final event with check-mode verdict fields derived
// from the result slice. Called once per run (or once per wait-loop exit).
func emitCheckFinal(ctx context.Context, sink progress.EventSink, results []selfhosted.CheckResult, cleanup []string) {
	// Success and FailedCount must agree with the process exit code, which is
	// driven by anyFailed. Counting every non-pass as a failure made a
	// warning-severity result emit success:false / verdict:failed while the
	// process exited 0, so a CI gate on final.success broke for anyone whose
	// registry credentials live in a Docker credential helper. "warnings" is
	// already part of the documented Verdict vocabulary; it was never emitted.
	passed, failed, warned := selfhosted.CountResults(results)
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
		Cleanup:     cleanup,
	})
}

// emitCheckTimeout emits the final event of a run that ended in a timeout,
// exit 5. Nothing passed the gate, whatever the rows say, so success is false.
func emitCheckTimeout(sink progress.EventSink, results []selfhosted.CheckResult, cleanup []string) {
	passed, failed, _ := selfhosted.CountResults(results)
	_ = sink.Emit(context.Background(), progress.Final{
		Success:     false,
		Verdict:     "timeout",
		TotalChecks: len(results),
		PassedCount: passed,
		FailedCount: failed,
		Cleanup:     cleanup,
	})
}

// anyCutShort reports a check the run's budget stopped before it finished.
func anyCutShort(results []selfhosted.CheckResult) bool {
	for _, r := range results {
		if r.CutShort {
			return true
		}
	}
	return false
}

func isBlockingFailure(r selfhosted.CheckResult) bool {
	return r.IsBlockingFailure()
}

// anyFailed returns true if any check failed at error severity. Warnings do
// not trigger non-zero exit per spec §6.3.
// isBlockingFailure is the single definition of "this fails the run". Both the
// exit code and the JSON verdict derive from it, so they cannot disagree.
// checkPreflightTools is a test seam over the local tool checks, so command
// tests do not depend on what is installed on the machine running them.
var checkPreflightTools = selfHostedPreflightTools

// checkBudget is a test seam over the command's outer time budget.
var checkBudget = func(d time.Duration) time.Duration { return d }

// checkProbeShare is the budget for the checks that run before a validator,
// and the whole budget of a run without one. The slowest of them, the node
// inotify probe, is bounded inside it.
const checkProbeShare = 2 * time.Minute

// anyWarningToWaitOn reports a warning expected to clear by itself, such as a
// rollout the validator saw in progress. --wait keeps polling on it; a single
// run still exits 0.
func anyWarningToWaitOn(results []selfhosted.CheckResult) bool {
	for _, r := range results {
		if r.Transient && !r.Passed {
			return true
		}
	}
	return false
}

func anyFailed(results []selfhosted.CheckResult) bool {
	for _, r := range results {
		if isBlockingFailure(r) {
			return true
		}
	}
	return false
}
