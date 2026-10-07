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
	"net/url"
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
	checkClusterValidatorExternal    []string
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

// Test seam.
var newClusterPullSecretCheckerForSelfHosted = selfhosted.NewClusterPullSecretChecker

var checkWriterIsTTY = isWriterTTY

var selfHostedCheckCmd = &cobra.Command{
	Use:          "check",
	Short:        "Run pre-flight, control-plane, and/or compute-plane health checks",
	RunE:         runSelfHostedCheck,
	SilenceUsage: true,
}

func init() {
	selfHostedCmd.AddCommand(selfHostedCheckCmd)
	selfHostedCheckCmd.Flags().BoolVar(&checkPre, "pre", false,
		"Run pre-flight before an install: local tools (a missing one fails), registry credentials, and "+
			"both roles' cluster checks for a cluster not yet installed, without SIS reachability. With --all "+
			"or a role flag, that role is checked as installed")
	selfHostedCheckCmd.Flags().BoolVar(&checkControlPlane, "control-plane", false,
		"Check the installed control plane")
	selfHostedCheckCmd.Flags().BoolVar(&checkComputePlane, "compute-plane", false,
		"Check the installed compute plane, including SIS reachability at a configured ICMS URL")
	selfHostedCheckCmd.Flags().BoolVar(&checkAll, "all", false,
		"Check the installed control plane and compute plane")
	selfHostedCheckCmd.Flags().StringVar(&checkClusterName, "cluster-name", "",
		"Cluster name shown in the check output header; no check uses it")
	selfHostedCheckCmd.Flags().BoolVar(&checkLocalOnly, "local-only", false,
		"Run local-host checks only (no cluster or registry contact); the skipped checks are reported as "+
			"skipped rows. Env: NVCF_CLI_SELFHOSTED_LOCAL_ONLY=true")
	selfHostedCheckCmd.Flags().BoolVar(&checkSkipInotifyCheck, "skip-inotify-check", false,
		"Disable the per-node inotify-limits probe; its row says it was skipped. Required when the "+
			"kubeconfig user cannot create pods in 'default', or when the probe image comes from a registry "+
			"that needs credentials: the probe pods get no pull secret. Env: NVCF_CLI_SELFHOSTED_SKIP_INOTIFY=true")
	selfHostedCheckCmd.Flags().BoolVar(&checkSkipClusterValidation, "skip-cluster-validation", false,
		"Disable the in-cluster cluster-validator probe; its row says it was skipped. "+
			"Env: NVCF_CLI_SELFHOSTED_SKIP_CLUSTER_VALIDATION=true")
	selfHostedCheckCmd.Flags().StringVar(&checkClusterValidatorImage, "cluster-validator-image", "",
		"Cluster-validator container image. Resolved from --cluster-validator-image > "+
			"NVCF_CLI_CLUSTER_VALIDATOR_IMAGE > nvcf-cli config (cluster_validator_image). "+
			"If unset everywhere, the validator does not run and its row is a warning. "+
			"When the value has no tag, the latest is discovered from the registry; "+
			"if none is found, the validator does not run and its row fails the check.")
	_ = viper.BindPFlag("cluster_validator_image",
		selfHostedCheckCmd.Flags().Lookup("cluster-validator-image"))
	selfHostedCheckCmd.Flags().StringVar(&checkClusterValidatorPullSecret, "cluster-validator-pull-secret", "",
		"Name of a docker-registry Secret in the 'default' namespace to pull the validator image. "+
			"When empty, the runner scans NVCF namespaces for a Secret with the image registry's "+
			"credential and copies that one entry into 'default' for the run. Failing that, it creates "+
			"one from this machine's credential for that registry, the one the registry-credentials "+
			"row checks: the docker login, or NGC_API_KEY for an image on nvcr.io only. "+
			"Set to force a specific name.")
	selfHostedCheckCmd.Flags().BoolVar(&checkClusterValidatorNoCleanup, "no-cleanup", false,
		"Keep the validator Job, its pod, RBAC, pull secret and ConfigMap for debugging instead of "+
			"removing them after the run. A kept Job whose pod cannot pull its image is suspended, which "+
			"deletes the pod. The result prints the kubectl command that removes them now; otherwise a later "+
			"check that runs the validator removes them once they are 24 hours old. Cannot be combined "+
			"with --wait.")
	selfHostedCheckCmd.Flags().StringSliceVar(&checkClusterValidatorRegistries, "cluster-validator-registries", nil,
		"Additional container registries to check: their credentials from this machine, and their "+
			"reachability from a pod of the control-plane validator. Format: host[:port][/path] "+
			"(e.g. harbor.company.internal:443/nvcf,ghcr.io); a path scopes the credential check. "+
			"Added to the registries the install pulls from. The in-pod probes are warnings only. "+
			"A malformed entry fails the command. Env: NVCF_CLI_CLUSTER_VALIDATOR_REGISTRIES. "+
			"Can also be set in nvcf-cli config as cluster_validator_registries, a list of strings in this format.")
	_ = viper.BindPFlag("cluster_validator_registries",
		selfHostedCheckCmd.Flags().Lookup("cluster-validator-registries"))
	selfHostedCheckCmd.Flags().StringSliceVar(&checkClusterValidatorTolerations, "cluster-validator-tolerations", nil,
		"Tolerations added to the validator Job, for clusters whose nodes carry taints other than the "+
			"control-plane ones it always tolerates. Format: key[=value][:effect], effect one of NoSchedule, "+
			"PreferNoSchedule or NoExecute (e.g. dedicated=infra:NoSchedule). Repeatable or comma-separated. "+
			"Env: NVCF_CLI_CLUSTER_VALIDATOR_TOLERATIONS. "+
			"Can also be set in nvcf-cli config as cluster_validator_tolerations, a list of strings in this format.")
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
	selfHostedCheckCmd.Flags().StringSliceVar(&checkClusterValidatorExternal,
		"cluster-validator-external-components", nil,
		"Stack dependencies that run outside the stack, or not at all, so the validator does not look for "+
			"them in the cluster: a subset of nats, openbao and cassandra. Repeatable or comma-separated. "+
			"Defaults to NVCF_EXTERNAL_COMPONENTS, then to the components the stack's environment file "+
			"disables. Env: NVCF_CLI_CLUSTER_VALIDATOR_EXTERNAL_COMPONENTS. "+
			"Can also be set in nvcf-cli config as cluster_validator_external_components, a list of names.")
	_ = viper.BindPFlag("cluster_validator_external_components",
		selfHostedCheckCmd.Flags().Lookup("cluster-validator-external-components"))
	selfHostedCheckCmd.Flags().BoolVar(&checkShowLogs, "show-logs", false,
		"Print each cluster-validator transcript to stderr after the check output, framed with the check "+
			"category it ran for. The transcript is not JSON, so leave this off when a parser reads --json.")
}

