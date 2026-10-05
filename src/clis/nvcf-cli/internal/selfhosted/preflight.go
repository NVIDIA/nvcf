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

package selfhosted

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Masterminds/semver/v3"
	corev1 "k8s.io/api/core/v1"

	"nvcf-cli/internal/selfhosted/progress"
	"nvcf-cli/internal/selfhosted/severity"
)

const (
	// versionProbeTimeout is the per-binary timeout for each version probe attempt.
	versionProbeTimeout = 15 * time.Second

	// versionProbeAttempts allows one transient process failure before preflight
	// declares a present tool unusable.
	versionProbeAttempts = 2

	binaryVersionMessage = "%s %s on PATH (%s required)"
)

// Severity is a CheckResult's severity, graded by severity.Of.
type Severity = severity.Severity

const (
	SeverityInfo    = severity.Info
	SeverityWarning = severity.Warning
	SeverityError   = severity.Error
)

// IsBlockingFailure reports whether the result fails the run. The exit code,
// the final event, the category counts, the summaries and the dashboard all
// grade with severity.Of, so they cannot disagree.
func (r CheckResult) IsBlockingFailure() bool {
	return severity.Of(r.Passed, r.Severity) == severity.Fail
}

// IsWarning reports a miss that does not fail the run.
func (r CheckResult) IsWarning() bool {
	return severity.Of(r.Passed, r.Severity) == severity.Warn
}

// CountResults tallies results by the one rule every count uses.
func CountResults(results []CheckResult) (passed, failed, warned int) {
	for _, r := range results {
		switch severity.Of(r.Passed, r.Severity) {
		case severity.Pass:
			passed++
		case severity.Fail:
			failed++
		default:
			warned++
		}
	}
	return passed, failed, warned
}

// One row in the linkerd-style output. Logs is internal-only and is not
// forwarded into the CheckCompleted JSONL wire event, so it never leaks
// into the stable JSON contract.
type CheckResult struct {
	ID       string
	Category string
	Severity Severity
	Passed   bool
	Message  string
	Detail   string // optional: short version string or extra context (M+8.11)
	HintURL  string
	Err      error  // populated only when the check itself failed to execute
	Logs     string // optional: full check transcript for --show-logs; not emitted to JSON
	// Transient marks a warning expected to clear by itself, such as a
	// rollout in progress. --wait keeps polling while one remains.
	Transient bool
	// CutShort marks a check the run's time budget stopped: it never started,
	// or ran out of time while it ran. Such a result is no finding about the
	// cluster, so the verdict is a timeout.
	CutShort bool
	// Cleanup is the command that removes what the check left in the
	// cluster. Its row is reported even when the run is interrupted.
	Cleanup string
}

// BinarySpec defines a tool that must be on PATH and a version constraint.
// LookPath and Version are seams for testing; production callers pass
// exec.LookPath and a real version probe.
type BinarySpec struct {
	Name            string
	MinVer          *semver.Version
	MaxVerExclusive *semver.Version
	HintURL         string
	LookPath        func(name string) (string, error)
	Version         func(ctx context.Context, path string) (*semver.Version, error)
}

func checkBinary(ctx context.Context, s BinarySpec) CheckResult {
	r := CheckResult{
		ID:       "local-host-tools-" + s.Name,
		Category: "local-host-tools",
		Severity: SeverityError,
		HintURL:  s.HintURL,
	}
	path, err := s.LookPath(s.Name)
	if err != nil || path == "" {
		r.Message = s.Name + " not found on PATH"
		return r
	}
	var v *semver.Version
	var probeErr error
	attempts := 0
	for attempt := 1; attempt <= versionProbeAttempts; attempt++ {
		attempts++
		v, probeErr = s.Version(ctx, path)
		if probeErr == nil {
			break
		}
		if ctx.Err() != nil {
			break
		}
	}
	if probeErr != nil {
		r.Message = fmt.Sprintf("%s present at %s but version probe failed after %d attempts: %v", s.Name, path, attempts, probeErr)
		r.Err = probeErr
		return r
	}
	if v.LessThan(s.MinVer) {
		r.Message = s.versionStatusMessage(v)
		return r
	}
	if s.MaxVerExclusive != nil && !v.LessThan(s.MaxVerExclusive) {
		r.Message = s.versionStatusMessage(v)
		return r
	}
	r.Passed = true
	r.Message = s.versionStatusMessage(v)
	r.Detail = v.String()
	return r
}

func (s BinarySpec) versionConstraintString() string {
	if s.MaxVerExclusive != nil {
		return fmt.Sprintf(">= %s and < %s", s.MinVer.String(), s.MaxVerExclusive.String())
	}
	return fmt.Sprintf(">= %s", s.MinVer.String())
}

func (s BinarySpec) versionStatusMessage(v *semver.Version) string {
	return fmt.Sprintf(binaryVersionMessage, s.Name, v.String(), s.versionConstraintString())
}

func probeKubectlVersion(ctx context.Context, path string) (*semver.Version, error) {
	return runVersionCmd(ctx, path, []string{"version", "--client", "--output=json"}, kubectlVersionRE)
}

func probeHelmfileVersion(ctx context.Context, path string) (*semver.Version, error) {
	return runVersionCmdCandidates(ctx, path, semverRE,
		[]string{"version", "--output", "short"},
		[]string{"version"},
		[]string{"--version"},
	)
}

func probeHelmVersion(ctx context.Context, path string) (*semver.Version, error) {
	return runVersionCmdCandidates(ctx, path, semverRE,
		[]string{"version", "--short"},
		[]string{"version"},
		[]string{"--version"},
	)
}

var (
	semverRE         = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)
	kubectlVersionRE = regexp.MustCompile(`"gitVersion":\s*"v(\d+\.\d+\.\d+)`)
)

func runVersionCmd(ctx context.Context, path string, args []string, re *regexp.Regexp) (*semver.Version, error) {
	return runVersionCmdCandidates(ctx, path, re, args)
}

func runVersionCmdCandidates(ctx context.Context, path string, re *regexp.Regexp, candidates ...[]string) (*semver.Version, error) {
	var errs []string
	for _, args := range candidates {
		v, err := runVersionCmdOnce(ctx, path, args, re)
		if err == nil {
			return v, nil
		}
		errs = append(errs, err.Error())
		if ctx.Err() != nil {
			break
		}
	}
	if len(errs) == 0 {
		return nil, fmt.Errorf("no version probe commands configured for %s", path)
	}
	return nil, fmt.Errorf("%s", strings.Join(errs, "; "))
}

func runVersionCmdOnce(ctx context.Context, path string, args []string, re *regexp.Regexp) (*semver.Version, error) {
	ctx, cancel := context.WithTimeout(ctx, versionProbeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, args...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("running %s %v: %w", path, args, err)
	}
	m := re.FindStringSubmatch(string(out))
	if m == nil {
		return nil, fmt.Errorf("no semver in output: %s", out)
	}
	ver := m[1]
	if len(m) >= 4 && m[1] != "" && m[2] != "" && m[3] != "" {
		ver = m[1] + "." + m[2] + "." + m[3]
	}
	return semver.NewVersion(ver)
}

