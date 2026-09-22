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
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"

	"nvcf-cli/internal/selfhosted/progress"
)

const (
	// versionProbeTimeout is the per-binary timeout for each version probe attempt.
	versionProbeTimeout = 15 * time.Second

	// versionProbeAttempts allows one transient process failure before preflight
	// declares a present tool unusable.
	versionProbeAttempts = 2

	binaryVersionMessage = "%s %s on PATH (%s required)"
)

// Severity values for CheckResult. Typed constants rather than bare strings:
// a typo'd "Error" silently downgraded a hard failure to a warning, and the
// exit code keys off an exact match.
const (
	SeverityInfo    = "info"
	SeverityWarning = "warning"
	SeverityError   = "error"
)

// One row in the linkerd-style output. Logs is internal-only and is not
// forwarded into the CheckCompleted JSONL wire event, so it never leaks
// into the stable JSON contract.
type CheckResult struct {
	ID       string
	Category string
	Severity string // "info" | "warning" | "error"
	Passed   bool
	Message  string
	Detail   string // optional: short version string or extra context (M+8.11)
	HintURL  string
	Err      error  // populated only when the check itself failed to execute
	Logs     string // optional: full check transcript for --show-logs; not emitted to JSON
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
		Severity: "error",
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

	// Registries is the list of container registries to credential-check.
	// When empty, the registry-credentials category is omitted entirely.
	// Populated by the cmd layer from EnumerateRegistries.
	Registries []RegistryEntry

	// RegistryChecker validates credentials for one registry. Nil skips the
	// category. Production wires NewRegistryCredentialChecker; tests pass fakes.
	RegistryChecker RegistryCredentialChecker
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
	RoleComputePlane             // shared + GPU operator + GPU node labels + (optional) SIS reachability
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

	// Nil skips the cluster-validator check. The cmd layer emits a stderr
	// notice when the operator explicitly opted out; the check schema
	// stays clean so JSON consumers don't see a misleading "passed=true"
	// row for a probe that never actually ran.
	ClusterValidator           ClusterValidator
	ClusterValidatorImage      string
	ClusterValidatorPullSecret string
	ClusterValidatorNoCleanup  bool

	// StaleNamespaceProber detects NVCF stack namespaces that are stuck
	// Terminating or exist as empty shells after a partial teardown. Nil skips
	// the check; production wires NewStaleNamespaceProber; tests pass fakes.
	StaleNamespaceProber StaleNamespaceProber

	// StackDir is the resolved stack checkout. When set, the namespaces to
	// probe are read from its helmfile.d rather than the static fallback list,
	// so the check follows the stack instead of drifting from it.
	StackDir string

	// ExtraStaleNamespaces are merged into this role's stale-namespace scan.
	// In ModeSingle both roles share one cluster, so only one of them runs the
	// probe to avoid emitting the same check ID twice; the other role's
	// namespaces are passed here instead of being dropped. The two roles probe
	// disjoint lists, so without this the skipped role's namespaces are never
	// scanned on the one topology where they sit on the same cluster.
	ExtraStaleNamespaces []string

	// ClusterValidatorRegistries is an optional list of "host:port" registry
	// endpoints added to the control-plane validator ConfigMap alongside the
	// built-in nvcr.io probe. Ignored for the compute-plane validator.
	ClusterValidatorRegistries []string
}

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

	if cfg.LocalOnly || role == RoleLocalOnly {
		return out
	}

	switch role {
	case RoleControlPlane:
		out = append(out, controlPlaneCheckCategory(rc))
	case RoleComputePlane:
		out = append(out, computePlaneCheckCategory(rc))
	}

	return out
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
	return &categorySpec{name: "local-host-tools", role: RoleLocalOnly, checks: checks}
}

