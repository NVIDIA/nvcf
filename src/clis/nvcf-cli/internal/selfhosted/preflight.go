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

// CheckResult is the outcome of one pre-flight check (one row in the linkerd-style
// output). Mirrors the JSON schema from spec §6.2.
type CheckResult struct {
	ID       string
	Category string
	Severity string // "info" | "warning" | "error"
	Passed   bool
	Message  string
	Detail   string // optional: short version string or extra context (M+8.11)
	HintURL  string
	Err      error // populated only when the check itself failed to execute
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
			Name: "helm", MinVer: semver.MustParse("3.14.0"), MaxVerExclusive: semver.MustParse("4.0.0"),
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
	RoleControlPlane             // shared + Gateway API CRDs + default StorageClass
	RoleComputePlane             // shared + GPU operator + GPU node labels + (optional) SIS reachability
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
	if len(checks) == 0 {
		return nil
	}
	return &categorySpec{name: "local-host-tools", role: RoleLocalOnly, checks: checks}
}

// controlPlaneCheckCategory returns the placeholder cluster-side checks for
// control plane: Gateway API CRDs, default StorageClass. Real probes land
// when M3 ships; for now they emit an "info" CheckResult so the renderer
// matrix is observable.
func controlPlaneCheckCategory(_ RoleConfig) categorySpec {
	return categorySpec{
		name: "control-plane-cluster",
		role: RoleControlPlane,
		checks: []binaryCheckSpec{
			placeholderCheck("gateway-api-crds", "checking Gateway API CRDs…", "Gateway API CRD probe — pending M3 cluster-side preflight"),
			placeholderCheck("default-storageclass", "checking default StorageClass…", "Default StorageClass probe — pending M3 cluster-side preflight"),
		},
	}
}

// computePlaneCheckCategory returns the placeholder compute-plane checks.
// Conditionally adds an SIS reachability probe when RoleConfig.SISURL is set.
func computePlaneCheckCategory(rc RoleConfig) categorySpec {
	cat := categorySpec{
		name: "compute-plane-cluster",
		role: RoleComputePlane,
		checks: []binaryCheckSpec{
			placeholderCheck("gpu-operator", "checking GPU operator…", "GPU operator probe — pending M+10 cluster-side preflight"),
			placeholderCheck("gpu-node-labels", "checking GPU node labels…", "GPU node-label probe — pending M+10 cluster-side preflight"),
		},
	}
	if rc.SISURL != "" {
		cat.checks = append(cat.checks, sisReachabilityCheck(rc.SISURL))
	}
	return cat
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