// PreflightConfig controls which checks RunPreflight executes.
type PreflightConfig struct {
	LocalOnly bool
	Tools     []BinarySpec
	// ToolsAdvisory grades a missing or unsupported local tool as a warning.
	// Checking an installed stack runs no helm, helmfile or kubectl binary;
	// only the install does.
	ToolsAdvisory bool

	// Registries is the list of container registries to credential-check.
	// When empty, the registry-credentials category is omitted entirely.
	// Populated by the cmd layer from EnumerateRegistries.
	Registries []RegistryEntry

	// RegistryChecker validates credentials for one registry. Nil skips the
	// category. Production wires NewRegistryCredentialChecker; tests pass fakes.
	RegistryChecker RegistryCredentialChecker

	// Interrupted reports an interrupt. The run's context cannot: once the
	// check's budget has ended it, a later interrupt leaves it unchanged, and
	// an interrupted run reports no rows for checks it never finished.
	Interrupted func() bool

	// RegistryPostInstall marks a run that checks an installed stack. The
	// cluster then pulls with its own pull secrets, so a credential of this
	// machine's that a registry rejects is a warning rather than an error.
	RegistryPostInstall bool
}

// DefaultTools returns the kubectl/helmfile/helm specs with version floors
// matching this CLI release. Update versions here when the supported range
// shifts; CI version-pinning tracking is in spec §12.
func DefaultTools() []BinarySpec {
	return []BinarySpec{
		{
			Name: "kubectl", MinVer: semver.MustParse("1.28.0"),
			HintURL:  "https://kubernetes.io/docs/tasks/tools/",
			LookPath: exec.LookPath, Version: probeKubectlVersion,
		},
		{
			Name: "helmfile", MinVer: semver.MustParse("1.0.0"),
			HintURL:  "https://github.com/helmfile/helmfile#installation",
			LookPath: exec.LookPath, Version: probeHelmfileVersion,
		},
		{
			Name: "helm", MinVer: semver.MustParse("3.14.0"),
			HintURL:  "https://helm.sh/docs/intro/install/",
			LookPath: exec.LookPath, Version: probeHelmVersion,
		},
	}
}

// DefaultToolsWithPreferredDir returns the default tool specs, but prefers
// binaries from preferredDir when they exist. This lets local stack workflows
// use the stack-pinned bin/ tools before falling back to the host PATH.
func DefaultToolsWithPreferredDir(preferredDir string) []BinarySpec {
	tools := DefaultTools()
	if preferredDir == "" {
		return tools
	}
	for i := range tools {
		fallback := tools[i].LookPath
		tools[i].LookPath = preferredDirLookPath(preferredDir, fallback)
	}
	return tools
}

func preferredDirLookPath(preferredDir string, fallback func(string) (string, error)) func(string) (string, error) {
	return func(name string) (string, error) {
		candidate := filepath.Join(preferredDir, name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate, nil
		}
		return fallback(name)
	}
}

// Role selects which subset of pre-flight checks runs. Roles are not mutually
// exclusive — single-cluster mode unions RoleControlPlane + RoleComputePlane.
// RoleLocalOnly is the floor (shared local-host checks only).
type Role int

const (
	RoleLocalOnly    Role = iota // shared local-host tools only; no kubectl contact
	RoleControlPlane             // shared + gateway/StorageClass/LB checks via cluster-validator
	RoleComputePlane             // shared + SIS reachability, node inotify limits and the validator, each opt-in
)

// Validator role strings passed as VALIDATOR_ROLE to the cluster-validator Job.
// These must match the constants in the nvca cluster-validator binary's
// clustervalidator package (RoleControlPlane / RoleComputePlane).
const (
	validatorRoleControlPlane = "control-plane"
	validatorRoleComputePlane = "compute-plane"
)

// String returns a human-readable name for the role.
func (r Role) String() string {
	switch r {
	case RoleLocalOnly:
		return "local-only"
	case RoleControlPlane:
		return "control-plane"
	case RoleComputePlane:
		return "compute-plane"
	default:
		return "unknown"
	}
}

// RoleConfig configures role-specific probes that don't fit BinarySpec.
type RoleConfig struct {
	KubeContext string // used for the kubectl-check probes (passed to subprocesses)
	SISURL      string // when non-empty, RoleComputePlane adds an HTTP-reachability probe

	// InotifyProber probes node-level inotify limits on the compute-plane
	// cluster. When nil, the inotify check is skipped (e.g. callers that opted
	// out via --skip-inotify-check or environments where the prober cannot be
	// constructed). Production wires NewInotifyProber; tests pass fakes.
	InotifyProber NodeInotifyProber

	// Nil skips the cluster-validator check; the caller reports why in
	// Skipped.
	ClusterValidator           ClusterValidator
	ClusterValidatorImage      string
	ClusterValidatorPullSecret string
	ClusterValidatorNoCleanup  bool

	// StaleNamespaceProber detects NVCF stack namespaces that are being
	// deleted, hold a Helm release left mid-operation, or hold what a removed
	// install leaves behind. Nil skips the check; production wires
	// NewStaleNamespaceProber; tests pass fakes.
	StaleNamespaceProber StaleNamespaceProber

	// StackDir is the resolved stack checkout. When set, the namespaces to
	// probe are read from its helmfile.d rather than the static fallback list,
	// so the check follows the stack instead of drifting from it.
	StackDir string

	// StackEnv is the helmfile environment whose values file, layered over
	// StackDir's environments/base.yaml, decides which gated releases the
	// stack installs, and so which of their namespaces are probed.
	StackEnv string

	// ExtraStaleNamespaces are merged into this role's stale-namespace scan.
	// In ModeSingle both roles share one cluster, so only one of them runs the
	// probe to avoid emitting the same check ID twice; the other role's
	// namespaces are passed here instead of being dropped. The two roles probe
	// disjoint lists, so without this the skipped role's namespaces are never
	// scanned on the one topology where they sit on the same cluster.
	ExtraStaleNamespaces []string

	// ClusterValidatorRegistries are the registries the control-plane
	// validator probes for reachability: the EnumerateRegistries result the
	// local credential check also uses. Ignored for the compute-plane validator.
	ClusterValidatorRegistries []RegistryEntry

	// ClusterValidatorEnv is passed to the validator container for both roles.
	ClusterValidatorEnv map[string]string
	// ClusterValidatorTolerations are added to the validator Job's
	// control-plane tolerations.
	ClusterValidatorTolerations []corev1.Toleration

	// ClusterValidatorUnresolvedImage is a configured validator image without
	// a tag whose latest tag could not be discovered. The validator does not
	// run, and the role reports that as a failed check rather than nothing.
	ClusterValidatorUnresolvedImage string

	// ValidatorCleanup records the removal command of every validator run
	// that may leave objects in the cluster. Nil records nothing.
	ValidatorCleanup *CleanupLedger

	// Skipped are the checks the run's flags or configuration leave out. Each
	// is still reported as a row, so the stream says what was not checked.
	Skipped []SkippedCheck
}

// SkippedCheck is a check the run does not make.
type SkippedCheck struct {
	Category string
	ID       string
	Message  string
	// Warn reports a check the run was expected to make but lacked an input
	// for. An explicit opt-out passes at info, as a registry this CLI cannot
	// probe does.
	Warn bool
}

func skippedCheck(s SkippedCheck) binaryCheckSpec {
	return binaryCheckSpec{
		ID:         s.ID,
		HumanLabel: s.Message,
		static:     true,
		Run: func(context.Context) CheckResult {
			if s.Warn {
				return CheckResult{ID: s.ID, Severity: SeverityWarning, Message: s.Message}
			}
			return CheckResult{ID: s.ID, Severity: SeverityInfo, Passed: true, Message: s.Message}
		},
	}
}

// The check categories a run reports.
const (
	CategoryLocalHostTools      = "local-host-tools"
	CategoryRegistryCredentials = "registry-credentials"
	CategoryControlPlane        = "control-plane-cluster"
	CategoryComputePlane        = "compute-plane-cluster"
)