func runSelfHostedCheck(c *cobra.Command, _ []string) error {
	// A reader of stdout or stderr that exits early, such as grep -m1, head or
	// a jq that fails on a line, or one a signal to the process group ends,
	// would otherwise have the next write kill the process mid-run, with the
	// validator's cluster-wide RBAC in place. Every write discards its error.
	// It stays ignored after return, for the error cobra prints afterwards.
	signal.Ignore(syscall.SIGPIPE)
	if !checkPre && !checkControlPlane && !checkComputePlane && !checkAll {
		return fmt.Errorf("at least one of --pre, --control-plane, --compute-plane, or --all is required")
	}
	waitDur, err := parseCheckWait(selfHostedWait)
	if err != nil {
		return err
	}
	if checkClusterValidatorNoCleanup && waitDur > 0 {
		// Each poll is a new validator run, so --no-cleanup would keep a full
		// set of RBAC, Secret, ConfigMap and Job for every poll.
		return fmt.Errorf("--no-cleanup keeps one run's objects for debugging and cannot be combined with --wait")
	}
	if _, err := configuredValidatorTolerations(); err != nil {
		return err
	}
	extraRegistries, err := configuredValidatorRegistries()
	if err != nil {
		return err
	}
	if _, err := configuredExternalComponents(); err != nil {
		return err
	}
	for _, name := range []string{envLocalOnly, envSkipClusterValidation, envSkipInotify} {
		if _, err := envToggle(name); err != nil {
			return err
		}
	}
	// One credential lookup per registry for each poll, shared by tag
	// discovery, the local credential row and the validator's pull secret,
	// so the row checks the credential the validator Job is given. Each
	// --wait poll reads them again: a login renewed meanwhile, or a helper
	// that failed once, counts on the next poll.
	creds := selfhosted.NewRegistryCredentials(preferNGCKey())
	runCtx := selfhosted.WithRegistryCredentials(c.Context(), creds)

	localOnly := checkLocalOnly || envToggleOn(envLocalOnly)
	skipClusterValidation := clusterValidationSkipped()

	// ValidateFlags in PersistentPreRunE guarantees mode is ModeSingle or
	// ModeSplit here.
	mode := kubectx.SelectMode(selfHostedControlPlaneContext, selfHostedComputePlaneContext)

	// Resolve the validator image up-front so we can right-size the
	// outer timeout (only when the validator actually runs). Empty == not
	// configured anywhere. One image covers both roles (VALIDATOR_ROLE
	// selects the check set). Gated on the clusters the dispatch below
	// visits, so every visited role that runs a validator has its image.
	anyValidatorRuns := !localOnly && !skipClusterValidation &&
		(computePlaneIsVisited() || controlPlaneIsVisited())
	clusterValidatorImage, unresolvedImage := "", ""
	if anyValidatorRuns {
		clusterValidatorImage, unresolvedImage = resolveClusterValidatorImage(runCtx)
	}
	// An image whose tag did not resolve is still sized for: --wait resolves
	// it again on every poll, and the validator runs once it does.
	clusterValidatorConfigured := anyValidatorRuns && (clusterValidatorImage != "" || unresolvedImage != "")

	// Every validator run that may leave objects in a cluster registers its
	// removal command here, across --wait polls, so every exit can print it.
	ledger := &selfhosted.CleanupLedger{}
	errOut := c.ErrOrStderr()

	// Catch Ctrl-C, SIGTERM and SIGHUP (a closed terminal or a dropped SSH
	// session) so an interrupted run unwinds through its deferred cleanup.
	// Without this the process exits on the signal and the validator's
	// cluster-wide ClusterRole, bound to a ServiceAccount in default, stays
	// behind until a later check's orphan sweep. A signal the process started
	// with ignored stays ignored: nohup ignores SIGHUP so that the run outlives
	// the session, and a shell without job control starts a background job
	// with SIGINT ignored.
	sigCtx, cancelSig := context.WithCancel(runCtx)
	defer cancelSig()
	// Read before Notify: a signal relayed to a channel no longer reads as
	// ignored.
	caughtSignals := notIgnored(os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	heldSignals := notIgnored(syscall.SIGTERM, syscall.SIGHUP)
	caught := make(chan os.Signal, 1)
	notifyOn(caught, caughtSignals)
	defer signal.Stop(caught)
	held := make(chan os.Signal, 1)
	defer signal.Stop(held)
	// interrupt ends the run on the first signal or the quit key. A second
	// Ctrl-C then exits at once, through the default handler. SIGTERM and
	// SIGHUP stay caught until return: CI follows its SIGINT with a SIGTERM a
	// few seconds later.
	var interruptOnce sync.Once
	interrupt := func() {
		interruptOnce.Do(func() {
			notifyOn(held, heldSignals)
			signal.Stop(caught)
			cancelSig()
		})
	}
	// interrupted is an explicit cancel, not the timeout below, which is a
	// child of sigCtx and leaves it live.
	interrupted := func() bool { return sigCtx.Err() != nil }
	ctx, cancel := context.WithTimeout(sigCtx,
		checkBudget(checkRunBudget(mode, localOnly, clusterValidatorConfigured, waitDur)))
	defer cancel()

	// Legacy --output=json: warn and treat as --json.
	if selfHostedOutput == "json" && !selfHostedJSON {
		fmt.Fprintln(c.ErrOrStderr(), "warning: --output=json is deprecated; use --json (will be removed in v2)")
		selfHostedJSON = true
	}

	// Enumerate registries for the local credential check. --local-only
	// skips it and reports a skipped row instead. Uses the same extras list
	// as the in-cluster ConfigMap reachability check.
	var (
		credEntries     []selfhosted.RegistryEntry
		registryChecker selfhosted.RegistryCredentialChecker
	)
	// Run credential checks whenever not local-only. The validator image is
	// optional: EnumerateRegistries handles an empty image ref and still picks
	// up global.image.registry from the stack values and any
	// --cluster-validator-registries extras independently of the image config.
	if !localOnly {
		// The configured image when its tag did not resolve: its registry,
		// and the repository the credentials must reach, are the same.
		credEntries = selfhosted.EnumerateRegistries(
			cmp.Or(clusterValidatorImage, unresolvedImage), registryStackValues(), extraRegistries,
		)
		if len(credEntries) > 0 {
			registryChecker = newRegistryCredentialCheckerForSelfHosted()
		}
	}

	registryPostInstall := !registryTarget().beforeInstall
	var registryClusterSecrets selfhosted.ClusterPullSecretChecker
	if registryChecker != nil && registryPostInstall {
		registryClusterSecrets = newClusterPullSecretCheckerForSelfHosted(visitedKubeContexts(mode))
	}
	cfg := selfhosted.PreflightConfig{
		LocalOnly: localOnly,
		Tools:     checkPreflightTools(),
		// The tools are what `up` runs; checking an installed stack does
		// not use them.
		ToolsAdvisory:          !checkPre,
		Registries:             credEntries,
		RegistryChecker:        registryChecker,
		RegistryPostInstall:    registryPostInstall,
		RegistryClusterSecrets: registryClusterSecrets,
		Interrupted:            interrupted,
	}

	// A quit key in the dashboard cancels the run the way a signal does.
	sink, err := selectCheckRendererFn(errOut, waitDur > 0, interrupt)
	if err != nil {
		return err
	}
	if starter, ok := sink.(interface{ Start() }); ok {
		starter.Start()
	}
	// After the first signal or quit key, say at once how to remove what the
	// runs in progress may leave, before anything can end the process.
	var noteOnce sync.Once
	noteInterrupt := func() {
		interrupt()
		noteOnce.Do(func() {
			commands := ledger.Outstanding()
			if len(commands) == 0 {
				return
			}
			// A dashboard on the alternate screen takes everything printed
			// meanwhile with it when it closes. The run is ending, so it is
			// closed first and the note stays on the operator's terminal.
			if owner, ok := sink.(interface{ OwnsTerminal() bool }); ok && owner.OwnsTerminal() {
				_ = sink.Close()
			}
			printInterruptCleanup(errOut, commands)
		})
	}
	returned := make(chan struct{})
	defer close(returned)
	go func() {
		select {
		case <-returned:
			return
		case <-caught:
		case <-sigCtx.Done():
		}
		noteInterrupt()
	}()
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

		// One inotify prober for every --wait poll: it keeps what it learned
		// of each node, so a poll gets further than the one before.
		inotifyProber := sync.OnceValue(func() selfhosted.NodeInotifyProber {
			return newInotifyProberForSelfHosted(configuredProbeImage())
		})
		runOnce := func() []selfhosted.CheckResult {
			// The first poll keeps the lookups the image resolution above made.
			pollCtx := selfhosted.WithRegistryCredentials(ctx, creds)
			creds = selfhosted.NewRegistryCredentials(preferNGCKey())
			var results []selfhosted.CheckResult
			if unresolvedImage != "" {
				// Discovery fails transiently too, and each --wait poll is a new
				// chance to run the validator.
				clusterValidatorImage, unresolvedImage = resolveClusterValidatorImage(pollCtx)
			}
			if checkPre || checkAll || checkControlPlane || checkComputePlane {
				results = append(results,
					runPreflightByRole(pollCtx, cfg, sink, mode, clusterValidatorImage, unresolvedImage, ledger,
						inotifyProber)...)
			}
			return results
		}

		exitFailed := func() error {
			emitCheckFinal(context.Background(), sink, lastResults, ledger.Outstanding())
			return &ExitCodeError{Code: 2, Msg: "pre-flight checks failed"}
		}
		// A budget stopped a check: one never started, or ran out of time while
		// it ran. Its row is no finding, so when it blocks the verdict is a
		// timeout rather than whatever the partial set would grade as. A budget
		// that ran out only after every check had its result changes nothing. A
		// blocking failure in a row the budget did not cut short is still exit 2:
		// no retry with more time passes it.
		exitBudgetSpent := func() error {
			if anyFindingFailed(lastResults) {
				return exitFailed()
			}
			emitCheckTimeout(sink, lastResults, ledger.Outstanding())
			return &ExitCodeError{Code: 5, Msg: "timed out: the check budget ran out before every check finished"}
		}

		if waitDur == 0 {
			// Single-shot mode.
			lastResults = runOnce()
			if interrupted() {
				return exitInterrupted()
			}
			if anyCutShortFailure(lastResults) {
				return exitBudgetSpent()
			}
			if anyFailed(lastResults) {
				return exitFailed()
			}
			emitCheckFinal(ctx, sink, lastResults, ledger.Outstanding())
			return nil
		}

		// --wait mode: poll until all checks pass or the duration elapses.
		deadline := time.After(waitDur)
		ticker := time.NewTicker(checkPollInterval)
		defer ticker.Stop()

		// Every exit 5 reports success:false, so a gate on the final event agrees
		// with the exit code: a run still waiting on a rollout has not passed.
		waitTimeout := func() error {
			emitCheckTimeout(sink, lastResults, ledger.Outstanding())
			switch {
			case anyFindingFailed(lastResults):
				return &ExitCodeError{Code: 5, Msg: "wait timeout: checks still failing after " + selfHostedWait}
			case anyCutShort(lastResults):
				return &ExitCodeError{Code: 5, Msg: "wait timeout: a check was still cut short by its time budget after " +
					selfHostedWait}
			}
			return &ExitCodeError{Code: 5, Msg: "wait timeout: a rollout was still in progress after " + selfHostedWait}
		}
		var previous []selfhosted.CheckResult
		for {
			lastResults = runOnce()
			if interrupted() {
				return exitInterrupted()
			}
			// A poll a budget cut short has incomplete results: never a pass. The
			// next poll gets fresh shares, so only the spent outer budget ends
			// the wait here.
			if anyCutShort(lastResults) && ctx.Err() != nil {
				return exitBudgetSpent()
			}
			if !anyFailed(lastResults) && !anyWarningToWaitOn(lastResults, previous) {
				emitCheckFinal(ctx, sink, lastResults, ledger.Outstanding())
				return nil
			}
			previous = lastResults

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

// notIgnored returns the signals in sigs that the process does not ignore.
func notIgnored(sigs ...os.Signal) []os.Signal {
	var out []os.Signal
	for _, sig := range sigs {
		if !signal.Ignored(sig) {
			out = append(out, sig)
		}
	}
	return out
}

// notifyOn relays sigs to ch. With none it relays nothing, where
// signal.Notify with no signals relays every one.
func notifyOn(ch chan<- os.Signal, sigs []os.Signal) {
	if len(sigs) > 0 {
		signal.Notify(ch, sigs...)
	}
}

// printInterruptCleanup says, as soon as a run is interrupted, how to remove
// what its validator runs may leave in the cluster if the teardown is cut
// short. A failed write is ignored: the teardown goes on whether or not
// anything still reads stderr.
func printInterruptCleanup(w io.Writer, commands []string) {
	if len(commands) == 0 {
		return
	}
	var note strings.Builder
	note.WriteString("note: interrupted; stopping the cluster-validator and removing its objects. " +
		"A second Ctrl-C exits at once and leaves them; remove them with:\n")
	for _, cmd := range commands {
		note.WriteString("  " + cmd + "\n")
	}
	_, _ = io.WriteString(w, note.String())
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

// validatorStackValues returns the control-plane stack values forwarded to
// the validator, or none when stackValuesForRun cannot read the values that
// describe the install. Another stack's Gateways would have the validator
// judge Gateways the install never created.
func validatorStackValues() selfhosted.StackValues {
	files, ok := stackValuesForRun(controlPlaneStackTarget())
	if !ok {
		return selfhosted.StackValues{}
	}
	return selfhosted.LoadStackValues(files)
}

// registryStackValues returns the stack values the registry credential check
// reads, from registryTarget's stack. Empty when they cannot be read.
func registryStackValues() selfhosted.StackValues {
	files, ok := stackValuesForRun(registryTarget())
	if !ok {
		return selfhosted.StackValues{}
	}
	return selfhosted.LoadStackValues(files)
}

// registryTarget is the plane the registry credential check belongs to: the
// stack its registries come from, and the install state it is graded by. That
// is a visited plane checked before its install, whose install this machine
// runs next, with helm pulling the stack's charts on its credentials; else
// the control plane when visited, else the compute plane.
func registryTarget() stackTarget {
	cp, gpu := controlPlaneStackTarget(), computePlaneStackTarget()
	switch {
	case controlPlaneIsVisited() && cp.beforeInstall:
		return cp
	case computePlaneIsVisited() && gpu.beforeInstall:
		return gpu
	case controlPlaneIsVisited():
		return cp
	}
	return gpu
}

// visitedKubeContexts returns the kubeconfig context of each plane the run
// visits: the current context, "", for one cluster.
func visitedKubeContexts(mode kubectx.Mode) []string {
	if mode != kubectx.ModeSplit {
		return []string{""}
	}
	var out []string
	if controlPlaneIsVisited() {
		out = append(out, selfHostedControlPlaneContext)
	}
	if computePlaneIsVisited() {
		out = append(out, selfHostedComputePlaneContext)
	}
	return out
}

// stackTarget is one stack check can read values from.
type stackTarget struct {
	// source is the stack flag; builtIn the stack the install commands use
	// without it.
	source, builtIn string
	// name is the stack's directory in a checkout, under deploy/stacks.
	name string
	// beforeInstall is set when the run checks the plane before its install.
	beforeInstall bool
}

func controlPlaneStackTarget() stackTarget {
	return stackTarget{source: selfHostedControlPlaneStack, builtIn: builtInControlPlaneStackOCI(),
		name: "self-managed", beforeInstall: checkedBeforeInstall(checkControlPlane)}
}

func computePlaneStackTarget() stackTarget {
	return stackTarget{source: selfHostedComputePlaneStack, builtIn: builtInComputePlaneStackOCI(),
		name: "nvcf-compute-plane", beforeInstall: checkedBeforeInstall(checkComputePlane)}
}

// checkedBeforeInstall reports whether a plane is checked before its install:
// --pre checks it so, unless --all or the plane's own flag checks it as
// installed.
func checkedBeforeInstall(planeFlag bool) bool {
	return checkPre && !checkAll && !planeFlag
}

// stackValuesForRun returns the values files that describe the install, in
// the order helmfile layers them (base.yaml, then <env>.yaml), and true only
// when it can read them from the stack the install used:
//
//   - The environment must be named, with --env or HELMFILE_ENV. --env's
//     default is not evidence of the environment an install used. Before
//     the install it is: up installs with it.
//   - <env>.yaml must be read, since helmfile refuses to install without it,
//     so base.yaml alone does not describe the install.
//   - It is read from the stack the install commands resolve: the stack
//     flag, else the built-in stack. A local directory is read directly, and
//     an oci:// stack from the extraction an earlier command left in the
//     cache. A git stack, file:// included, is not read: the install cloned
//     it, so the working tree here can differ. With neither a flag nor a
//     built-in stack, the stack checkout above the working directory is read.
//
// Otherwise nothing is read, and every stack-derived input is left out.
func stackValuesForRun(t stackTarget) ([]string, bool) {
	env, ok := stackEnvForRun(t)
	if !ok {
		return nil, false
	}
	dir := stackDirForRun(t)
	if dir == "" {
		return nil, false
	}
	envFile := filepath.Join(dir, "environments", env+".yaml")
	if !isRegularFile(envFile) {
		return nil, false
	}
	var files []string
	if base := filepath.Join(dir, "environments", "base.yaml"); isRegularFile(base) {
		files = append(files, base)
	}
	return append(files, envFile), true
}

// stackEnvForRun returns the environment the install of t's plane used, and
// false when it is not known: the one the operator named, or, before the
// install, the one up installs with.
func stackEnvForRun(t stackTarget) (string, bool) {
	if env, ok := explicitStackEnv(); ok {
		return env, true
	}
	if t.beforeInstall {
		return resolveStackEnv(), true
	}
	return "", false
}

// stackDirForRun returns the root of the stack the install commands resolve
// for t, or "" when it cannot be read here: the stack flag, else the built-in
// stack, as a local directory or an extracted oci:// stack; with neither, the
// first stack checkout at or above the working directory with an
// environments directory.
func stackDirForRun(t stackTarget) string {
	if src := cmp.Or(t.source, t.builtIn); src != "" {
		return cmp.Or(localStackDir(src), selfhosted.ExtractedOCIStack(src))
	}
	for _, dir := range checkoutStackDirs(t.name) {
		if fi, err := os.Stat(filepath.Join(dir, "environments")); err == nil && fi.IsDir() {
			return dir
		}
	}
	return ""
}

// namespaceGateStack returns the stack directory and environment a plane's
// stale-namespace scan reads the gates of the stack's releases from. env is
// empty when the install's environment is not known.
func namespaceGateStack(t stackTarget) (dir, env string) {
	env, _ = stackEnvForRun(t)
	return stackDirForRun(t), env
}

// checkoutStackDirs lists where a stack can sit at or above the working
// directory: deploy/stacks/<name> in a repository checkout, or the stack
// directory itself.
func checkoutStackDirs(name string) []string {
	cwd, err := os.Getwd()
	if err != nil {
		return nil
	}
	var dirs []string
	for i, dir := 0, cwd; i < 6; i++ {
		if filepath.Base(dir) == name {
			dirs = append(dirs, dir)
		}
		dirs = append(dirs, filepath.Join(dir, "deploy", "stacks", name))
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return dirs
}

func isRegularFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}

// explicitStackEnv returns the helmfile environment the operator named: an
// explicit --env, then HELMFILE_ENV for an operator who exports it and runs
// helmfile directly. When the CLI runs helmfile itself it passes --env as
// HELMFILE_ENV, so an explicit flag must win.
func explicitStackEnv() (string, bool) {
	if f := selfHostedCmd.PersistentFlags().Lookup("env"); f != nil && f.Changed {
		if env := strings.TrimSpace(selfHostedEnv); env != "" {
			return env, true
		}
	}
	if env := strings.TrimSpace(os.Getenv("HELMFILE_ENV")); env != "" {
		return env, true
	}
	return "", false
}

// resolveStackEnv is the helmfile environment the run assumes: the one the
// operator named, else --env's default.
func resolveStackEnv() string {
	if env, ok := explicitStackEnv(); ok {
		return env
	}
	if env := strings.TrimSpace(selfHostedEnv); env != "" {
		return env
	}
	return "local"
}

// preferNGCKey reports whether the NGC API key goes ahead of the docker login
// for nvcr.io: only where up mints its pull secrets from the key, when the
// registry check's plane is the control plane before a local install.
// compute-plane install, which installs the compute plane next, mints none,
// and its helm pulls with the docker login. Anywhere else the docker login is
// what docker uses, and the key is sent only for a scope where there is no
// login or nvcr.io refuses it.
func preferNGCKey() bool {
	t := registryTarget()
	return t.name == controlPlaneStackTarget().name && t.beforeInstall &&
		strings.EqualFold(resolveStackEnv(), "local")
}

// localStackDir returns src when it points at a readable local directory.
// Remote sources (oci://, git@, https://...git, file://) are not fetched
// here, so no check depends on a network round trip. file:// is a git source:
// the install clones its committed HEAD, which the working tree can differ
// from.
func localStackDir(src string) string {
	if src == "" || strings.Contains(src, "://") || strings.HasPrefix(src, "git@") {
		return ""
	}
	if fi, err := os.Stat(src); err == nil && fi.IsDir() {
		return src
	}
	return ""
}

// configList returns a list setting from its flag, env var or config file.
// viper.GetStringSlice reads a config-file entry that is a map or a list,
// such as a Kubernetes-style toleration, as "", and a value that is a single
// map as no entries at all, which would drop it without a word, so either is
// an error naming it.
func configList(key string) ([]string, error) {
	var raw []any
	switch v := viper.Get(key).(type) {
	case nil, string, []string:
		return viper.GetStringSlice(key), nil
	case []any:
		raw = v
	default:
		return nil, fmt.Errorf("%s is not a list of strings: %v", key, v)
	}
	out := make([]string, 0, len(raw))
	for i, item := range raw {
		switch v := item.(type) {
		case nil:
		case string, bool, int, int64, uint64, float64:
			out = append(out, fmt.Sprint(v))
		default:
			return nil, fmt.Errorf("%s entry %d is not a string: %v", key, i+1, v)
		}
	}
	return out, nil
}

// configuredValidatorRegistries parses the extra registries from the flag,
// env var, or config file, one entry per registry. A malformed entry is an
// error naming it, as a malformed toleration is.
//
// viper.GetStringSlice splits a raw env string on whitespace, so the documented
// comma form "a:443,b:443" arrives as a single element and is split here.
func configuredValidatorRegistries() ([]selfhosted.RegistryEntry, error) {
	raws, err := configList("cluster_validator_registries")
	if err != nil {
		return nil, fmt.Errorf("%w; write each registry as host[:port][/path]", err)
	}
	var out []selfhosted.RegistryEntry
	for _, raw := range raws {
		for _, part := range strings.Split(raw, ",") {
			if part = strings.TrimSpace(part); part == "" {
				continue
			}
			entry, err := selfhosted.ParseRegistryExtra(part)
			if err != nil {
				return nil, fmt.Errorf("--cluster-validator-registries %w", err)
			}
			out = append(out, entry)
		}
	}
	return out, nil
}

// configuredValidatorTolerations parses the validator Job's extra tolerations
// from the flag, env var, or config file. Each is key[=value][:effect]: with
// a value it matches that value, without one any; with no effect it matches
// every effect.
func configuredValidatorTolerations() ([]corev1.Toleration, error) {
	raws, err := configList("cluster_validator_tolerations")
	if err != nil {
		return nil, fmt.Errorf("%w; write each toleration as key[=value][:effect]", err)
	}
	var out []corev1.Toleration
	for _, raw := range raws {
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

// The *IsVisited pair reports whether a role's checks run: its own flag,
// --all, or --pre, which checks both roles before an install in either mode.
// They gate the dispatch, the validators, the inotify probe and the budget.
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

// maybeShowClusterValidatorLogs prints each cleaned cluster-validator
// transcript to w when --show-logs is set, framed by markers that name the
// check category it ran for, so operators can find it in mixed CLI output and
// tell the two roles apart: both report under the same check ID, and a
// validator image that predates roles prints no role line of its own. Nothing
// is printed for a validator that did not run (skipped, no image, a role not
// visited) or produced no output.
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
		fmt.Fprintf(w, "--- cluster-validator logs (%s) ---\n", r.Category)
		fmt.Fprint(w, r.Logs)
		fmt.Fprintf(w, "--- end cluster-validator logs (%s) ---\n", r.Category)
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

// runPreflightByRole dispatches RunPreflightForRole for the roles the scope
// flags visit (see the *IsVisited pair):
//
//   - --local-only (cfg.LocalOnly): RoleLocalOnly only, with a skipped row for
//     each cluster check left out;
//   - ModeSingle (no context flags): the visited roles in turn, on one cluster;
//   - ModeSplit (both context flags): the visited roles in parallel, each on
//     its own context.
//
// mode is the already-resolved kubectx.Mode (hoisted to the caller so image
// resolution and timeout sizing share the same answer). clusterValidatorImage
// is the already-resolved validator image (empty when not configured).
// sharedInotifyProber returns the inotify prober every poll shares.
func runPreflightByRole(
	ctx context.Context, cfg selfhosted.PreflightConfig, sink progress.EventSink, mode kubectx.Mode,
	clusterValidatorImage, unresolvedImage string, ledger *selfhosted.CleanupLedger,
	sharedInotifyProber func() selfhosted.NodeInotifyProber,
) []selfhosted.CheckResult {
	// LocalOnly: skip all cluster probes, and say so in a row for each.
	if cfg.LocalOnly {
		return checkRunRole(ctx, cfg, selfhosted.RoleLocalOnly,
			selfhosted.RoleConfig{Skipped: localOnlySkips()}, sink)
	}

	// SIS reachability is a compute-plane, post-install concern: only an
	// explicit --all or --compute-plane asks for it. A bare --pre skips it
	// because SIS is not up before install, and --control-plane does not
	// target the compute plane. It probes only a URL the operator configured.
	icmsURL := ""
	var computeSkips []selfhosted.SkippedCheck
	if checkAll || checkComputePlane {
		var unset string
		if icmsURL, unset = resolveCheckSISURL(selfHostedICMSURL); icmsURL == "" {
			computeSkips = append(computeSkips, selfhosted.SkippedCheck{
				Category: selfhosted.CategoryComputePlane, ID: "sis-reachability", Warn: true,
				Message: "SIS reachability not checked: " + unset + "; pass --icms-url or set NVCF_ICMS_URL",
			})
		}
	}

	var inotifyProber selfhosted.NodeInotifyProber
	// Visited, not targeted: the inotify limit is exactly what --pre exists to
	// catch before NVCA bootstrap, so it must run for --pre in ModeSplit too.
	if computePlaneIsVisited() {
		if inotifyCheckSkipped() {
			computeSkips = append(computeSkips, selfhosted.SkippedCheck{
				Category: selfhosted.CategoryComputePlane, ID: "node-inotify-limits",
				Message: "node inotify limits skipped (--skip-inotify-check)",
			})
		} else {
			inotifyProber = sharedInotifyProber()
		}
	}
	computeSkips = append(computeSkips,
		validatorSkip(selfhosted.CategoryComputePlane, clusterValidatorImage, unresolvedImage)...)
	cpSkips := validatorSkip(selfhosted.CategoryControlPlane, clusterValidatorImage, unresolvedImage)

	// clusterValidatorImage is resolved by the caller. Empty value means
	// either the operator explicitly opted out (--skip-cluster-validation /
	// env) or no image is configured (no flag / env / config-file value).
	// Either way the validator is nil and the role's Skipped row says which.
	// Gate on the role predicate as well as the image, mirroring
	// cpClusterValidator below. Without this, --control-plane in ModeSplit
	// still creates a ServiceAccount, cluster-wide ClusterRole/CRB, pull secret
	// and validator Job in the compute cluster.
	var clusterValidator selfhosted.ClusterValidator
	if computePlaneIsVisited() && clusterValidatorImage != "" {
		clusterValidator = newClusterValidatorForSelfHosted()
	}

	staleNSProber := newStaleNamespaceProberForSelfHosted()
	// Each plane's stale-namespace scan reads the gates of its stack's
	// releases from the stack and environment the other stack values come
	// from. Without a known environment the stack's base.yaml defaults decide.
	cpStackDir, cpStackEnv := namespaceGateStack(controlPlaneStackTarget())
	gpuStackDir, gpuStackEnv := namespaceGateStack(computePlaneStackTarget())

	// The control-plane validator probes the hosts the local credential check
	// probes, extras included, each as a warning only, but from the
	// control-plane stack where that check reads the compute plane's.
	registries := cfg.Registries
	if len(registries) > 0 && registryTarget().name != controlPlaneStackTarget().name {
		cpStack := selfhosted.StackValues{}
		if files, ok := stackValuesForRun(controlPlaneStackTarget()); ok {
			cpStack = selfhosted.LoadStackValues(files)
		}
		// Validated before the run starts.
		extras, _ := configuredValidatorRegistries()
		registries = selfhosted.EnumerateRegistries(cmp.Or(clusterValidatorImage, unresolvedImage), cpStack, extras)
	}
	validatorEnv := clusterValidatorJobEnv(validatorStackValues())
	// Validated before the run starts.
	validatorTolerations, _ := configuredValidatorTolerations()
	cpValidatorEnv := validatorEnvForRole(validatorEnv, controlPlaneStackTarget().beforeInstall)
	gpuValidatorEnv := validatorEnvForRole(validatorEnv, computePlaneStackTarget().beforeInstall)

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
					StackDir:                        cpStackDir,
					Skipped:                         cpSkips,
					StackEnv:                        cpStackEnv,
				}
				cpResults = checkRunRole(egCtx, cpCfg, selfhosted.RoleControlPlane, rc, sink)
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
					StackDir:                        gpuStackDir,
					Skipped:                         computeSkips,
					StackEnv:                        gpuStackEnv,
				}
				gpuResults = checkRunRole(egCtx, gpuCfg, selfhosted.RoleComputePlane, rc, sink)
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
				cpExtraNamespaces = selfhosted.ComputePlaneStaleNamespaces(gpuStackDir, gpuStackEnv)
			}
		} else {
			staleForControlPlane = nil
		}

		if runControlPlane {
			cpRC := selfhosted.RoleConfig{
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
				StackDir:                        cpStackDir,
				StackEnv:                        cpStackEnv,
				ExtraStaleNamespaces:            cpExtraNamespaces,
				Skipped:                         cpSkips,
			}
			results = append(results,
				checkRunRole(ctx, cpCfg, selfhosted.RoleControlPlane, cpRC, sink)...)
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
				StackDir:                        gpuStackDir,
				Skipped:                         computeSkips,
				StackEnv:                        gpuStackEnv,
			}
			results = append(results,
				checkRunRole(ctx, gpuCfg, selfhosted.RoleComputePlane, gpuRC, sink)...)
		}
		return results
	}
}