// controlPlaneCheckCategory returns the cluster-side checks for the control
// plane. The stale-namespace check runs first so a leftover namespace from a
// prior partial teardown is surfaced before any other cluster work.
// Gateway API CRD and StorageClass probes are placeholders pending M3.
func controlPlaneCheckCategory(rc RoleConfig) categorySpec {
	cat := categorySpec{
		name:   "control-plane-cluster",
		role:   RoleControlPlane,
		checks: []binaryCheckSpec{},
	}
	// Stale namespace check runs first so leftover namespaces surface
	// before any other cluster work.
	if rc.StaleNamespaceProber != nil {
		cat.checks = append(cat.checks,
			staleNamespaceCheck(rc.StaleNamespaceProber, rc.KubeContext,
				mergeNamespaces(resolveStackNamespaces(rc.StackDir, nvcfControlPlaneNamespaces),
					rc.ExtraStaleNamespaces)))
	}
	// Containerized cluster-validator probe for control-plane checks
	// (Gateway API CRDs, Envoy Gateway, StorageClass, external LB,
	// node-to-node overlay, and reachability to nvcr.io + extra registries).
	if rc.ClusterValidator != nil {
		cat.checks = append(cat.checks, clusterValidatorCheck(
			rc.ClusterValidator,
			rc.KubeContext,
			rc.ClusterValidatorImage,
			rc.ClusterValidatorPullSecret,
			rc.ClusterValidatorNoCleanup,
			validatorRoleControlPlane,
			rc.ClusterValidatorRegistries,
		))
	}
	return cat
}

// computePlaneCheckCategory returns the compute-plane checks. SIS, inotify,
// and cluster-validator probes are opt-in via their RoleConfig fields; a nil
// or empty field omits the corresponding check from the category.
func computePlaneCheckCategory(rc RoleConfig) categorySpec {
	cat := categorySpec{
		name: "compute-plane-cluster",
		role: RoleComputePlane,
		checks: []binaryCheckSpec{
			placeholderCheck("gpu-operator", "checking GPU operator…", "GPU operator probe — pending M+10 cluster-side preflight"),
			placeholderCheck("gpu-node-labels", "checking GPU node labels…", "GPU node-label probe — pending M+10 cluster-side preflight"),
		},
	}
	// Stale namespace check runs first so leftover namespaces from a prior
	// partial teardown surface before any other cluster work.
	if rc.StaleNamespaceProber != nil {
		cat.checks = append([]binaryCheckSpec{
			staleNamespaceCheck(rc.StaleNamespaceProber, rc.KubeContext,
				mergeNamespaces(resolveStackNamespaces(rc.StackDir, nvcfComputePlaneNamespaces),
					rc.ExtraStaleNamespaces)),
		}, cat.checks...)
	}
	if rc.SISURL != "" {
		cat.checks = append(cat.checks, sisReachabilityCheck(rc.SISURL))
	}
	if rc.InotifyProber != nil {
		cat.checks = append(cat.checks, nodeInotifyCheck(rc.InotifyProber, rc.KubeContext))
	}
	if rc.ClusterValidator != nil {
		cat.checks = append(cat.checks, clusterValidatorCheck(
			rc.ClusterValidator,
			rc.KubeContext,
			rc.ClusterValidatorImage,
			rc.ClusterValidatorPullSecret,
			rc.ClusterValidatorNoCleanup,
			validatorRoleComputePlane,
			nil, // registries: compute-plane doesn't use the ConfigMap reachability list
		))
	}
	return cat
}