// categorySpec groups a set of checks under a named category. Categories run
// in declaration order so the bubbletea ModeCheck dashboard renders them
// top-to-bottom predictably.
type categorySpec struct {
	name   string
	role   Role // RoleLocalOnly = always runs; others gated by RunPreflightForRole's role arg
	checks []binaryCheckSpec
}

// binaryCheckSpec is an internal adapter that wraps BinarySpec with the extra
// metadata RunPreflightStreaming needs (human label for in-flight TTY rows).
type binaryCheckSpec struct {
	ID         string
	HumanLabel string
	Run        func(ctx context.Context) CheckResult
	// ownBudget runs the check on the run's budget, which sizes it by its own
	// ceiling, instead of inside the probe share.
	ownBudget bool
	// static marks a row whose result is known without any work, so the
	// budget cannot cut it short.
	static bool
}

// buildCategories converts a PreflightConfig into the ordered list of
// categorySpec values that runPreflightImpl iterates. The role argument gates
// which cluster-side categories are added beyond the shared local-host category.
func buildCategories(cfg PreflightConfig, role Role, rc RoleConfig) []categorySpec {
	var out []categorySpec

	if local := buildLocalHostCategory(cfg); local != nil {
		out = append(out, *local)
	}

	// Registry credential check runs from the operator's machine - no cluster
	// contact needed. RunPreflightForRole is called once per role, so the
	// caller clears Registries on every role but one to avoid duplicate
	// check_started/check_completed events. Deduplicating on the role here
	// instead would drop the check entirely when the control plane does not
	// run, which is the case for a compute-plane-only invocation.
	if len(cfg.Registries) > 0 && cfg.RegistryChecker != nil {
		out = append(out, buildRegistryCredentialCategory(cfg))
	}

	if !cfg.LocalOnly {
		switch role {
		case RoleControlPlane:
			out = append(out, controlPlaneCheckCategory(rc))
		case RoleComputePlane:
			out = append(out, computePlaneCheckCategory(rc))
		}
	}

	for _, s := range rc.Skipped {
		i := slices.IndexFunc(out, func(c categorySpec) bool { return c.name == s.Category })
		if i < 0 {
			i = len(out)
			out = append(out, categorySpec{name: s.Category, role: role})
		}
		out[i].checks = append(out[i].checks, skippedCheck(s))
	}
	// A category with no rows would report a clean tally for nothing checked.
	return slices.DeleteFunc(out, func(c categorySpec) bool { return len(c.checks) == 0 })
}

// buildLocalHostCategory builds the shared local-host-tools category from cfg.
func buildLocalHostCategory(cfg PreflightConfig) *categorySpec {
	var checks []binaryCheckSpec
	for _, t := range cfg.Tools {
		t := t // capture loop var
		checks = append(checks, binaryCheckSpec{
			ID:         "local-host-tools-" + t.Name,
			HumanLabel: "checking " + t.Name + "…",
			Run: func(ctx context.Context) CheckResult {
				return checkBinary(ctx, t)
			},
		})
	}
	if compat := helmRuntimeCompatibilityCheck(cfg.Tools); compat != nil {
		checks = append(checks, *compat)
	}
	if len(checks) == 0 {
		return nil
	}
	if cfg.ToolsAdvisory {
		for i := range checks {
			checks[i].Run = advisoryTool(checks[i].Run)
		}
	}
	return &categorySpec{name: CategoryLocalHostTools, role: RoleLocalOnly, checks: checks}
}

// advisoryTool grades a tool check's miss as a warning.
func advisoryTool(run func(context.Context) CheckResult) func(context.Context) CheckResult {
	return func(ctx context.Context) CheckResult {
		r := run(ctx)
		if !r.Passed {
			r.Severity = SeverityWarning
			r.Message += "; only an install needs it"
		}
		return r
	}
}

// controlPlaneCheckCategory returns the cluster-side checks for the control
// plane. The stale-namespace check runs first so a leftover namespace from a
// prior partial teardown is surfaced before any other cluster work.
func controlPlaneCheckCategory(rc RoleConfig) categorySpec {
	cat := categorySpec{
		name:   CategoryControlPlane,
		role:   RoleControlPlane,
		checks: []binaryCheckSpec{},
	}
	// Stale namespace check runs first so leftover namespaces surface
	// before any other cluster work.
	if rc.StaleNamespaceProber != nil {
		cat.checks = append(cat.checks,
			staleNamespaceCheck(rc.StaleNamespaceProber, rc.KubeContext,
				mergeNamespaces(resolveStackNamespaces(rc.StackDir, rc.StackEnv, nvcfControlPlaneNamespaces),
					rc.ExtraStaleNamespaces)))
	}
	// Containerized cluster-validator probe for control-plane checks
	// (Gateway API CRDs, Envoy Gateway, StorageClass, external LB,
	// node-to-node overlay, and reachability to rc's registries).
	if rc.ClusterValidator != nil {
		cat.checks = append(cat.checks, clusterValidatorCheck(rc, validatorRoleControlPlane))
	} else if rc.ClusterValidatorUnresolvedImage != "" {
		cat.checks = append(cat.checks, unresolvedValidatorCheck(rc.ClusterValidatorUnresolvedImage))
	}
	return cat
}

// computePlaneCheckCategory returns the compute-plane checks. SIS, inotify,
// and cluster-validator probes are opt-in via their RoleConfig fields; a nil
// or empty field omits the corresponding check from the category.
func computePlaneCheckCategory(rc RoleConfig) categorySpec {
	cat := categorySpec{
		name: CategoryComputePlane,
		role: RoleComputePlane,
	}
	// Stale namespace check runs first so leftover namespaces from a prior
	// partial teardown surface before any other cluster work.
	if rc.StaleNamespaceProber != nil {
		cat.checks = append(cat.checks,
			staleNamespaceCheck(rc.StaleNamespaceProber, rc.KubeContext,
				mergeNamespaces(resolveStackNamespaces(rc.StackDir, rc.StackEnv, nvcfComputePlaneNamespaces),
					rc.ExtraStaleNamespaces)))
	}
	if rc.SISURL != "" {
		cat.checks = append(cat.checks, sisReachabilityCheck(rc.SISURL))
	}
	if rc.InotifyProber != nil {
		cat.checks = append(cat.checks, nodeInotifyCheck(rc.InotifyProber, rc.KubeContext))
	}
	if rc.ClusterValidator != nil {
		cat.checks = append(cat.checks, clusterValidatorCheck(rc, validatorRoleComputePlane))
	} else if rc.ClusterValidatorUnresolvedImage != "" {
		cat.checks = append(cat.checks, unresolvedValidatorCheck(rc.ClusterValidatorUnresolvedImage))
	}
	return cat
}

// Required minimum inotify limits per
// docs/compute-plane/cluster-management/self-managed.md#node-inotify-limits.
// NVCA bootstrap fails with "too many open files" when these are too low,
// which surfaces downstream as opaque errors like empty clusterGroups or
// "Invalid GPU specified" on function deploy.
const (
	minInotifyMaxUserInstances = 8192
	minInotifyMaxUserWatches   = 524288
	inotifyHintURL             = "https://docs.nvidia.com/nvcf/self-managed-clusters#node-inotify-limits"
)

// NodeInotifyLimits captures one node's observed inotify sysctls. Err is
// populated only on per-node probe failure; cluster-wide failures surface as
// the prober's returned error instead.
type NodeInotifyLimits struct {
	NodeName         string
	MaxUserInstances int64
	MaxUserWatches   int64
	Err              error
}

// NodeInotifyProber returns one NodeInotifyLimits per cluster node, or a
// non-nil error if probing the cluster failed before any per-node result
// could be collected.
type NodeInotifyProber func(ctx context.Context, kubeContext string) ([]NodeInotifyLimits, error)