// validatorEnvForRole returns env for the validator of a role whose plane is
// checked before its install or not, as checkedBeforeInstall decides. One
// checked as installed is told the run is post-install, so an empty control
// plane fails instead of passing as pre-install. env itself is not modified.
func validatorEnvForRole(env map[string]string, beforeInstall bool) map[string]string {
	if beforeInstall {
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
// resolved configuration, matching what the chart CronJob forwards
// (validatorEnvForRole adds VALIDATOR_POST_INSTALL for each role):
//   - relocated OpenBao and Envoy Gateway namespaces, without which the Tier
//     rows assess the defaults and miss the real components;
//   - NVCF_GATEWAY_NAMES, the override for the NVCF Gateway discovery;
//   - the overlay probe image, so a mirrored cluster does not pull busybox
//     from Docker Hub;
//   - NVCF_EXTERNAL_COMPONENTS, the dependencies that run outside the stack,
//     so the validator does not fail them for being absent from the cluster;
//   - NVCF_STORAGE_CLASS, the class the stack's PVCs name, so a cluster with
//     no default class is not failed for it.
func clusterValidatorJobEnv(stack selfhosted.StackValues) map[string]string {
	env := map[string]string{}
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
	// Validated before the run starts. An explicit setting wins over the
	// components the stack leaves out.
	external, _ := configuredExternalComponents()
	if external == nil {
		external = stack.ExternalComponents
	}
	if len(external) > 0 {
		env["NVCF_EXTERNAL_COMPONENTS"] = strings.Join(external, ",")
	}
	if class := cmp.Or(configValue("NVCF_STORAGE_CLASS"), stack.StorageClass); class != "" {
		env["NVCF_STORAGE_CLASS"] = class
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

// configuredExternalComponents returns the components the operator says run
// outside the stack: --cluster-validator-external-components (or its env and
// config key), then NVCF_EXTERNAL_COMPONENTS. nil means neither is set, and
// the stack values decide.
func configuredExternalComponents() ([]string, error) {
	raw, err := configList("cluster_validator_external_components")
	if err != nil {
		return nil, fmt.Errorf("%w; write each component as its name", err)
	}
	if len(raw) == 0 {
		raw = []string{configValue("NVCF_EXTERNAL_COMPONENTS")}
	}
	out, err := selfhosted.ParseExternalComponents(raw)
	if err != nil {
		return nil, fmt.Errorf("--cluster-validator-external-components: %w", err)
	}
	return out, nil
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
		Success:      failed == 0,
		Verdict:      verdict,
		TotalChecks:  len(results),
		PassedCount:  passed,
		FailedCount:  failed,
		WarningCount: warned,
		Cleanup:      cleanup,
	})
}

// emitCheckTimeout emits the final event of a run that ended in a timeout,
// exit 5. Nothing passed the gate, whatever the rows say, so success is false.
func emitCheckTimeout(sink progress.EventSink, results []selfhosted.CheckResult, cleanup []string) {
	passed, failed, warned := selfhosted.CountResults(results)
	_ = sink.Emit(context.Background(), progress.Final{
		Success:      false,
		Verdict:      "timeout",
		TotalChecks:  len(results),
		PassedCount:  passed,
		FailedCount:  failed,
		WarningCount: warned,
		Cleanup:      cleanup,
	})
}

// anyCutShort reports a check a budget stopped before it finished.
func anyCutShort(results []selfhosted.CheckResult) bool {
	for _, r := range results {
		if r.CutShort {
			return true
		}
	}
	return false
}

// anyCutShortFailure reports a check a budget stopped whose worst outcome
// fails the run. One that could at worst warn does not.
func anyCutShortFailure(results []selfhosted.CheckResult) bool {
	for _, r := range results {
		if r.CutShort && r.IsBlockingFailure() {
			return true
		}
	}
	return false
}

// checkPreflightTools is a test seam over the local tool checks, so command
// tests do not depend on what is installed on the machine running them.
var checkPreflightTools = selfHostedPreflightTools

// checkRunRole is a test seam over one role's checks, so command tests can
// grade rows a time budget stopped without waiting it out.
var checkRunRole = selfhosted.RunPreflightForRole

// checkBudget is a test seam over the command's outer time budget.
var checkBudget = func(d time.Duration) time.Duration { return d }

// checkPollInterval is how often --wait runs the checks again. A var so tests
// can shorten it.
var checkPollInterval = 5 * time.Second

// parseCheckWait parses --wait. Empty is a single run. A malformed or
// non-positive duration is a usage error, reported before any work starts.
func parseCheckWait(raw string) (time.Duration, error) {
	if raw == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid --wait duration %q: %w", raw, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("invalid --wait duration %q: must be positive", raw)
	}
	return d, nil
}

// checkRunBudget is the command's outer time budget. Each role invocation gets
// the local share, for the checks that run on this machine, and the probe
// share, which it enforces on the cluster checks before its validator, plus
// the validator's longest run when one is configured: its own timeout, the
// wait after it for the Job's deadline to end the pod, the reads that grade
// it and the sweeps. Sizing on the validator's timeout alone ran the budget
// out during that wait, reporting a spent budget in place of the validator's
// own failure. ModeSingle runs the two roles in turn, so each needs a share;
// ModeSplit runs them in parallel. --wait polls for its duration on top.
func checkRunBudget(mode kubectx.Mode, localOnly, validatorConfigured bool, wait time.Duration) time.Duration {
	perRole := selfhosted.LocalCheckShare() + selfhosted.CheckProbeShare()
	if validatorConfigured {
		perRole += selfhosted.ClusterValidatorRunCeiling()
	}
	roles := time.Duration(1)
	if !localOnly && mode == kubectx.ModeSingle && controlPlaneIsVisited() && computePlaneIsVisited() {
		roles = 2
	}
	return roles*perRole + wait
}

func clusterValidationSkipped() bool {
	return checkSkipClusterValidation || envToggleOn(envSkipClusterValidation)
}

func inotifyCheckSkipped() bool {
	return checkSkipInotifyCheck || envToggleOn(envSkipInotify)
}

// The env toggles of check's --local-only, --skip-cluster-validation and
// --skip-inotify-check.
const (
	envLocalOnly             = "NVCF_CLI_SELFHOSTED_LOCAL_ONLY"
	envSkipClusterValidation = "NVCF_CLI_SELFHOSTED_SKIP_CLUSTER_VALIDATION"
	envSkipInotify           = "NVCF_CLI_SELFHOSTED_SKIP_INOTIFY"
)

// envToggle reads a boolean env toggle: unset or empty is off, and a word
// progress.ParseToggle reads says which, so "false", "0" and "no" are off.
// Any other value is an error naming it.
func envToggle(name string) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return false, nil
	}
	on, ok := progress.ParseToggle(raw)
	if !ok {
		return false, fmt.Errorf("%s=%q: expected true or false (or 1/0, yes/no, y/n, on/off)", name, raw)
	}
	return on, nil
}