// Required minimum inotify limits per
// docs/user/cluster-management/self-managed.md#node-inotify-limits.
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
				Severity: "error",
				HintURL:  inotifyHintURL,
			}
			limits, err := prober(ctx, kubeContext)
			if err != nil {
				r.Severity = "warning"
				r.Message = "node inotify probe failed: " + err.Error()
				r.Err = err
				return r
			}
			if len(limits) == 0 {
				r.Severity = "warning"
				r.Passed = true
				r.Message = "no nodes returned by inotify probe; skipping"
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
				r.Severity = "warning"
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

// clusterValidatorCheck runs the validator Job. Runner errors (RBAC/timeout)
// are warning severity; validator failures are error. role selects the check
// set via VALIDATOR_ROLE; registries extends the ConfigMap reachability list.
func clusterValidatorCheck(cv ClusterValidator, kubeContext, image, pullSecret string, noCleanup bool, role string, registries []string) binaryCheckSpec {
	const id = "cluster-validator"
	return binaryCheckSpec{
		ID:         id,
		HumanLabel: "running cluster-validator probe…",
		Run: func(ctx context.Context) CheckResult {
			r := CheckResult{
				ID:      id,
				HintURL: clusterValidatorHintURL,
			}
			result := cv(ctx, ClusterValidatorParams{
				KubeContext: kubeContext,
				Image:       image,
				PullSecret:  pullSecret,
				NoCleanup:   noCleanup,
				Role:        role,
				Registries:  registries,
			})
			r.Logs = result.Logs
			r.Detail = clusterValidatorDetail(kubeContext, result.JobName)

			if result.Err != nil {
				r.Severity = "warning"
				r.Message = "cluster-validator did not complete: " + result.Err.Error()
				r.Err = result.Err
				return r
			}
			r.Passed = result.Passed
			if result.Passed {
				r.Severity = "info"
				r.Message = "cluster passed cluster-validator built-in checks"
				return r
			}
			r.Severity = "error"
			r.Message = fmt.Sprintf("cluster-validator reported failures (exit code %d)", result.ExitCode)
			return r
		},
	}
}

func clusterValidatorDetail(kubeContext, jobName string) string {
	hint := kubectlLogsHint(kubeContext, jobName)
	if hint == "" {
		return ""
	}
	return "logs: " + hint
}

// buildRegistryCredentialCategory returns the registry-credentials category
// that probes each configured registry for reachability and valid credentials.
func buildRegistryCredentialCategory(cfg PreflightConfig) categorySpec {
	cat := categorySpec{
		name:   "registry-credentials",
		role:   RoleLocalOnly, // runs regardless of cluster role
		checks: make([]binaryCheckSpec, 0, len(cfg.Registries)),
	}
	for _, reg := range cfg.Registries {
		reg := reg // capture loop var
		cat.checks = append(cat.checks, registryCredentialCheck(cfg.RegistryChecker, reg))
	}
	return cat
}

// registryCredentialCheck returns a binaryCheckSpec that probes one registry.
// Critical registries (nvcr.io) fail at error severity; non-critical ones
// fail at warning so they don't block the operator on optional registries.
func registryCredentialCheck(checker RegistryCredentialChecker, entry RegistryEntry) binaryCheckSpec {
	id := "registry-cred-" + entry.Registry
	severity := "warning"
	if entry.Critical {
		severity = "error"
	}
	return binaryCheckSpec{
		ID:         id,
		HumanLabel: fmt.Sprintf("checking credentials for %s...", entry.Registry),
		Run: func(ctx context.Context) CheckResult {
			r := CheckResult{
				ID:       id,
				Severity: severity,
			}
			if err := checker(ctx, entry.Registry, entry.RepoHint, entry.Critical); err != nil {
				// A registry this probe cannot speak to (ECR's SigV4, a Basic
				// challenge) is not evidence of a credential problem, and an
				// unreadable credential helper is not evidence of a missing
				// credential. Neither may block the run.
				var skipped errRegistryProbeSkipped
				var unverified errRegistryCredentialsUnverified
				switch {
				case errors.As(err, &skipped):
					r.Passed = true
					r.Severity = "info"
					r.Message = entry.Registry + ": skipped (" + skipped.reason + ")"
					return r
				case errors.As(err, &unverified):
					r.Severity = "warning"
					r.Message = entry.Registry + ": " + err.Error()
					r.Err = err
					return r
				}
				r.Message = entry.Registry + ": " + err.Error()
				r.Err = err
				return r
			}
			r.Passed = true
			r.Severity = "info"
			r.Message = entry.Registry + ": credentials valid"
			return r
		},
	}
}

func mergeNamespaces(base, extra []string) []string {
	if len(extra) == 0 {
		return base
	}
	seen := make(map[string]bool, len(base)+len(extra))
	out := make([]string, 0, len(base)+len(extra))
	for _, list := range [][]string{base, extra} {
		for _, ns := range list {
			if ns == "" || seen[ns] {
				continue
			}
			seen[ns] = true
			out = append(out, ns)
		}
	}
	return out
}

// staleNamespaceCheck detects NVCF namespaces stuck Terminating or left as
// empty shells after a partial teardown. Severity is error; prober errors
// degrade to warning so transient kubeconfig issues don't falsely fail.
// mergeNamespaces returns the union of two namespace lists, order-stable and
// de-duplicated.
func staleNamespaceCheck(prober StaleNamespaceProber, kubeContext string, namespaces []string) binaryCheckSpec {
	const id = "stale-namespaces"
	return binaryCheckSpec{
		ID:         id,
		HumanLabel: "checking for stale NVCF namespaces...",
		Run: func(ctx context.Context) CheckResult {
			r := CheckResult{ID: id, Severity: "error"}
			// Resolve the context before probing, then probe that exact name.
			// Resolving afterwards leaves a window where the current-context
			// changes in between, which would have the hints delete namespaces
			// in a cluster other than the one that was read.
			probedContext := effectiveKubeContext(kubeContext)
			stale, err := prober(ctx, probedContext, namespaces)
			if err != nil {
				r.Severity = "warning"
				r.Message = "stale namespace probe failed: " + err.Error()
				r.Err = err
				return r
			}
			if len(stale) == 0 {
				r.Severity = "info"
				r.Passed = true
				r.Message = "no stale NVCF namespaces detected"
				return r
			}
			parts := make([]string, 0, len(stale))
			var terminating, emptyShell []string
			for _, ns := range stale {
				parts = append(parts, ns.Name+" ("+ns.Reason+")")
				if ns.Reason == "stuck Terminating" {
					terminating = append(terminating, ns.Name)
				} else {
					emptyShell = append(emptyShell, ns.Name)
				}
			}
			// Every hint names the context that was actually probed, above.
			// In split mode the two callers pass different contexts, and with
			// no context flag the probe followed the current-context. Either
			// way the name goes into the command, because these hints delete
			// namespaces and the current-context can change between reading
			// the output and pasting it.
			kctl := "kubectl" + kubectlContextArg(probedContext)
			var hints []string
			if len(terminating) > 0 {
				// spec.finalizers is writable only through the /finalize
				// subresource: a plain patch or update is silently reverted by
				// the apiserver's namespace strategy, so `kubectl patch ...
				// --type=merge` prints "patched" and changes nothing.
				for _, ns := range terminating {
					hints = append(hints, fmt.Sprintf(
						"clear namespace finalizers on %s: %s get ns %s -o json | "+
							"jq '.spec.finalizers=[]' | %s replace --raw /api/v1/namespaces/%s/finalize -f -",
						ns, kctl, ns, kctl, ns))
				}
				// One command per namespace with the real name substituted. A
				// `<ns>` placeholder is not pasteable: the shell reads `<` and
				// `>` as redirection, so the command fails before kubectl runs.
				for _, ns := range terminating {
					hints = append(hints, fmt.Sprintf(
						"if %s stays Terminating, the deadlock is on objects inside it: "+
							"%s api-resources --verbs=list --namespaced -o name | "+
							"xargs -n1 %s get -n %s --show-kind --ignore-not-found",
						ns, kctl, kctl, ns))
				}
			}
			if len(emptyShell) > 0 {
				// Deliberately not a delete command. A namespace with no Helm
				// release is not necessarily stale: the stack gates
				// cert-manager, NATS, OpenBao and Cassandra on *.enabled, so an
				// operator who installs one the documented upstream way, or via
				// Argo, owns a healthy namespace with no owner=helm object.
				// Handing them "kubectl delete namespace cert-manager" would
				// destroy every Certificate and Issuer in the cluster.
				hints = append(hints,
					fmt.Sprintf("inspect these and remove them only after confirming they are unused: %s get all -n %s",
						kctl, strings.Join(emptyShell, " -n ")))
			}
			// Only a namespace stuck Terminating blocks the run. "No Helm
			// release" is a heuristic over a conditionally-installed stack, so
			// it warns rather than turning a supported install shape into exit 2.
			if len(terminating) == 0 {
				r.Severity = SeverityWarning
			}
			r.Message = fmt.Sprintf("%d stale namespace(s) detected: %s. To resolve: %s",
				len(stale), strings.Join(parts, ", "), strings.Join(hints, "; "))
			return r
		},
	}
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

// placeholderCheck returns a binaryCheckSpec that emits a passing "info"
// CheckResult with the given message. Used until M3/M+10 cluster-side probes ship.
func placeholderCheck(id, label, message string) binaryCheckSpec {
	return binaryCheckSpec{
		ID:         id,
		HumanLabel: label,
		Run: func(_ context.Context) CheckResult {
			return CheckResult{
				ID:       id,
				Severity: "info",
				Passed:   true,
				Message:  message,
			}
		},
	}
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
					ID: "sis-reachability", Severity: "error", Passed: false,
					Message: "SIS request build failed: " + err.Error(),
					HintURL: "https://docs.nvidia.com/nvcf/self-hosted/troubleshooting#sis-reachability",
				}
			}
			resp, err := cli.Do(req)
			if err != nil {
				return CheckResult{
					ID: "sis-reachability", Severity: "error", Passed: false,
					Message: "SIS unreachable: " + err.Error(),
					HintURL: "https://docs.nvidia.com/nvcf/self-hosted/troubleshooting#sis-reachability",
				}
			}
			defer resp.Body.Close()
			if resp.StatusCode >= 500 {
				return CheckResult{
					ID: "sis-reachability", Severity: "error", Passed: false,
					Message: fmt.Sprintf("SIS returned %d", resp.StatusCode),
					HintURL: "https://docs.nvidia.com/nvcf/self-hosted/troubleshooting#sis-reachability",
				}
			}
			return CheckResult{
				ID: "sis-reachability", Severity: "info", Passed: true,
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

func runPreflightImpl(ctx context.Context, cfg PreflightConfig, role Role, rc RoleConfig, sink progress.EventSink) []CheckResult {
	var all []CheckResult

	categories := buildCategories(cfg, role, rc)
	for _, cat := range categories {
		catStart := time.Now()
		var passed, failed int
		for _, spec := range cat.checks {
			if ctx.Err() != nil {
				return all
			}
			_ = sink.Emit(ctx, progress.CheckStarted{
				Category: cat.name,
				ID:       spec.ID,
				Message:  spec.HumanLabel,
			})
			res := spec.Run(ctx)
			res.Category = cat.name
			all = append(all, res)
			_ = sink.Emit(ctx, progress.CheckCompleted{
				Category: cat.name,
				ID:       res.ID,
				Passed:   res.Passed,
				Severity: res.Severity,
				Message:  res.Message,
				Detail:   res.Detail,
				HintURL:  res.HintURL,
			})
			if res.Passed {
				passed++
			} else {
				failed++
			}
		}
		_ = sink.Emit(ctx, progress.CategoryCompleted{
			Category:    cat.name,
			PassedCount: passed,
			FailedCount: failed,
			DurationSec: time.Since(catStart).Seconds(),
		})
	}
	return all
}

// noopSink is a progress.EventSink that discards all events. Used by RunPreflight
// to delegate to RunPreflightStreaming without streaming overhead for callers that
// only need the final result slice.
type noopSink struct{}

func (*noopSink) Emit(context.Context, progress.Event) error { return nil }
func (*noopSink) Close() error                               { return nil }

// RunPreflight executes local-host (and optionally cluster-side) pre-flight
// checks and returns the result slice. It is a thin wrapper around
// RunPreflightStreaming with a no-op sink, kept for orchestrator callers
// (runUpPreflight in cmd/self_hosted_up.go) that do not need streaming.
// Cluster-side checks land in M3 (need a kubectl client wired up).
func RunPreflight(ctx context.Context, cfg PreflightConfig) []CheckResult {
	return RunPreflightStreaming(ctx, cfg, &noopSink{})
}

// ControlPlaneStaleNamespaces and ComputePlaneStaleNamespaces expose each
// role's stale-namespace list so the cmd layer can merge the two when a single
// cluster hosts both roles and only one probe runs.
func ControlPlaneStaleNamespaces(stackDir string) []string {
	return resolveStackNamespaces(stackDir, nvcfControlPlaneNamespaces)
}

func ComputePlaneStaleNamespaces(stackDir string) []string {
	return resolveStackNamespaces(stackDir, nvcfComputePlaneNamespaces)
}