// nodeInotifyCheck verifies fs.inotify.max_user_instances and
// fs.inotify.max_user_watches on every compute-plane node meet NVCA's
// minimums. On failure it lists the offending nodes with observed-vs-required
// values and a link to the documented remediation DaemonSet.
func nodeInotifyCheck(prober NodeInotifyProber, kubeContext string) binaryCheckSpec {
	const id = "node-inotify-limits"
	return binaryCheckSpec{
		ID:         id,
		HumanLabel: "checking node inotify limits…",
		Run: func(ctx context.Context) CheckResult {
			r := CheckResult{
				ID:       id,
				Severity: SeverityError,
				HintURL:  inotifyHintURL,
			}
			limits, err := prober(ctx, kubeContext)
			if err != nil {
				r.Severity = SeverityWarning
				r.Message = "node inotify probe failed: " + err.Error()
				r.Err = err
				return r
			}
			if len(limits) == 0 {
				// A probe that checked nothing is not a pass.
				r.Severity = SeverityWarning
				r.Message = "no schedulable nodes were probed for inotify limits"
				return r
			}
			var failing, probeErrs []string
			for _, l := range limits {
				if l.Err != nil {
					probeErrs = append(probeErrs, fmt.Sprintf("%s: %v", l.NodeName, l.Err))
					continue
				}
				if l.MaxUserInstances < minInotifyMaxUserInstances || l.MaxUserWatches < minInotifyMaxUserWatches {
					failing = append(failing, fmt.Sprintf(
						"%s (max_user_instances=%d/%d, max_user_watches=%d/%d)",
						l.NodeName,
						l.MaxUserInstances, minInotifyMaxUserInstances,
						l.MaxUserWatches, minInotifyMaxUserWatches,
					))
				}
			}
			// Limit violations take priority over probe errors. If both are
			// present, surface the violations as error and append the probe
			// errors so non-compliant nodes are never hidden behind warning
			// noise from an unrelated RBAC/scheduling failure.
			if len(failing) > 0 {
				msg := fmt.Sprintf(
					"node inotify limits below NVCA minimums (max_user_instances >= %d, max_user_watches >= %d) on %d node(s): %s",
					minInotifyMaxUserInstances, minInotifyMaxUserWatches, len(failing), strings.Join(failing, "; "),
				)
				if len(probeErrs) > 0 {
					msg += fmt.Sprintf("; additionally could not probe %d node(s): %s",
						len(probeErrs), strings.Join(probeErrs, "; "))
				}
				r.Message = msg
				return r
			}
			if len(probeErrs) > 0 {
				r.Severity = SeverityWarning
				r.Message = fmt.Sprintf(
					"could not probe inotify limits on %d node(s): %s",
					len(probeErrs), strings.Join(probeErrs, "; "),
				)
				return r
			}
			r.Passed = true
			r.Message = fmt.Sprintf("inotify limits meet NVCA minimums on %d node(s)", len(limits))
			return r
		},
	}
}

// unresolvedValidatorCheck reports a configured validator that cannot run
// because its image has no tag and the latest one could not be discovered.
// The kubelet would pull such a reference as :latest, which the validator
// repository does not publish. It fails like any validator that could not
// run: nothing was validated.
func unresolvedValidatorCheck(image string) binaryCheckSpec {
	const id = "cluster-validator"
	return binaryCheckSpec{
		ID:         id,
		HumanLabel: "resolving the cluster-validator image...",
		static:     true,
		Run: func(context.Context) CheckResult {
			return CheckResult{
				ID:       id,
				Severity: SeverityError,
				HintURL:  clusterValidatorHintURL,
				Message: "cluster-validator not run: could not resolve a tag for " + image +
					"; pin cluster_validator_image to a tag, or pass --skip-cluster-validation",
			}
		},
	}
}

// clusterValidatorCheck runs the validator Job for role. A validator that
// could not run (RBAC denied, image pull failure, timeout) is an error like a
// validator failure: nothing was checked, and a readiness gate must not pass
// on that. --skip-cluster-validation is the explicit opt-out. Only the
// control-plane validator probes rc's registries.
func clusterValidatorCheck(rc RoleConfig, role string) binaryCheckSpec {
	const id = "cluster-validator"
	registries := rc.ClusterValidatorRegistries
	if role != validatorRoleControlPlane {
		registries = nil
	}
	return binaryCheckSpec{
		ID:         id,
		HumanLabel: "running cluster-validator probe…",
		ownBudget:  true,
		Run: func(ctx context.Context) CheckResult {
			r := CheckResult{
				ID:      id,
				HintURL: clusterValidatorHintURL,
			}
			// Every hint names the context the run used, so it stays right
			// after the current context changes.
			kubeContext := effectiveKubeContext(rc.KubeContext)
			result := rc.ClusterValidator(ctx, ClusterValidatorParams{
				KubeContext: kubeContext,
				Image:       rc.ClusterValidatorImage,
				PullSecret:  rc.ClusterValidatorPullSecret,
				NoCleanup:   rc.ClusterValidatorNoCleanup,
				Role:        role,
				Registries:  registries,
				Env:         rc.ClusterValidatorEnv,
				Tolerations: rc.ClusterValidatorTolerations,
				OnStart: func(runID string) {
					rc.ValidatorCleanup.started(runID, validatorRemovalCommand(kubeContext, runID))
				},
			})
			r.Logs = result.Logs
			r.Detail = strings.Join(append(result.Notes, clusterValidatorDetail(kubeContext, result)), "; ")
			r.Detail = strings.TrimSuffix(r.Detail, "; ")
			if why := validatorKeptReason(rc.ClusterValidatorNoCleanup, result); why != "" {
				r.Cleanup = validatorRemovalCommand(kubeContext, result.RunID)
				r.Detail = strings.TrimPrefix(r.Detail+"; ", "; ") + why + ": " + r.Cleanup
			}
			rc.ValidatorCleanup.finished(result.RunID, r.Cleanup)

			if errors.Is(result.Err, context.Canceled) {
				r.Severity = SeverityWarning
				r.Message = "cluster-validator was interrupted"
				r.Err = result.Err
				return r
			}
			if result.Err != nil {
				r.Severity = SeverityError
				r.Message = "cluster-validator did not complete, so the cluster was not validated: " +
					result.Err.Error() + "; fix the cause, or pass --skip-cluster-validation to run without it"
				r.Err = result.Err
				return r
			}
			gradeValidatorTranscript(&r, result, role, kubeContext)
			return r
		},
	}
}