// envToggleOn is envToggle for a toggle validated before the run starts.
func envToggleOn(name string) bool {
	on, _ := envToggle(name)
	return on
}

// resolveCheckSISURL returns the SIS URL to probe, or "" and why there is
// none. Unlike resolveICMSURL it does not fall back to the client's built-in
// base_http_url, nor derive one from a base_http_url on NVIDIA's hosted
// service, as a config copied from the template has: neither is this install.
func resolveCheckSISURL(flagValue string) (sisURL, unset string) {
	if flagValue == "" && os.Getenv("NVCF_ICMS_URL") == "" && os.Getenv("NVCF_SIS_URL") == "" &&
		strings.TrimSpace(viper.GetString("icms_url")) == "" {
		switch base := strings.TrimSpace(viper.GetString("base_http_url")); {
		case base == "":
			return "", "no ICMS URL is configured"
		case onNVIDIAHostedNVCF(base):
			return "", "base_http_url names NVIDIA's hosted NVCF, not this install"
		}
	}
	return resolveICMSURL(flagValue), ""
}

// onNVIDIAHostedNVCF reports whether rawURL is on the domain of NVIDIA's
// hosted NVCF, which the client's default base_http_url and the config
// template's name.
func onNVIDIAHostedNVCF(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "nvcf.nvidia.com" || strings.HasSuffix(host, ".nvcf.nvidia.com")
}