// gradeValidatorTranscript grades a validator that ran to its end on what its
// transcript shows, not on the Job's status alone.
func gradeValidatorTranscript(r *CheckResult, result ClusterValidatorResult, role, kubeContext string) {
	// An image that predates role support ignores VALIDATOR_ROLE and runs
	// the compute-plane GPU checks, which fail on a CPU-only control
	// plane. The latest tag can be such an image until a release with
	// role support is published, so report it as the image being too
	// old rather than as the cluster failing. That needs positive
	// evidence of the legacy check set, not just a missing role line: a
	// role-aware image that fails before printing it (say, building its
	// Kubernetes client) must still fail the run.
	if role == validatorRoleControlPlane && isLegacyComputePlaneTranscript(result.Logs) {
		// Downgrade only the failure the old image causes by itself: its
		// GPU check on a CPU-only control plane. Its checks common to both
		// roles (the control plane's /readyz, admission webhooks, critical
		// registry reachability) still see real failures, and those fail.
		if others := legacyCriticalFailures(result.Logs); len(others) > 0 {
			r.Severity = SeverityError
			r.Message = "cluster-validator reported failures (" + strings.Join(others, "; ") +
				"); the image predates validator roles, so only its checks common to both roles ran"
			return
		}
		r.Severity = SeverityWarning
		r.Message = "cluster-validator image does not support the control-plane checks (it ran the " +
			"compute-plane set); use an image from an NVCA release that supports validator roles, " +
			"or pass --skip-cluster-validation"
		return
	}
	if !result.Passed {
		r.Severity = SeverityError
		if !strings.Contains(result.Logs, validatorVerdictNotReady) {
			// It ended before its verdict: a crash, an OOM kill or an
			// eviction, not the cluster failing a check.
			r.Message = "cluster-validator did not complete, so the cluster was not validated: " +
				validatorEndedEarly(result)
			return
		}
		r.Message = fmt.Sprintf("cluster-validator reported failures (exit code %d)", result.ExitCode)
		if rows := failedValidatorRows(result.Logs, ""); len(rows) > 0 {
			r.Message = "cluster-validator reported failures: " + strings.Join(rows, "; ")
		}
		return
	}
	// Info needs proof the requested checks ran and passed: the role line,
	// and the verdict. A transcript that cannot be read, or stops short of
	// its verdict, can hide warnings, so it is not a clean pass, and --wait
	// polls again.
	if why := unreadVerdict(result, role); why != "" {
		r.Severity = SeverityWarning
		r.Transient = true
		r.Message = "cluster-validator Job succeeded but its verdict could not be read (" + why + ")"
		if hint := kubectlLogsHint(kubeContext, result.JobName); hint != "" {
			r.Message += "; read it with: " + hint
		}
		return
	}
	if strings.Contains(result.Logs, validatorReadyWithWarnings) {
		// The Job succeeding is not a clean pass when the validator
		// itself reported warnings. A rollout in progress is expected
		// to clear, so --wait keeps polling on it.
		r.Severity = SeverityWarning
		r.Transient = isRolloutTranscript(result.Logs)
		r.Message = "cluster-validator passed with warnings; run with --show-logs for details"
		if r.Transient {
			r.Message = "cluster-validator passed with a rollout in progress; --wait polls until it completes"
		}
		return
	}
	r.Passed = true
	r.Severity = SeverityInfo
	r.Message = "cluster passed cluster-validator built-in checks"
}

// unreadVerdict says why a successful run's transcript does not show the
// requested check set reaching its verdict, or "" when it does. A validator
// that predates roles always runs the compute-plane set.
func unreadVerdict(result ClusterValidatorResult, role string) string {
	logs := result.Logs
	ranRole := strings.Contains(logs, validatorRoleMarker+role) ||
		(role == validatorRoleComputePlane && isLegacyComputePlaneTranscript(logs))
	switch {
	case result.LogsErr != nil && !strings.Contains(logs, validatorVerdictReady):
		return "its transcript could not be read: " + result.LogsErr.Error()
	case strings.TrimSpace(logs) == "":
		return "its transcript was empty"
	case !ranRole:
		return "the transcript does not show the " + role + " checks"
	case !strings.Contains(logs, validatorVerdictReady):
		return "the transcript has no verdict"
	}
	return ""
}

// validatorEndedEarly describes a validator that failed before its verdict.
func validatorEndedEarly(result ClusterValidatorResult) string {
	msg := "its pod ended before the validator printed a verdict"
	if result.ExitCode >= 0 {
		msg = fmt.Sprintf("the validator exited %d before printing a verdict", result.ExitCode)
	}
	if result.Reason != "" {
		msg += " (" + result.Reason + ")"
	}
	if result.LogsErr != nil {
		msg += "; its transcript could not be read: " + result.LogsErr.Error()
	}
	return msg
}

// validatorRoleMarker prefixes the line a role-aware validator prints naming
// the check set it runs. Must match RoleMarker in
// nvca/internal/clustervalidator/validator.go.
const validatorRoleMarker = "Validator role: "

// The verdict lines printSummary prints. The with-warnings line contains the
// plain one. Must match printSummary in nvca/internal/clustervalidator.
const (
	validatorVerdictReady      = "Cluster is NVCF-Ready"
	validatorReadyWithWarnings = "NVCF-Ready (with warnings)"
	validatorVerdictNotReady   = "Cluster is NVCF-Not-Ready"
)

// validatorRolloutMarkers are in the validator's warnings for a Deployment or
// StatefulSet rollout in progress (checks.go, Tier-1 and Tier-2).
var validatorRolloutMarkers = []string{"rollout in progress", "rolling update in progress", "mid-rollout"}

func isRolloutTranscript(logs string) bool {
	for _, m := range validatorRolloutMarkers {
		if strings.Contains(logs, m) {
			return true
		}
	}
	return false
}

// validatorSummaryStart, validatorFailIcon and validatorWarnIcon locate the
// failed and unobserved rows the validator prints in its summary. Must match
// printSummary and the print helpers in nvca/internal/clustervalidator.
const (
	validatorSummaryStart  = "Check Results:"
	validatorFailIcon      = "\u2717"
	validatorWarnIcon      = "\u26a0"
	validatorStatusUnknown = "Status Unknown"
)

// failedValidatorRows returns the summary rows of a transcript that failed a
// critical check, or that report a check as Status Unknown, leaving out rows
// that start with skip when skip is set. The rows end at the verdict: the line
// after it carries the failure mark too.
func failedValidatorRows(logs, skip string) []string {
	i := strings.LastIndex(logs, validatorSummaryStart)
	if i < 0 {
		return nil
	}
	var out []string
	for _, line := range strings.Split(logs[i:], "\n") {
		if strings.Contains(line, "Cluster is") {
			break
		}
		_, row, failed := strings.Cut(line, validatorFailIcon)
		if !failed {
			if !strings.Contains(line, validatorStatusUnknown) {
				continue
			}
			row = strings.TrimPrefix(strings.TrimSpace(line), validatorWarnIcon)
		}
		if row = strings.TrimSpace(row); row != "" && (skip == "" || !strings.HasPrefix(row, skip)) {
			out = append(out, row)
		}
	}
	return out
}

// legacyCriticalFailures returns the failed critical summary rows of a
// pre-role transcript other than GPU Resources, the one row the compute-plane
// set fails on any CPU-only control plane. A transcript without a summary
// returns a row saying so: nothing shows the run was otherwise clean.
func legacyCriticalFailures(logs string) []string {
	if !strings.Contains(logs, validatorSummaryStart) {
		return []string{"the validator printed no summary"}
	}
	return failedValidatorRows(logs, legacyComputePlaneHeader)
}

// legacyComputePlaneHeader is a section header only the compute-plane check
// set prints. A validator that predates roles always runs that set.
const legacyComputePlaneHeader = "GPU Resources"

// isLegacyComputePlaneTranscript reports whether logs come from a validator
// that ignored the requested control-plane role: no role line, and the
// compute-plane check set's output instead.
func isLegacyComputePlaneTranscript(logs string) bool {
	return !strings.Contains(logs, validatorRoleMarker) && strings.Contains(logs, legacyComputePlaneHeader)
}

// validatorKeptReason says why a run's objects are still in the cluster, or
// "" when nothing it created was kept.
func validatorKeptReason(noCleanup bool, result ClusterValidatorResult) string {
	switch {
	case result.RunID == "":
		return ""
	case result.SweepErr != nil:
		return "removing the run's objects failed (" + result.SweepErr.Error() + "); remove them with"
	case result.LeftBehind:
		// The pod could still be running, so its RBAC was kept rather than
		// pulled out from under it.
		return "the validator pod may still be running, so its objects were kept; once it ends, remove them with"
	case noCleanup && result.Created:
		return "kept with --no-cleanup; remove with"
	}
	return ""
}

// validatorRemovalCommand removes everything one validator run created, in an
// order that is safe at any time: look at the pod, stop the Job in the
// foreground so its pod ends while it still has its RBAC, revoke the
// cluster-wide RBAC, then delete the rest. Joined with ';', so a failed step
// does not skip the RBAC. Everything the run created carries its run label.
func validatorRemovalCommand(kubeContext, runID string) string {
	sel := clusterValidatorRunLabel + "=" + runID
	kubectl := "kubectl" + kubectlContextArg(kubeContext)
	return strings.Join([]string{
		fmt.Sprintf("%s get pods -n %s -l %s", kubectl, clusterValidatorNamespace, sel),
		fmt.Sprintf("%s delete -n %s job -l %s --cascade=foreground --wait", kubectl, clusterValidatorNamespace, sel),
		fmt.Sprintf("%s delete clusterrolebinding,clusterrole -l %s", kubectl, sel),
		fmt.Sprintf("%s delete -n %s rolebinding,role,serviceaccount,secret,configmap -l %s", kubectl,
			clusterValidatorNamespace, sel),
	}, "; ")
}

// CleanupLedger records, across the validator runs of one command, the
// removal command of every run that may leave objects in a cluster: runs in
// progress, and runs that kept objects. Safe for concurrent use; its methods
// do nothing on a nil ledger.
type CleanupLedger struct {
	mu      sync.Mutex
	running map[string]string
	kept    []string
}

func (l *CleanupLedger) started(runID, command string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.running == nil {
		l.running = map[string]string{}
	}
	l.running[runID] = command
}

// finished records that a run returned, keeping its removal command when it
// left objects behind.
func (l *CleanupLedger) finished(runID, command string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.running, runID)
	if command != "" && !slices.Contains(l.kept, command) {
		l.kept = append(l.kept, command)
	}
}

// Outstanding returns the removal commands of the runs that kept objects,
// then of the runs still in progress.
func (l *CleanupLedger) Outstanding() []string {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	out := slices.Clone(l.kept)
	running := make([]string, 0, len(l.running))
	for _, c := range l.running {
		running = append(running, c)
	}
	sort.Strings(running)
	return append(out, running...)
}

// clusterValidatorDetail points at the validator's transcript, or for a kept
// Job that was suspended, whose pod is gone, at the Job.
func clusterValidatorDetail(kubeContext string, result ClusterValidatorResult) string {
	if result.Suspended && result.JobName != "" {
		return fmt.Sprintf("inspect: kubectl%s describe -n %s job/%s",
			kubectlContextArg(kubeContext), clusterValidatorNamespace, result.JobName)
	}
	hint := kubectlLogsHint(kubeContext, result.JobName)
	if hint == "" {
		return ""
	}
	return "logs: " + hint
}

// buildRegistryCredentialCategory returns the registry-credentials category
// that probes each configured registry for reachability and valid credentials.
// A registry listed twice, for two repository scopes, gets one row per scope.
func buildRegistryCredentialCategory(cfg PreflightConfig) categorySpec {
	cat := categorySpec{
		name:   CategoryRegistryCredentials,
		role:   RoleLocalOnly, // runs regardless of cluster role
		checks: make([]binaryCheckSpec, 0, len(cfg.Registries)),
	}
	seen := map[string]bool{}
	for _, reg := range cfg.Registries {
		label := reg.Registry
		if seen[label] {
			label += "/" + reg.RepoHint
		}
		seen[reg.Registry] = true
		cat.checks = append(cat.checks, registryCredentialCheck(cfg.RegistryChecker, reg, label, cfg.RegistryPostInstall))
	}
	return cat
}

// registryCredentialCheck returns a binaryCheckSpec that probes one registry.
// Only a credential the registry rejected can fail: at error severity for a
// critical registry, at warning for any other. A rejection on a post-install
// run is a warning too: it is this machine's credential, and the cluster
// pulls with its own pull secret. Everything else the probe can report, from
// an unreachable registry to a missing local credential, is a warning or an
// informational pass.
func registryCredentialCheck(
	checker RegistryCredentialChecker, entry RegistryEntry, label string, postInstall bool,
) binaryCheckSpec {
	id := "registry-cred-" + label
	severity := SeverityWarning
	if entry.Critical {
		severity = SeverityError
	}
	return binaryCheckSpec{
		ID:         id,
		HumanLabel: fmt.Sprintf("checking credentials for %s...", label),
		Run: func(ctx context.Context) CheckResult {
			r := CheckResult{ID: id, Severity: severity}
			err := checker(ctx, entry.Registry, entry.RepoHint, entry.Critical)
			if err == nil {
				r.Passed = true
				r.Severity = SeverityInfo
				r.Message = label + ": credentials valid"
				return r
			}
			var outcome registryProbeOutcome
			if !errors.As(err, &outcome) {
				// The run's context ended mid-probe: nothing was learned.
				r.Message = label + ": " + err.Error()
				r.Err = err
				return r
			}
			r.Message = label + ": " + outcome.detail
			switch outcome.kind {
			case probeSkipped:
				r.Passed = true
				r.Severity = SeverityInfo
				r.Message = label + ": skipped (" + outcome.detail + ")"
			case probeAnonymous, probeNotVerified, probeLoginRejected:
				r.Passed = true
				r.Severity = SeverityInfo
			case probeNoCredential, probeUnverifiable:
				r.Severity = SeverityWarning
				r.Err = err
			case probeRejected:
				r.Err = err
				if postInstall {
					r.Severity = SeverityWarning
					r.Message += "; the cluster pulls with its own pull secret, so this affects only this machine"
				}
			}
			return r
		},
	}
}

// mergeNamespaces returns the union of namespace lists, order-stable and
// de-duplicated, without the namespaces NVCA creates at runtime.
func mergeNamespaces(lists ...[]string) []string {
	var out []string
	seen := make(map[string]bool)
	for _, list := range lists {
		for _, ns := range list {
			if ns == "" || seen[ns] || runtimeOwnedNamespaces[ns] {
				continue
			}
			seen[ns] = true
			out = append(out, ns)
		}
	}
	return out
}