// validatorSkip is the row for a role whose validator does not run: none when
// it runs, or when the role reports its own unresolved image.
func validatorSkip(category, image, unresolvedImage string) []selfhosted.SkippedCheck {
	switch {
	case clusterValidationSkipped():
		return []selfhosted.SkippedCheck{{
			Category: category, ID: "cluster-validator",
			Message: "cluster-validator skipped (--skip-cluster-validation)",
		}}
	case image == "" && unresolvedImage == "":
		return []selfhosted.SkippedCheck{{
			Category: category, ID: "cluster-validator", Warn: true,
			Message: "cluster-validator not run: cluster_validator_image is not set; set it in the nvcf-cli " +
				"config, NVCF_CLI_CLUSTER_VALIDATOR_IMAGE or --cluster-validator-image to validate the cluster",
		}}
	}
	return nil
}

// localOnlySkips are the rows for what --local-only leaves out: the registry
// credential check, and the cluster checks of each role the scope selects.
func localOnlySkips() []selfhosted.SkippedCheck {
	skips := []selfhosted.SkippedCheck{{
		Category: selfhosted.CategoryRegistryCredentials, ID: "registry-credentials",
		Message: "registry credentials skipped (--local-only)",
	}}
	if controlPlaneIsVisited() {
		skips = append(skips, selfhosted.SkippedCheck{
			Category: selfhosted.CategoryControlPlane, ID: "control-plane-cluster",
			Message: "control-plane cluster checks skipped (--local-only)",
		})
	}
	if computePlaneIsVisited() {
		skips = append(skips, selfhosted.SkippedCheck{
			Category: selfhosted.CategoryComputePlane, ID: "compute-plane-cluster",
			Message: "compute-plane cluster checks skipped (--local-only)",
		})
	}
	return skips
}

// anyFindingFailed reports a blocking failure the cluster or host caused, not
// the budget.
func anyFindingFailed(results []selfhosted.CheckResult) bool {
	for _, r := range results {
		if r.IsBlockingFailure() && !r.CutShort {
			return true
		}
	}
	return false
}

// anyWarningToWaitOn reports a warning a later poll may clear: one expected to
// clear by itself, such as a rollout the validator saw in progress, or a check
// its budget stopped. --wait keeps polling on it; a single run still exits 0.
// A warning its share cut short on the previous poll too is not waited on:
// each poll gets the same shares, so the next would most likely cut it short
// the same way, and --wait would exit 5 where a single run exits 0.
func anyWarningToWaitOn(results, previous []selfhosted.CheckResult) bool {
	cutBefore := map[[2]string]bool{}
	for _, r := range previous {
		if cutShortWarning(r) {
			cutBefore[[2]string{r.Category, r.ID}] = true
		}
	}
	for _, r := range results {
		switch {
		case r.Passed:
		case r.Transient:
			return true
		case r.CutShort && !(cutShortWarning(r) && cutBefore[[2]string{r.Category, r.ID}]):
			return true
		}
	}
	return false
}

// cutShortWarning reports a check a budget stopped that could at worst warn.
func cutShortWarning(r selfhosted.CheckResult) bool {
	return r.CutShort && r.IsWarning()
}

// anyFailed reports a result that fails the run. Warnings do not. The exit
// code and the final event's verdict both grade with IsBlockingFailure, so
// they cannot disagree.
func anyFailed(results []selfhosted.CheckResult) bool {
	for _, r := range results {
		if r.IsBlockingFailure() {
			return true
		}
	}
	return false
}