// staleNamespaceCheck detects NVCF namespaces stuck Terminating, holding a
// Helm release left mid-operation, or left holding install data after a
// partial teardown. Only a namespace stuck Terminating is an error; the
// others, and a probe that could not read the cluster, are warnings.
func staleNamespaceCheck(prober StaleNamespaceProber, kubeContext string, namespaces []string) binaryCheckSpec {
	const id = "stale-namespaces"
	return binaryCheckSpec{
		ID:         id,
		HumanLabel: "checking for stale NVCF namespaces...",
		Run: func(ctx context.Context) CheckResult {
			r := CheckResult{ID: id, Severity: SeverityWarning}
			// Resolve the context before probing, then probe that exact name.
			// Resolving afterwards leaves a window where the current-context
			// changes in between, which would have the hints act on a
			// cluster other than the one that was read.
			probedContext := effectiveKubeContext(kubeContext)
			stale, err := prober(ctx, probedContext, namespaces)
			var unreachable *ClusterUnreachableError
			if errors.As(err, &unreachable) {
				// Nothing about this cluster was checked, so nothing passes.
				r.Severity = SeverityError
				r.Message = unreachable.Error()
				r.Err = err
				return r
			}
			if len(stale) == 0 {
				if err != nil {
					r.Err = err
					r.Message = "stale namespace probe failed: " + err.Error()
					return r
				}
				r.Severity = SeverityInfo
				r.Passed = true
				r.Message = "no stale NVCF namespaces detected"
				return r
			}
			// Every hint names the context that was actually probed, above.
			// In split mode the two callers pass different contexts, and with
			// no context flag the probe followed the current-context. Either
			// way the name goes into the command, because the current-context
			// can change between reading the output and pasting it.
			kctl := "kubectl" + kubectlContextArg(probedContext)
			parts := make([]string, 0, len(stale))
			var hints []string
			for _, ns := range stale {
				part := ns.Name + " (" + ns.Reason
				if ns.Detail != "" {
					part += ": " + ns.Detail
				}
				parts = append(parts, part+")")
				switch ns.Reason {
				case StaleStuckTerminating:
					// Positive evidence found before a later read failed
					// still blocks the run.
					r.Severity = SeverityError
				case StaleTerminating:
					// A normal deletion finishes by itself; --wait polls it.
					r.Transient = true
				}
				hints = append(hints, staleNamespaceHints(ns, kctl, probedContext)...)
			}
			r.Message = fmt.Sprintf("%d stale namespace(s) detected: %s. %s",
				len(stale), strings.Join(parts, ", "), strings.Join(hints, "; "))
			if err != nil {
				r.Message += ". Additionally could not probe: " + err.Error()
			}
			// The error reaches a warning row, so a budget that ran out
			// mid-scan grades it as cut short rather than as the warnings
			// found so far. A stuck namespace is a finding however the scan
			// ended: graded as cut short it would lose its name and exit 5.
			if r.Severity != SeverityError {
				r.Err = err
			}
			r.Transient = r.Transient && r.Severity == SeverityWarning
			return r
		},
	}
}

// staleNamespaceHints returns the commands for one reported namespace, one
// per namespace with the real name substituted. A `<ns>` placeholder is not
// pasteable: the shell reads `<` and `>` as redirection. kubectl also keeps
// only the last -n it sees, so a joined command inspects only one namespace.
//
// Every kind is enumerated, not `get all`: that omits PVCs, Secrets,
// ConfigMaps and custom resources, so after a partial teardown it reports
// "No resources found" over the Cassandra volumes and the OpenBao unseal
// Secret.
func staleNamespaceHints(ns StaleNamespace, kctl, kubeContext string) []string {
	inspect := fmt.Sprintf("%s api-resources --verbs=list --namespaced -o name | "+
		"xargs -n1 %s get -n %s --show-kind --ignore-not-found", kctl, kctl, ns.Name)
	conditions := fmt.Sprintf("%s get ns %s -o jsonpath='{.status.conditions}'", kctl, ns.Name)
	switch ns.Reason {
	case StaleStuckTerminating:
		// Inspect first, force last. A namespace usually stays Terminating
		// because an object inside it still has a finalizer; clearing the
		// namespace's own finalizers skips that object's cleanup and can
		// orphan what it manages, such as a cloud load balancer or volume.
		// spec.finalizers is writable only through the /finalize
		// subresource: a plain patch is silently reverted by the
		// apiserver's namespace strategy.
		return []string{
			fmt.Sprintf("see why %s is held in Terminating: %s", ns.Name, conditions),
			fmt.Sprintf("find what is holding %s in Terminating: %s", ns.Name, inspect),
			fmt.Sprintf("only if nothing inside %s can be cleaned up, force-clear its finalizers: "+
				"%s get ns %s -o json | jq '.spec.finalizers=[]' | "+
				"%s replace --raw /api/v1/namespaces/%s/finalize -f -",
				ns.Name, kctl, ns.Name, kctl, ns.Name),
		}
	case StaleTerminating:
		return []string{fmt.Sprintf("%s is still being deleted; rerun the check once it is gone, "+
			"or see what it is waiting on: %s", ns.Name, conditions)}
	case StaleReleaseMidOperation:
		// An install or teardown still running leaves the same records, so
		// the hint reads the history before anything is changed.
		helm := "helm" + helmContextArg(kubeContext)
		var hints []string
		for _, release := range ns.Releases {
			hints = append(hints, fmt.Sprintf("if no install or teardown is running, read the history of %s "+
				"with %s history %s -n %s, then finish it with helm rollback or helm uninstall",
				release, helm, release, ns.Name))
		}
		return hints
	default:
		// An observation, not a remediation. A namespace with no Helm
		// release is not necessarily stale: an operator who installs a
		// gated component the documented upstream way, or via Argo, owns a
		// healthy namespace with no owner=helm object.
		return []string{fmt.Sprintf("%s holds volume claims or workload objects but no Helm release; inspect: %s",
			ns.Name, inspect)}
	}
}

// helmContextArg renders the probed context as a --kube-context flag, or ""
// when no explicit context was given.
func helmContextArg(kubeContext string) string {
	if kubeContext == "" {
		return ""
	}
	return " --kube-context " + shellQuoteArg(kubeContext)
}

// kubectlContextArg renders the probed context as a --context flag for the
// remediation hints, or "" when no explicit context was given so the hint stays
// readable for a single-cluster setup. The value is shell-quoted: a context
// name is operator-supplied and arbitrary.
func kubectlContextArg(kubeContext string) string {
	if kubeContext == "" {
		return ""
	}
	return " --context " + shellQuoteArg(kubeContext)
}

// shellQuoteArg makes s safe to paste into a shell. Unquoted when it holds only
// characters no shell treats specially, so the common case stays legible.
func shellQuoteArg(s string) string {
	if s == "" {
		return "''"
	}
	safe := strings.IndexFunc(s, func(r rune) bool {
		return !(r == '-' || r == '_' || r == '.' || r == '/' || r == ':' || r == '@' || r == '=' ||
			(r >= '0' && r <= '9') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= 'a' && r <= 'z'))
	}) == -1
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

// sisReachabilityCheck does an HTTP GET on /v1/health and asserts < 5xx.
// Tagged so the renderer groups it under "compute-plane-cluster".
func sisReachabilityCheck(sisURL string) binaryCheckSpec {
	return binaryCheckSpec{
		ID:         "sis-reachability",
		HumanLabel: "probing SIS reachability…",
		Run: func(ctx context.Context) CheckResult {
			cli := &http.Client{Timeout: 5 * time.Second}
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(sisURL, "/")+"/v1/health", nil)
			if err != nil {
				return CheckResult{
					ID: "sis-reachability", Severity: SeverityError, Passed: false,
					Message: "SIS request build failed: " + err.Error(),
					HintURL: "https://docs.nvidia.com/nvcf/self-hosted/troubleshooting#sis-reachability",
				}
			}
			resp, err := cli.Do(req)
			if err != nil {
				return CheckResult{
					ID: "sis-reachability", Severity: SeverityError, Passed: false,
					Message: "SIS unreachable: " + err.Error(),
					HintURL: "https://docs.nvidia.com/nvcf/self-hosted/troubleshooting#sis-reachability",
					Err:     err,
				}
			}
			defer resp.Body.Close()
			if resp.StatusCode >= 500 {
				return CheckResult{
					ID: "sis-reachability", Severity: SeverityError, Passed: false,
					Message: fmt.Sprintf("SIS returned %d", resp.StatusCode),
					HintURL: "https://docs.nvidia.com/nvcf/self-hosted/troubleshooting#sis-reachability",
				}
			}
			return CheckResult{
				ID: "sis-reachability", Severity: SeverityInfo, Passed: true,
				Message: fmt.Sprintf("SIS reachable (%d)", resp.StatusCode),
			}
		},
	}
}

// RunPreflightForRole runs the shared local-host checks plus role-specific
// cluster-side checks. Use ctx for cancellation and sink for streaming events.
//
// The orchestrator calls this twice in split-cluster mode (RoleControlPlane
// + RoleComputePlane in parallel) and once in single-cluster mode.
func RunPreflightForRole(ctx context.Context, cfg PreflightConfig, role Role, rc RoleConfig, sink progress.EventSink) []CheckResult {
	return runPreflightImpl(ctx, cfg, role, rc, sink)
}

// RunPreflightStreaming preserves the M+8.J entry point. Equivalent to
// RoleLocalOnly when cfg.LocalOnly is set, else RoleLocalOnly so the existing
// single-cluster up flow keeps current behavior. Tests + callers that don't
// yet plumb roles use this.
func RunPreflightStreaming(ctx context.Context, cfg PreflightConfig, sink progress.EventSink) []CheckResult {
	return runPreflightImpl(ctx, cfg, RoleLocalOnly, RoleConfig{}, sink)
}

// checkProbeShare bounds the checks a role runs before its validator, and is
// the whole budget of a role without one. A var so tests can shorten it.
var checkProbeShare = 2 * time.Minute

// CheckProbeShare is the time each role sets aside for the checks before its
// validator. RunPreflightForRole enforces it, so however slow those probes are
// the validator still gets the time the run's budget sized it for.
func CheckProbeShare() time.Duration { return checkProbeShare }

// localCheckShare bounds the checks that run on this machine: the local tools
// and the registry probes. They get their own share so a slow registry cannot
// spend the time set aside for the cluster checks. A var so tests can shorten
// it.
var localCheckShare = 90 * time.Second

// LocalCheckShare is the time each role invocation sets aside for the checks
// that run on this machine, before its cluster checks.
func LocalCheckShare() time.Duration { return localCheckShare }

func runPreflightImpl(ctx context.Context, cfg PreflightConfig, role Role, rc RoleConfig, sink progress.EventSink) []CheckResult {
	var all []CheckResult
	interrupted := func() bool {
		return errors.Is(ctx.Err(), context.Canceled) || (cfg.Interrupted != nil && cfg.Interrupted())
	}
	localCtx, cancelLocal := context.WithTimeout(ctx, localCheckShare)
	defer cancelLocal()
	// The cluster share starts with the first cluster category, so the local
	// checks before it cannot spend it. The probes end early enough for a
	// probe pod's cleanup, which outlives its probe, to finish inside it.
	clusterShare := &lazyTimeout{parent: ctx, d: checkProbeShare - probePodCleanupTimeout}
	defer clusterShare.stop()

	categories := buildCategories(cfg, role, rc)
	for _, cat := range categories {
		shareCtx := localCtx
		if cat.role != RoleLocalOnly {
			shareCtx = clusterShare.get()
		}
		catStart := time.Now()
		var catResults []CheckResult
		for _, spec := range cat.checks {
			checkCtx := shareCtx
			if spec.ownBudget {
				checkCtx = ctx
			}
			var res CheckResult
			switch {
			case interrupted():
				// Interrupted: the command reports the interrupt, not rows.
				return all
			case checkCtx.Err() != nil && !spec.static:
				// The budget ran out before this check started. Dropping it
				// let the partial set grade as the verdict, so a validator
				// that never ran could pass the gate.
				res = CheckResult{
					ID: spec.ID, Severity: SeverityError, CutShort: true,
					Message: "not run: the check's time budget ran out before it started",
					Err:     checkCtx.Err(),
				}
			default:
				_ = sink.Emit(ctx, progress.CheckStarted{
					Category: cat.name,
					ID:       spec.ID,
					Message:  spec.HumanLabel,
				})
				res = spec.Run(checkCtx)
				if interrupted() {
					// Cut short by the interrupt, so its result is not a
					// finding; the cancelled final event says what happened.
					// A row that says how to remove what the check left in
					// the cluster is still reported: nothing else will.
					if res.Cleanup != "" {
						res.Category = cat.name
						all = append(all, res)
						emitCheckCompleted(sink, cat.name, res)
					}
					return all
				}
				res = normaliseResult(checkCtx, res)
			}
			res.Category = cat.name
			all = append(all, res)
			emitCheckCompleted(sink, cat.name, res)
			catResults = append(catResults, res)
		}
		passed, failed, warned := CountResults(catResults)
		_ = sink.Emit(ctx, progress.CategoryCompleted{
			Category:     cat.name,
			PassedCount:  passed,
			FailedCount:  failed,
			WarningCount: warned,
			DurationSec:  time.Since(catStart).Seconds(),
		})
	}
	return all
}

// lazyTimeout is a context whose timeout starts on first use.
type lazyTimeout struct {
	parent context.Context
	d      time.Duration
	ctx    context.Context
	cancel context.CancelFunc
}

func (l *lazyTimeout) get() context.Context {
	if l.ctx == nil {
		l.ctx, l.cancel = context.WithTimeout(l.parent, l.d)
	}
	return l.ctx
}

func (l *lazyTimeout) stop() {
	if l.cancel != nil {
		l.cancel()
	}
}

// normaliseResult applies the rules every row shares. A pass is info, whatever
// severity the check set before it knew the outcome. A miss caused by the
// spent budget, which ctx carries, is cut short: no finding either way. A
// check whose own bound fired keeps its result.
func normaliseResult(ctx context.Context, res CheckResult) CheckResult {
	if res.Passed {
		res.Severity = SeverityInfo
		return res
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) && errors.Is(res.Err, context.DeadlineExceeded) {
		res.CutShort = true
		res.Severity = SeverityError
		// Replaced, not wrapped: the check's own advice is about a failure
		// it did not get to observe.
		res.Message = "cut short: the check's time budget ran out before it finished"
	}
	return res
}

// emitCheckCompleted reports one check's result. It uses a fresh context: the
// run's own may have ended, and the row must still reach the stream.
func emitCheckCompleted(sink progress.EventSink, category string, res CheckResult) {
	_ = sink.Emit(context.Background(), progress.CheckCompleted{
		Category:  category,
		ID:        res.ID,
		Passed:    res.Passed,
		Severity:  res.Severity,
		Message:   res.Message,
		Detail:    res.Detail,
		HintURL:   res.HintURL,
		Transient: res.Transient,
		Cleanup:   res.Cleanup,
	})
}

// noopSink is a progress.EventSink that discards all events. Used by RunPreflight
// to delegate to RunPreflightStreaming without streaming overhead for callers that
// only need the final result slice.
type noopSink struct{}

func (*noopSink) Emit(context.Context, progress.Event) error { return nil }
func (*noopSink) Close() error                               { return nil }

// RunPreflight executes the role-independent pre-flight checks (local tools
// and registry credentials) and returns the result slice. It is a thin
// wrapper around RunPreflightStreaming with a no-op sink, kept for
// orchestrator callers (runUpPreflight in cmd/self_hosted_up.go) that do not
// need streaming.
func RunPreflight(ctx context.Context, cfg PreflightConfig) []CheckResult {
	return RunPreflightStreaming(ctx, cfg, &noopSink{})
}

// ControlPlaneStaleNamespaces and ComputePlaneStaleNamespaces expose each
// role's stale-namespace list so the cmd layer can merge the two when a single
// cluster hosts both roles and only one probe runs.
func ControlPlaneStaleNamespaces(stackDir, env string) []string {
	return resolveStackNamespaces(stackDir, env, nvcfControlPlaneNamespaces)
}

func ComputePlaneStaleNamespaces(stackDir, env string) []string {
	return resolveStackNamespaces(stackDir, env, nvcfComputePlaneNamespaces)
}
