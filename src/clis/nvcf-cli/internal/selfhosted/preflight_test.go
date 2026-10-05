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
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nvcf-cli/internal/selfhosted/progress"
)

func TestCheckBinary_PresentAndInRange(t *testing.T) {
	r := checkBinary(context.Background(), BinarySpec{
		Name:     "go",
		MinVer:   semver.MustParse("1.0.0"),
		LookPath: func(name string) (string, error) { return "/usr/local/bin/go", nil },
		Version: func(_ context.Context, _ string) (*semver.Version, error) {
			return semver.MustParse("1.25.5"), nil
		},
	})
	require.NoError(t, r.Err)
	assert.True(t, r.Passed)
	assert.Contains(t, r.Message, "1.25.5")
}

func TestCheckBinary_MissingFromPath(t *testing.T) {
	r := checkBinary(context.Background(), BinarySpec{
		Name:     "helmfile",
		MinVer:   semver.MustParse("1.0.0"),
		LookPath: func(name string) (string, error) { return "", assert.AnError },
		HintURL:  "https://github.com/helmfile/helmfile#installation",
	})
	assert.False(t, r.Passed)
	assert.Equal(t, "helmfile not found on PATH", r.Message)
	assert.Equal(t, "https://github.com/helmfile/helmfile#installation", r.HintURL)
}

func TestCheckBinary_VersionTooLow(t *testing.T) {
	r := checkBinary(context.Background(), BinarySpec{
		Name:     "helm",
		MinVer:   semver.MustParse("3.14.0"),
		LookPath: func(name string) (string, error) { return "/usr/local/bin/helm", nil },
		Version: func(_ context.Context, _ string) (*semver.Version, error) {
			return semver.MustParse("3.10.0"), nil
		},
		HintURL: "https://helm.sh/docs/intro/install/",
	})
	assert.False(t, r.Passed)
	assert.Contains(t, r.Message, "3.10.0")
	assert.Contains(t, r.Message, ">= 3.14.0 required")
}

func TestCheckBinary_VersionTooHigh(t *testing.T) {
	r := checkBinary(context.Background(), BinarySpec{
		Name:            "helm",
		MinVer:          semver.MustParse("3.14.0"),
		MaxVerExclusive: semver.MustParse("4.0.0"),
		LookPath:        func(name string) (string, error) { return "/usr/local/bin/helm", nil },
		Version: func(_ context.Context, _ string) (*semver.Version, error) {
			return semver.MustParse("4.1.4"), nil
		},
		HintURL: "https://helm.sh/docs/intro/install/",
	})
	assert.False(t, r.Passed)
	assert.Contains(t, r.Message, "4.1.4")
	assert.Contains(t, r.Message, ">= 3.14.0 and < 4.0.0 required")
}

func TestDefaultTools_AllowsHelm4IndividualVersionCheck(t *testing.T) {
	var helm BinarySpec
	for _, tool := range DefaultTools() {
		if tool.Name == "helm" {
			helm = tool
			break
		}
	}
	require.Equal(t, "helm", helm.Name)
	helm.LookPath = func(string) (string, error) { return "/usr/local/bin/helm", nil }
	helm.Version = func(_ context.Context, _ string) (*semver.Version, error) {
		return semver.MustParse("4.0.5"), nil
	}

	r := checkBinary(context.Background(), helm)

	require.NoError(t, r.Err)
	assert.True(t, r.Passed)
	assert.Contains(t, r.Message, "helm 4.0.5")
}

func TestRunPreflight_Helm4RequiresCompatibleHelmfile(t *testing.T) {
	cfg := PreflightConfig{Tools: []BinarySpec{
		{
			Name: "helmfile", MinVer: semver.MustParse("1.0.0"),
			LookPath: func(string) (string, error) { return "/usr/local/bin/helmfile", nil },
			Version: func(_ context.Context, _ string) (*semver.Version, error) {
				return semver.MustParse("1.1.9"), nil
			},
		},
		{
			Name: "helm", MinVer: semver.MustParse("3.14.0"),
			LookPath: func(string) (string, error) { return "/usr/local/bin/helm", nil },
			Version: func(_ context.Context, _ string) (*semver.Version, error) {
				return semver.MustParse("4.0.5"), nil
			},
		},
	}}

	results := RunPreflight(context.Background(), cfg)

	var compat *CheckResult
	for i := range results {
		if results[i].ID == "local-host-tools-helm-runtime" {
			compat = &results[i]
			break
		}
	}
	require.NotNil(t, compat)
	assert.False(t, compat.Passed)
	require.Error(t, compat.Err)
	assert.Contains(t, compat.Message, "Helm 4.0.5 requires helmfile >= 1.5.0")
}

func TestRunPreflight_Helm4WithCompatibleHelmfilePasses(t *testing.T) {
	cfg := PreflightConfig{Tools: []BinarySpec{
		{
			Name: "helmfile", MinVer: semver.MustParse("1.0.0"),
			LookPath: func(string) (string, error) { return "/usr/local/bin/helmfile", nil },
			Version: func(_ context.Context, _ string) (*semver.Version, error) {
				return semver.MustParse("1.5.1"), nil
			},
		},
		{
			Name: "helm", MinVer: semver.MustParse("3.14.0"),
			LookPath: func(string) (string, error) { return "/usr/local/bin/helm", nil },
			Version: func(_ context.Context, _ string) (*semver.Version, error) {
				return semver.MustParse("4.0.5"), nil
			},
		},
	}}

	results := RunPreflight(context.Background(), cfg)

	var compat *CheckResult
	for i := range results {
		if results[i].ID == "local-host-tools-helm-runtime" {
			compat = &results[i]
			break
		}
	}
	require.NotNil(t, compat)
	assert.True(t, compat.Passed)
	assert.Equal(t, "helm4-compat", compat.Detail)
}

func TestCheckBinary_RetriesTransientVersionProbeFailure(t *testing.T) {
	attempts := 0
	r := checkBinary(context.Background(), BinarySpec{
		Name:     "helmfile",
		MinVer:   semver.MustParse("1.0.0"),
		LookPath: func(string) (string, error) { return "/usr/local/bin/helmfile", nil },
		Version: func(_ context.Context, _ string) (*semver.Version, error) {
			attempts++
			if attempts == 1 {
				return nil, fmt.Errorf("running /usr/local/bin/helmfile [version --output short]: signal: killed")
			}
			return semver.MustParse("1.5.0"), nil
		},
		HintURL: "https://github.com/helmfile/helmfile#installation",
	})

	require.NoError(t, r.Err)
	assert.True(t, r.Passed)
	assert.Equal(t, 2, attempts)
	assert.Equal(t, "1.5.0", r.Detail)
}

func TestDefaultToolsWithPreferredDirUsesPinnedBinary(t *testing.T) {
	binDir := t.TempDir()
	pinnedHelm := filepath.Join(binDir, "helm")
	require.NoError(t, os.WriteFile(pinnedHelm, []byte("#!/bin/sh\n"), 0o755))

	tools := DefaultToolsWithPreferredDir(binDir)
	var helm BinarySpec
	for _, tool := range tools {
		if tool.Name == "helm" {
			helm = tool
			break
		}
	}
	require.NotNil(t, helm.LookPath)
	path, err := helm.LookPath("helm")
	require.NoError(t, err)
	assert.Equal(t, pinnedHelm, path)
}

// captureSink records all emitted events for assertion in tests.
type captureSink struct {
	events []progress.Event
}

func (s *captureSink) Emit(_ context.Context, e progress.Event) error {
	s.events = append(s.events, e)
	return nil
}

func (*captureSink) Close() error { return nil }

// TestRunPreflightStreaming_EmitsEvents verifies that RunPreflightStreaming emits
// events in the correct order: CheckStarted → CheckCompleted for each check, then
// CategoryCompleted. It also verifies that the Check result fields are correctly
// threaded into the CheckCompleted events.
func TestRunPreflightStreaming_EmitsEvents(t *testing.T) {
	passingSpec := BinarySpec{
		Name:     "kubectl",
		MinVer:   semver.MustParse("1.0.0"),
		HintURL:  "https://kubernetes.io/docs/tasks/tools/",
		LookPath: func(string) (string, error) { return "/usr/local/bin/kubectl", nil },
		Version: func(_ context.Context, _ string) (*semver.Version, error) {
			return semver.MustParse("1.30.2"), nil
		},
	}
	failingSpec := BinarySpec{
		Name:     "helmfile",
		MinVer:   semver.MustParse("1.0.0"),
		HintURL:  "https://github.com/helmfile/helmfile#installation",
		LookPath: func(string) (string, error) { return "", assert.AnError },
	}

	cfg := PreflightConfig{
		Tools: []BinarySpec{passingSpec, failingSpec},
	}
	sink := &captureSink{}
	results := RunPreflightStreaming(context.Background(), cfg, sink)
	require.Len(t, results, 2, "expected 2 CheckResult entries")

	// Passing check
	assert.True(t, results[0].Passed, "kubectl check should pass")
	assert.Equal(t, "1.30.2", results[0].Detail, "Detail should carry the version string")

	// Failing check
	assert.False(t, results[1].Passed, "helmfile check should fail")

	// Verify event sequence:
	// CheckStarted{kubectl}, CheckCompleted{kubectl},
	// CheckStarted{helmfile}, CheckCompleted{helmfile},
	// CategoryCompleted
	require.Len(t, sink.events, 5, "expected 5 events (2×(started+completed) + 1 category)")

	cs0, ok := sink.events[0].(progress.CheckStarted)
	require.True(t, ok, "event[0] must be CheckStarted")
	assert.Equal(t, "local-host-tools", cs0.Category)
	assert.Equal(t, "local-host-tools-kubectl", cs0.ID)

	cc0, ok := sink.events[1].(progress.CheckCompleted)
	require.True(t, ok, "event[1] must be CheckCompleted")
	assert.Equal(t, "local-host-tools-kubectl", cc0.ID)
	assert.True(t, cc0.Passed)
	assert.Equal(t, "1.30.2", cc0.Detail)

	cs1, ok := sink.events[2].(progress.CheckStarted)
	require.True(t, ok, "event[2] must be CheckStarted")
	assert.Equal(t, "local-host-tools-helmfile", cs1.ID)

	cc1, ok := sink.events[3].(progress.CheckCompleted)
	require.True(t, ok, "event[3] must be CheckCompleted")
	assert.Equal(t, "local-host-tools-helmfile", cc1.ID)
	assert.False(t, cc1.Passed)
	assert.Equal(t, SeverityError, cc1.Severity)
	assert.Equal(t, "https://github.com/helmfile/helmfile#installation", cc1.HintURL)

	catDone, ok := sink.events[4].(progress.CategoryCompleted)
	require.True(t, ok, "event[4] must be CategoryCompleted")
	assert.Equal(t, "local-host-tools", catDone.Category)
	assert.Equal(t, 1, catDone.PassedCount)
	assert.Equal(t, 1, catDone.FailedCount)
	assert.Greater(t, catDone.DurationSec, 0.0)
}

// TestRunPreflightStreaming_ContextCancel verifies that RunPreflightStreaming
// stops emitting events when the context is cancelled.
func TestRunPreflightStreaming_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel so the first ctx.Err() check fires

	cfg := PreflightConfig{
		Tools: []BinarySpec{
			{
				Name: "kubectl", MinVer: semver.MustParse("1.0.0"),
				LookPath: func(string) (string, error) { return "/usr/local/bin/kubectl", nil },
				Version: func(_ context.Context, _ string) (*semver.Version, error) {
					return semver.MustParse("1.30.0"), nil
				},
			},
		},
	}
	sink := &captureSink{}
	results := RunPreflightStreaming(ctx, cfg, sink)
	// With a pre-cancelled context the loop exits before running any checks.
	assert.Empty(t, results, "expected no results with pre-cancelled context")
	assert.Empty(t, sink.events, "expected no events with pre-cancelled context")
}

// TestRunPreflight_BackwardsCompatible verifies that the legacy RunPreflight
// API still returns the same slice and that callers which previously depended
// on it are not broken.
func TestRunPreflight_BackwardsCompatible(t *testing.T) {
	cfg := PreflightConfig{
		Tools: []BinarySpec{
			{
				Name: "helm", MinVer: semver.MustParse("3.14.0"),
				LookPath: func(string) (string, error) { return "/usr/local/bin/helm", nil },
				Version: func(_ context.Context, _ string) (*semver.Version, error) {
					return semver.MustParse("3.15.0"), nil
				},
			},
		},
	}
	results := RunPreflight(context.Background(), cfg)
	require.Len(t, results, 1)
	assert.True(t, results[0].Passed)
	assert.Equal(t, "3.15.0", results[0].Detail)
}

func TestRunVersionCmd_ParsesShortHelmOutput(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "fake-helm")
	require.NoError(t, os.WriteFile(fake, []byte("#!/bin/sh\necho 'v3.15.4+gfa9efb0'\n"), 0o755))
	v, err := runVersionCmd(context.Background(), fake, []string{"version", "--short"}, semverRE)
	require.NoError(t, err)
	assert.Equal(t, "3.15.4", v.String())
}

func TestProbeHelmfileVersion_FallsBackToPlainVersion(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "helmfile")
	require.NoError(t, os.WriteFile(fake, []byte(`#!/bin/sh
if [ "$1 $2 $3" = "version --output short" ]; then
  echo "short output temporarily unavailable" >&2
  exit 2
fi
if [ "$1" = "version" ]; then
  echo "helmfile version v1.5.0"
  exit 0
fi
echo "unexpected args: $*" >&2
exit 64
`), 0o755))

	v, err := probeHelmfileVersion(context.Background(), fake)
	require.NoError(t, err)
	assert.Equal(t, "1.5.0", v.String())
}

// passingToolSpec returns a BinarySpec that always reports the given version as passing.
func passingToolSpec(name, ver string) BinarySpec {
	return BinarySpec{
		Name:     name,
		MinVer:   semver.MustParse("1.0.0"),
		LookPath: func(string) (string, error) { return "/usr/local/bin/" + name, nil },
		Version: func(_ context.Context, _ string) (*semver.Version, error) {
			return semver.MustParse(ver), nil
		},
	}
}

func TestRunPreflightForRole_LocalOnly(t *testing.T) {
	sink := &captureSink{}
	cfg := PreflightConfig{Tools: []BinarySpec{
		passingToolSpec("kubectl", "1.30.0"),
		passingToolSpec("helmfile", "1.1.0"),
		passingToolSpec("helm", "3.15.0"),
	}}
	res := RunPreflightForRole(context.Background(), cfg, RoleLocalOnly, RoleConfig{}, sink)

	// Only local-host-tools category emitted; 3 tools plus the helm runtime compatibility check.
	for _, e := range sink.events {
		if cs, ok := e.(progress.CheckStarted); ok {
			assert.Equal(t, "local-host-tools", cs.Category, "unexpected category for check %s", cs.ID)
		}
	}
	assert.Len(t, res, 4, "expected 3 tool results plus helm runtime compatibility")
}

// A category with no rows would report a clean tally for nothing checked, so
// it is not reported. A role always has rows: its probes, or the skipped rows
// that say why they did not run.
func TestRunPreflightForRole_EmptyCategoryIsNotReported(t *testing.T) {
	sink := &captureSink{}
	cfg := PreflightConfig{Tools: []BinarySpec{passingToolSpec("kubectl", "1.30.0")}}
	RunPreflightForRole(context.Background(), cfg, RoleControlPlane, RoleConfig{KubeContext: "admin@cp"}, sink)

	seen := map[string]bool{}
	for _, e := range sink.events {
		if cc, ok := e.(progress.CategoryCompleted); ok {
			seen[cc.Category] = true
		}
	}
	assert.True(t, seen["local-host-tools"], "expected local-host-tools category")
	assert.False(t, seen["control-plane-cluster"], "an empty category must not report a tally")
}

func TestRunPreflightForRole_ControlPlaneWithValidatorAddsClusterValidatorCheck(t *testing.T) {
	sink := &captureSink{}
	cfg := PreflightConfig{Tools: []BinarySpec{passingToolSpec("kubectl", "1.30.0")}}
	cv := func(_ context.Context, p ClusterValidatorParams) ClusterValidatorResult {
		return ClusterValidatorResult{Passed: true}
	}
	res := RunPreflightForRole(context.Background(), cfg, RoleControlPlane, RoleConfig{
		KubeContext:           "admin@cp",
		ClusterValidator:      cv,
		ClusterValidatorImage: "nvcf-validator:1.0",
	}, sink)

	var gotCheckIDs []string
	for _, r := range res {
		gotCheckIDs = append(gotCheckIDs, r.ID)
	}
	assert.Contains(t, gotCheckIDs, "cluster-validator",
		"cluster-validator check must appear when ClusterValidator is configured for control-plane role")
}

func TestRunPreflightForRole_ComputePlaneWithoutSISURL(t *testing.T) {
	sink := &captureSink{}
	cfg := PreflightConfig{Tools: []BinarySpec{passingToolSpec("kubectl", "1.30.0")}}
	res := RunPreflightForRole(context.Background(), cfg, RoleComputePlane, RoleConfig{}, sink)

	var gotIDs []string
	for _, r := range res {
		gotIDs = append(gotIDs, r.ID)
	}
	// No row passes for a check that does not run.
	assert.NotContains(t, gotIDs, "gpu-operator")
	assert.NotContains(t, gotIDs, "gpu-node-labels")
	assert.NotContains(t, gotIDs, "sis-reachability") // no SISURL → not added
}

func TestRunPreflightForRole_ComputePlaneWithSISURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sink := &captureSink{}
	cfg := PreflightConfig{Tools: []BinarySpec{passingToolSpec("kubectl", "1.30.0")}}
	res := RunPreflightForRole(context.Background(), cfg, RoleComputePlane, RoleConfig{SISURL: srv.URL}, sink)

	var sisRes *CheckResult
	for i, r := range res {
		if r.ID == "sis-reachability" {
			sisRes = &res[i]
			break
		}
	}
	require.NotNil(t, sisRes, "expected sis-reachability result")
	assert.True(t, sisRes.Passed, "SIS check should pass against a healthy server")
}

func TestRunPreflightForRole_ComputePlaneSISDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	srv.Close() // immediately close so connections are refused

	sink := &captureSink{}
	cfg := PreflightConfig{Tools: []BinarySpec{passingToolSpec("kubectl", "1.30.0")}}
	res := RunPreflightForRole(context.Background(), cfg, RoleComputePlane, RoleConfig{SISURL: srv.URL}, sink)

	var sisRes *CheckResult
	for i, r := range res {
		if r.ID == "sis-reachability" {
			sisRes = &res[i]
			break
		}
	}
	require.NotNil(t, sisRes, "expected sis-reachability result")
	assert.False(t, sisRes.Passed, "SIS check should fail when server is down")
}

func TestRunPreflightForRole_LocalOnlyConfigSuppressesClusterChecks(t *testing.T) {
	sink := &captureSink{}
	cfg := PreflightConfig{LocalOnly: true, Tools: []BinarySpec{passingToolSpec("kubectl", "1.30.0")}}
	// Even with RoleControlPlane, LocalOnly suppresses cluster checks.
	res := RunPreflightForRole(context.Background(), cfg, RoleControlPlane, RoleConfig{}, sink)

	var gotIDs []string
	for _, r := range res {
		gotIDs = append(gotIDs, r.ID)
	}
	assert.NotContains(t, gotIDs, "gateway-api-crds", "LocalOnly must suppress control-plane cluster checks")
	assert.Contains(t, gotIDs, "local-host-tools-kubectl", "local-host tools must still run")
}

// findResult returns a pointer to the first CheckResult with the given ID, or
// nil if not present. Callers use require.NotNil to assert presence.
func findResult(res []CheckResult, id string) *CheckResult {
	for i := range res {
		if res[i].ID == id {
			return &res[i]
		}
	}
	return nil
}

func TestNodeInotifyCheck_AllNodesPass(t *testing.T) {
	prober := func(_ context.Context, _ string) ([]NodeInotifyLimits, error) {
		return []NodeInotifyLimits{
			{NodeName: "node-a", MaxUserInstances: 8192, MaxUserWatches: 524288},
			{NodeName: "node-b", MaxUserInstances: 16384, MaxUserWatches: 1048576},
		}, nil
	}
	sink := &captureSink{}
	cfg := PreflightConfig{Tools: []BinarySpec{passingToolSpec("kubectl", "1.30.0")}}
	res := RunPreflightForRole(context.Background(), cfg, RoleComputePlane, RoleConfig{InotifyProber: prober}, sink)

	got := findResult(res, "node-inotify-limits")
	require.NotNil(t, got, "expected node-inotify-limits result")
	assert.True(t, got.Passed, "all nodes meeting minimums should pass")
	assert.Contains(t, got.Message, "2 node(s)")
}

func TestNodeInotifyCheck_OneNodeBelowMinimum(t *testing.T) {
	prober := func(_ context.Context, _ string) ([]NodeInotifyLimits, error) {
		return []NodeInotifyLimits{
			{NodeName: "node-a", MaxUserInstances: 8192, MaxUserWatches: 524288},
			{NodeName: "node-b", MaxUserInstances: 128, MaxUserWatches: 524288},
		}, nil
	}
	sink := &captureSink{}
	cfg := PreflightConfig{Tools: []BinarySpec{passingToolSpec("kubectl", "1.30.0")}}
	res := RunPreflightForRole(context.Background(), cfg, RoleComputePlane, RoleConfig{InotifyProber: prober}, sink)

	got := findResult(res, "node-inotify-limits")
	require.NotNil(t, got)
	assert.False(t, got.Passed, "below-minimum node must fail the check")
	assert.Equal(t, SeverityError, got.Severity)
	assert.Contains(t, got.Message, "node-b")
	assert.Contains(t, got.Message, "max_user_instances=128/8192")
	assert.NotContains(t, got.Message, "node-a", "node above minimum should not be listed")
	assert.Equal(t, inotifyHintURL, got.HintURL)
}

func TestNodeInotifyCheck_BothLimitsBelowMinimum(t *testing.T) {
	prober := func(_ context.Context, _ string) ([]NodeInotifyLimits, error) {
		return []NodeInotifyLimits{
			{NodeName: "node-a", MaxUserInstances: 128, MaxUserWatches: 8192},
		}, nil
	}
	sink := &captureSink{}
	cfg := PreflightConfig{Tools: []BinarySpec{passingToolSpec("kubectl", "1.30.0")}}
	res := RunPreflightForRole(context.Background(), cfg, RoleComputePlane, RoleConfig{InotifyProber: prober}, sink)

	got := findResult(res, "node-inotify-limits")
	require.NotNil(t, got)
	assert.False(t, got.Passed)
	assert.Contains(t, got.Message, "max_user_instances=128/8192")
	assert.Contains(t, got.Message, "max_user_watches=8192/524288")
}

func TestNodeInotifyCheck_LimitViolationsBeatProbeErrors(t *testing.T) {
	// Mixed result: one node we couldn't probe (RBAC denial), one node that
	// is non-compliant. The check must surface the limit violation as
	// "error" so the operator sees the actionable problem, and still mention
	// the unprobed node so they aren't surprised.
	prober := func(_ context.Context, _ string) ([]NodeInotifyLimits, error) {
		return []NodeInotifyLimits{
			{NodeName: "node-a", Err: fmt.Errorf(`create probe pod on node node-a: pods is forbidden: User "cli" cannot create resource "pods"`)},
			{NodeName: "node-b", MaxUserInstances: 128, MaxUserWatches: 524288},
		}, nil
	}
	sink := &captureSink{}
	cfg := PreflightConfig{Tools: []BinarySpec{passingToolSpec("kubectl", "1.30.0")}}
	res := RunPreflightForRole(context.Background(), cfg, RoleComputePlane, RoleConfig{InotifyProber: prober}, sink)

	got := findResult(res, "node-inotify-limits")
	require.NotNil(t, got)
	assert.False(t, got.Passed, "limit violation must fail the check")
	assert.Equal(t, SeverityError, got.Severity,
		"limit violations outrank probe errors and must surface as error, not warning")
	assert.Contains(t, got.Message, "node-b", "non-compliant node must appear in message")
	assert.Contains(t, got.Message, "max_user_instances=128/8192")
	assert.Contains(t, got.Message, "node-a", "unprobed node must still be reported")
	assert.Contains(t, got.Message, "additionally could not probe",
		"probe errors should be appended, not replace the violation message")
}

func TestNodeInotifyCheck_PerNodeProbeError(t *testing.T) {
	prober := func(_ context.Context, _ string) ([]NodeInotifyLimits, error) {
		return []NodeInotifyLimits{
			{NodeName: "node-a", MaxUserInstances: 8192, MaxUserWatches: 524288},
			{NodeName: "node-b", Err: fmt.Errorf(`create probe pod on node node-b: pods is forbidden: User "cli" cannot create resource "pods"`)},
		}, nil
	}
	sink := &captureSink{}
	cfg := PreflightConfig{Tools: []BinarySpec{passingToolSpec("kubectl", "1.30.0")}}
	res := RunPreflightForRole(context.Background(), cfg, RoleComputePlane, RoleConfig{InotifyProber: prober}, sink)

	got := findResult(res, "node-inotify-limits")
	require.NotNil(t, got)
	assert.False(t, got.Passed, "per-node probe error must not silently pass")
	assert.Equal(t, SeverityWarning, got.Severity, "probe failures degrade to warning, not error")
	assert.Contains(t, got.Message, "node-b")
	assert.Contains(t, got.Message, "forbidden")
	assert.False(t, got.Transient, "a denied create does not clear by itself")
}

// A node the probe's budget did not reach may be below the limits, so the
// warning is transient and --wait polls again. A limit violation found beside
// it is an error, never transient.
func TestNodeInotifyCheck_NodesTheBudgetDidNotReachAreTransient(t *testing.T) {
	notReached := NodeInotifyLimits{NodeName: "node-b", OutOfBudget: true,
		Err: errors.New("not probed: the inotify probe's 1m40s budget ran out")}
	ok := NodeInotifyLimits{NodeName: "node-a", MaxUserInstances: 8192, MaxUserWatches: 524288}
	low := NodeInotifyLimits{NodeName: "node-a", MaxUserInstances: 128, MaxUserWatches: 524288}
	for name, tc := range map[string]struct {
		limits        []NodeInotifyLimits
		wantSeverity  Severity
		wantTransient bool
	}{
		"not reached": {
			limits:        []NodeInotifyLimits{ok, notReached},
			wantSeverity:  SeverityWarning,
			wantTransient: true,
		},
		"not reached beside a violation": {
			limits:       []NodeInotifyLimits{low, notReached},
			wantSeverity: SeverityError,
		},
	} {
		res := RunPreflightForRole(context.Background(), PreflightConfig{}, RoleComputePlane, RoleConfig{
			InotifyProber: func(context.Context, string) ([]NodeInotifyLimits, error) { return tc.limits, nil },
		}, &captureSink{})
		got := findResult(res, "node-inotify-limits")
		require.NotNil(t, got, name)
		assert.False(t, got.Passed, name)
		assert.Equal(t, tc.wantSeverity, got.Severity, name)
		assert.Equal(t, tc.wantTransient, got.Transient, name)
		assert.Contains(t, got.Message, "node-b: not probed", name)
	}
}

func TestNodeInotifyCheck_ClusterWideProbeError(t *testing.T) {
	probeErr := fmt.Errorf("kubectl get nodes: connection refused")
	prober := func(_ context.Context, _ string) ([]NodeInotifyLimits, error) {
		return nil, probeErr
	}
	sink := &captureSink{}
	cfg := PreflightConfig{Tools: []BinarySpec{passingToolSpec("kubectl", "1.30.0")}}
	res := RunPreflightForRole(context.Background(), cfg, RoleComputePlane, RoleConfig{InotifyProber: prober}, sink)

	got := findResult(res, "node-inotify-limits")
	require.NotNil(t, got)
	assert.False(t, got.Passed)
	assert.Equal(t, SeverityWarning, got.Severity)
	assert.ErrorIs(t, got.Err, probeErr)
}

func TestNodeInotifyCheck_NotInjectedWhenProberNil(t *testing.T) {
	sink := &captureSink{}
	cfg := PreflightConfig{Tools: []BinarySpec{passingToolSpec("kubectl", "1.30.0")}}
	// No InotifyProber → check must not appear.
	res := RunPreflightForRole(context.Background(), cfg, RoleComputePlane, RoleConfig{}, sink)
	assert.Nil(t, findResult(res, "node-inotify-limits"),
		"inotify check must be skipped when no prober is configured")
}

func TestParseInotifyOutput(t *testing.T) {
	t.Run("two clean lines", func(t *testing.T) {
		instances, watches, err := parseInotifyOutput("8192\n524288\n")
		require.NoError(t, err)
		assert.Equal(t, int64(8192), instances)
		assert.Equal(t, int64(524288), watches)
	})
	t.Run("ignores non-numeric prefix lines", func(t *testing.T) {
		instances, watches, err := parseInotifyOutput("If you don't see a command prompt, try pressing enter.\n8192\n524288\n")
		require.NoError(t, err)
		assert.Equal(t, int64(8192), instances)
		assert.Equal(t, int64(524288), watches)
	})
	t.Run("missing second value errors", func(t *testing.T) {
		_, _, err := parseInotifyOutput("8192\n")
		require.Error(t, err)
	})
	t.Run("empty output errors", func(t *testing.T) {
		_, _, err := parseInotifyOutput("")
		require.Error(t, err)
	})
}

// In ModeSingle one cluster hosts both roles and only one stale-namespace
// probe runs, so the skipped role's namespaces must be merged in. Dropping
// them means a kai-scheduler or nvca-operator namespace wedged Terminating is
// reported as "no stale NVCF namespaces detected".
func TestStaleNamespaceCheck_MergesTheOtherRolesNamespaces(t *testing.T) {
	var probed []string
	prober := func(_ context.Context, _ string, namespaces []string) ([]StaleNamespace, error) {
		probed = namespaces
		return nil, nil
	}
	rc := RoleConfig{
		StaleNamespaceProber: prober,
		ExtraStaleNamespaces: ComputePlaneStaleNamespaces("", ""),
	}
	cat := controlPlaneCheckCategory(rc)
	require.NotEmpty(t, cat.checks)
	cat.checks[0].Run(context.Background())

	for _, ns := range []string{"nvcf", "vault-system"} {
		assert.Contains(t, probed, ns, "the control-plane list must still be covered")
	}
	assert.Contains(t, probed, "nvca-operator", "the compute-plane list must be merged in, not dropped")
}

func TestMergeNamespaces_DedupesAndKeepsOrder(t *testing.T) {
	got := mergeNamespaces([]string{"a", "b"}, []string{"b", "c", ""})
	assert.Equal(t, []string{"a", "b", "c"}, got)
	assert.Nil(t, mergeNamespaces(nil, nil))
	assert.Equal(t, []string{"a"}, mergeNamespaces([]string{"nvcf-backend", "a"}, []string{"nvca-system"}),
		"namespaces NVCA creates at runtime hold live work and are never probed")
}

// Every count derives from one rule: only an error-severity miss fails; any
// other miss is a warning.
func TestCountResults_OneRule(t *testing.T) {
	p, f, w := CountResults([]CheckResult{
		{Passed: true, Severity: SeverityInfo},
		{Passed: false, Severity: SeverityError},
		{Passed: false, Severity: SeverityWarning},
		{Passed: false, Severity: SeverityInfo},
	})
	assert.Equal(t, 1, p)
	assert.Equal(t, 1, f)
	assert.Equal(t, 2, w)
}

// When the budget runs out, the checks not yet started are error rows rather
// than silently dropped, so a partial run cannot grade as a pass. An
// interrupt ends the run without them: the command reports the interrupt.
func TestRunPreflight_BudgetSpentChecksAreNotRunErrors(t *testing.T) {
	cfg := PreflightConfig{Tools: []BinarySpec{passingToolSpec("kubectl", "1.30.0"), passingToolSpec("helm", "3.15.0")}}

	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	res := RunPreflightForRole(expired, cfg, RoleLocalOnly, RoleConfig{}, &captureSink{})
	require.NotEmpty(t, res)
	for _, r := range res {
		assert.True(t, r.IsBlockingFailure(), r.ID)
		assert.True(t, r.CutShort, r.ID)
		assert.Contains(t, r.Message, "not run", r.ID)
	}

	cancelled, stop := context.WithCancel(context.Background())
	stop()
	assert.Empty(t, RunPreflightForRole(cancelled, cfg, RoleLocalOnly, RoleConfig{}, &captureSink{}))

	// A check cut short by the interrupt is not a finding either.
	midRun, stopMidRun := context.WithCancel(context.Background())
	defer stopMidRun()
	interrupting := passingToolSpec("kubectl", "1.30.0")
	interrupting.Version = func(context.Context, string) (*semver.Version, error) {
		stopMidRun()
		return nil, context.Canceled
	}
	cfg = PreflightConfig{Tools: []BinarySpec{interrupting}}
	assert.Empty(t, RunPreflightForRole(midRun, cfg, RoleLocalOnly, RoleConfig{}, &captureSink{}))
}

// A check the budget stopped while it ran is cut short: no finding either way.
// One that had its result, a pass or its own failure, keeps it even when the
// budget ran out right after.
func TestRunPreflight_CutShortOnlyWhenTheBudgetStoppedTheCheck(t *testing.T) {
	spec := func(name string, version func(ctx context.Context) (*semver.Version, error)) BinarySpec {
		s := passingToolSpec(name, "1.30.0")
		s.Version = func(ctx context.Context, _ string) (*semver.Version, error) { return version(ctx) }
		return s
	}
	for name, tc := range map[string]struct {
		version  func(ctx context.Context) (*semver.Version, error)
		cutShort bool
		passed   bool
	}{
		"stopped by the budget": {
			version:  func(ctx context.Context) (*semver.Version, error) { <-ctx.Done(); return nil, ctx.Err() },
			cutShort: true,
		},
		"its own failure": {
			version: func(ctx context.Context) (*semver.Version, error) {
				<-ctx.Done()
				return nil, errors.New("exec format error")
			},
		},
		"passed as the budget ran out": {
			version: func(ctx context.Context) (*semver.Version, error) {
				<-ctx.Done()
				return semver.MustParse("1.30.0"), nil
			},
			passed: true,
		},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		res := RunPreflightForRole(ctx, PreflightConfig{Tools: []BinarySpec{spec("kubectl", tc.version)}},
			RoleLocalOnly, RoleConfig{}, &captureSink{})
		cancel()
		require.Len(t, res, 1, name)
		assert.Equal(t, tc.cutShort, res[0].CutShort, name)
		assert.Equal(t, tc.passed, res[0].Passed, name)
		if tc.cutShort {
			assert.True(t, strings.HasPrefix(res[0].Message, "cut short: "), name)
		}
	}
}

// A row that says how to remove what a check left in the cluster is reported
// even when the run is interrupted, as an event too, since nothing else will
// say it. An interrupted row without one is not a finding and is dropped.
func TestRunPreflight_InterruptKeepsTheCleanupRow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := &captureSink{}
	rc := RoleConfig{
		KubeContext:           "ctx-a",
		ClusterValidatorImage: "nvcr.io/nvidia/validator:1",
		ClusterValidator: func(context.Context, ClusterValidatorParams) ClusterValidatorResult {
			cancel()
			return ClusterValidatorResult{RunID: "run1", Err: context.Canceled, LeftBehind: true}
		},
	}
	res := RunPreflightForRole(ctx, PreflightConfig{}, RoleControlPlane, rc, sink)
	require.Len(t, res, 1)
	assert.Equal(t, "cluster-validator", res[0].ID)
	assert.Equal(t, SeverityWarning, res[0].Severity)
	assert.Equal(t, "cluster-validator was interrupted", res[0].Message)
	assert.Contains(t, res[0].Cleanup, clusterValidatorRunLabel+"=run1")
	assert.Contains(t, res[0].Cleanup, "--context ctx-a")
	var emitted []progress.CheckCompleted
	for _, e := range sink.events {
		if cc, ok := e.(progress.CheckCompleted); ok {
			emitted = append(emitted, cc)
		}
	}
	require.Len(t, emitted, 1)
	assert.Contains(t, emitted[0].Detail, res[0].Cleanup)

	dropped, stop := context.WithCancel(context.Background())
	defer stop()
	rc.ClusterValidator = func(context.Context, ClusterValidatorParams) ClusterValidatorResult {
		stop()
		return ClusterValidatorResult{RunID: "run2", Err: context.Canceled}
	}
	assert.Empty(t, RunPreflightForRole(dropped, PreflightConfig{}, RoleControlPlane, rc, &captureSink{}))
}

// A configured validator whose tag could not be resolved does not run, and
// each role says so as a failed row: a note on stderr is lost to a consumer
// of the JSON stream, and nothing was validated.
func TestRunPreflight_UnresolvedValidatorImageFailsEachRole(t *testing.T) {
	rc := RoleConfig{ClusterValidatorUnresolvedImage: "nvcr.io/nvidia/nvcf-byoc/cluster-validator"}
	for _, role := range []Role{RoleControlPlane, RoleComputePlane} {
		res := RunPreflightForRole(context.Background(), PreflightConfig{}, role, rc, &captureSink{})
		var row *CheckResult
		for i := range res {
			if res[i].ID == "cluster-validator" {
				row = &res[i]
			}
		}
		require.NotNil(t, row, role)
		assert.True(t, row.IsBlockingFailure(), role)
		assert.Contains(t, row.Message, "could not resolve a tag for nvcr.io/nvidia/nvcf-byoc/cluster-validator", role)
		assert.Contains(t, row.Message, "--skip-cluster-validation", role)
	}
}

// completedRows returns the CheckCompleted events a sink received.
func completedRows(sink *captureSink) []progress.CheckCompleted {
	var out []progress.CheckCompleted
	for _, e := range sink.events {
		if cc, ok := e.(progress.CheckCompleted); ok {
			out = append(out, cc)
		}
	}
	return out
}

// Every passing row is info on the wire, whatever severity the check set
// before it knew the outcome: a consumer that triages on severity error must
// not flag a healthy run.
func TestRunPreflight_PassingRowsAreInfo(t *testing.T) {
	sink := &captureSink{}
	cfg := PreflightConfig{Tools: []BinarySpec{passingToolSpec("kubectl", "1.30.0"), passingToolSpec("helm", "3.15.0")}}
	rc := RoleConfig{
		InotifyProber: func(context.Context, string) ([]NodeInotifyLimits, error) {
			return []NodeInotifyLimits{{NodeName: "n1", MaxUserInstances: 8192, MaxUserWatches: 524288}}, nil
		},
	}
	RunPreflightForRole(context.Background(), cfg, RoleComputePlane, rc, sink)
	rows := completedRows(sink)
	require.Len(t, rows, 3)
	for _, row := range rows {
		assert.True(t, row.Passed, row.ID)
		assert.Equal(t, SeverityInfo, row.Severity, row.ID)
	}
}

// A probe that checked no node is not a pass.
func TestNodeInotifyCheck_NothingProbedIsNotAPass(t *testing.T) {
	spec := nodeInotifyCheck(func(context.Context, string) ([]NodeInotifyLimits, error) { return nil, nil }, "")
	r := spec.Run(context.Background())
	assert.False(t, r.Passed)
	assert.Equal(t, SeverityWarning, r.Severity)
	assert.Contains(t, r.Message, "no schedulable nodes were probed")
}

// A SIS request the spent budget stopped is cut short, not graded as SIS being
// unreachable.
func TestRunPreflight_SISStoppedByTheBudgetIsCutShort(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	res := RunPreflightForRole(ctx, PreflightConfig{}, RoleComputePlane, RoleConfig{SISURL: srv.URL}, &captureSink{})
	sis := findResult(res, "sis-reachability")
	require.NotNil(t, sis)
	assert.True(t, sis.CutShort)
	assert.True(t, strings.HasPrefix(sis.Message, "cut short: "), sis.Message)
}

// The checks before a validator run inside the probe share, so a probe that
// hangs cannot eat into the validator's time: the validator still runs on the
// run's budget, and the hung probe is cut short.
func TestRunPreflight_ProbeShareBoundsTheChecksBeforeTheValidator(t *testing.T) {
	prevShare, prevCleanup := checkProbeShare, probePodCleanupTimeout
	checkProbeShare, probePodCleanupTimeout = 400*time.Millisecond, 100*time.Millisecond
	t.Cleanup(func() { checkProbeShare, probePodCleanupTimeout = prevShare, prevCleanup })

	var validatorCtxErr error
	ran := false
	rc := RoleConfig{
		StaleNamespaceProber: func(ctx context.Context, _ string, _ []string) ([]StaleNamespace, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		ClusterValidatorImage: "nvcr.io/nvidia/validator:1",
		ClusterValidator: func(ctx context.Context, _ ClusterValidatorParams) ClusterValidatorResult {
			ran, validatorCtxErr = true, ctx.Err()
			return ClusterValidatorResult{Passed: true,
				Logs: validatorRoleMarker + "control-plane\n" + validatorVerdictReady + "\n"}
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	start := time.Now()
	res := RunPreflightForRole(ctx, PreflightConfig{}, RoleControlPlane, rc, &captureSink{})
	assert.Less(t, time.Since(start), checkProbeShare, "the probes end inside the share")

	stale := findResult(res, "stale-namespaces")
	require.NotNil(t, stale)
	assert.True(t, stale.CutShort)
	require.True(t, ran, "the validator still runs")
	assert.NoError(t, validatorCtxErr, "on the run's budget, not the spent share")
	validator := findResult(res, "cluster-validator")
	require.NotNil(t, validator)
	assert.True(t, validator.Passed)
}

// The checks that run on this machine have a share of their own: registry
// probes that hang cannot spend the time set aside for the cluster checks
// after them, and a run of local checks alone is still bounded.
func TestRunPreflight_LocalChecksDoNotSpendTheClusterShare(t *testing.T) {
	prevLocal, prevShare, prevCleanup := localCheckShare, checkProbeShare, probePodCleanupTimeout
	localCheckShare = 300 * time.Millisecond
	checkProbeShare, probePodCleanupTimeout = 400*time.Millisecond, 100*time.Millisecond
	t.Cleanup(func() { localCheckShare, checkProbeShare, probePodCleanupTimeout = prevLocal, prevShare, prevCleanup })

	hang := func(ctx context.Context, _, _ string, _ bool) error {
		<-ctx.Done()
		return ctx.Err()
	}
	cfg := PreflightConfig{
		Registries:      []RegistryEntry{{Registry: "a.example"}, {Registry: "b.example"}},
		RegistryChecker: hang,
	}
	var staleCtxErr error
	rc := RoleConfig{
		StaleNamespaceProber: func(ctx context.Context, _ string, _ []string) ([]StaleNamespace, error) {
			staleCtxErr = ctx.Err()
			return nil, nil
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	res := RunPreflightForRole(ctx, cfg, RoleControlPlane, rc, &captureSink{})
	stale := findResult(res, "stale-namespaces")
	require.NotNil(t, stale)
	assert.NoError(t, staleCtxErr, "the cluster check starts with its share intact")
	assert.False(t, stale.CutShort, stale.Message)
	assert.True(t, stale.Passed, stale.Message)

	start := time.Now()
	local := RunPreflightForRole(ctx, cfg, RoleLocalOnly, RoleConfig{}, &captureSink{})
	assert.Less(t, time.Since(start), localCheckShare+200*time.Millisecond, "a local-only run is bounded")
	require.NotEmpty(t, local)
	for _, r := range local {
		assert.True(t, r.CutShort, r.ID+": "+r.Message)
	}
}

// The inotify probe's own bound, and the cleanup of its pods after it, fit in
// the probe share, so a slow cluster is reported as nodes not probed rather
// than cut short.
func TestCheckProbeShare_CoversTheInotifyProbe(t *testing.T) {
	assert.Less(t, inotifyProbeBudget, checkProbeShare-probePodCleanupTimeout)
}

// A missing tool fails pre-install, where the install runs it, and only warns
// when an installed stack is checked, which runs no local tool.
func TestRunPreflight_ToolsAdvisoryWarns(t *testing.T) {
	missing := BinarySpec{
		Name: "helmfile", MinVer: semver.MustParse("1.0.0"),
		LookPath: func(string) (string, error) { return "", errors.New("not found") },
	}
	for advisory, want := range map[bool]Severity{false: SeverityError, true: SeverityWarning} {
		res := RunPreflightForRole(context.Background(),
			PreflightConfig{Tools: []BinarySpec{missing}, ToolsAdvisory: advisory}, RoleLocalOnly, RoleConfig{}, &captureSink{})
		require.Len(t, res, 1)
		assert.False(t, res[0].Passed)
		assert.Equal(t, want, res[0].Severity, "advisory=%v", advisory)
	}
}

// A check the flags or configuration leave out is still a row: an opt-out
// passes at info, a missing input warns. Neither is cut short by a spent
// budget, since neither has work to do, and its category is reported even
// when the role runs no other check in it.
func TestRunPreflight_SkippedChecksAreRows(t *testing.T) {
	rc := RoleConfig{Skipped: []SkippedCheck{
		{Category: CategoryControlPlane, ID: "cluster-validator", Message: "skipped (--skip-cluster-validation)"},
		{Category: CategoryComputePlane, ID: "cluster-validator", Message: "not set", Warn: true},
	}}
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	for _, ctx := range []context.Context{context.Background(), expired} {
		sink := &captureSink{}
		res := RunPreflightForRole(ctx, PreflightConfig{LocalOnly: true}, RoleLocalOnly, rc, sink)
		require.Len(t, res, 2)
		assert.Equal(t, CategoryControlPlane, res[0].Category)
		assert.True(t, res[0].Passed)
		assert.Equal(t, SeverityInfo, res[0].Severity)
		assert.Equal(t, CategoryComputePlane, res[1].Category)
		assert.False(t, res[1].Passed)
		assert.Equal(t, SeverityWarning, res[1].Severity)
		for _, r := range res {
			assert.False(t, r.CutShort)
		}
		assert.Len(t, completedRows(sink), 2)
	}
}

// category_completed counts by the one rule: an error is a failure, a warning
// is not, and a pass is a pass.
func TestRunPreflight_CategoryCountsFollowTheOneRule(t *testing.T) {
	sink := &captureSink{}
	rc := RoleConfig{
		StaleNamespaceProber: func(context.Context, string, []string) ([]StaleNamespace, error) { return nil, nil },
		InotifyProber: func(context.Context, string) ([]NodeInotifyLimits, error) {
			return []NodeInotifyLimits{{NodeName: "n1", MaxUserInstances: 128, MaxUserWatches: 8192}}, nil
		},
		Skipped: []SkippedCheck{{Category: CategoryComputePlane, ID: "cluster-validator", Message: "not set", Warn: true}},
	}
	RunPreflightForRole(context.Background(), PreflightConfig{}, RoleComputePlane, rc, sink)
	var cats []progress.CategoryCompleted
	for _, e := range sink.events {
		if cc, ok := e.(progress.CategoryCompleted); ok {
			cats = append(cats, cc)
		}
	}
	require.Len(t, cats, 1)
	assert.Equal(t, 1, cats[0].PassedCount)
	assert.Equal(t, 1, cats[0].FailedCount)
	assert.Equal(t, 1, cats[0].WarningCount)
}

// A registry this CLI cannot probe, or one that allows anonymous access, is a
// skip: a passing row at info on the wire, never a warning or a failure.
func TestRunPreflight_RegistrySkipsPassAtInfo(t *testing.T) {
	cfg := PreflightConfig{
		Registries: []RegistryEntry{
			{Registry: "123456789012.dkr.ecr.us-west-2.amazonaws.com", Critical: true},
			{Registry: "basic.example.com", Critical: true},
			{Registry: "anon.example.com", Critical: true},
		},
		RegistryChecker: func(_ context.Context, registry, _ string, _ bool) error {
			switch registry {
			case "anon.example.com":
				return registryProbeOutcome{kind: probeAnonymous, detail: "reachable anonymously"}
			default:
				return registryProbeOutcome{kind: probeSkipped, detail: "this probe cannot speak to it"}
			}
		},
	}
	sink := &captureSink{}
	RunPreflightForRole(context.Background(), cfg, RoleLocalOnly, RoleConfig{}, sink)
	rows := completedRows(sink)
	require.Len(t, rows, 3)
	for _, row := range rows {
		assert.True(t, row.Passed, row.ID)
		assert.Equal(t, SeverityInfo, row.Severity, row.ID)
	}
}

// A cluster that cannot be contacted at all fails its role: nothing about it
// was checked. A failure part way through the probe stays a warning.
func TestStaleNamespaceCheck_UnreachableClusterIsAnError(t *testing.T) {
	unreachable := staleNamespaceCheck(func(context.Context, string, []string) ([]StaleNamespace, error) {
		return nil, &ClusterUnreachableError{Context: "ctx-a", Err: errors.New("connection refused")}
	}, "ctx-a", []string{"nvcf"}).Run(context.Background())
	assert.True(t, unreachable.IsBlockingFailure())
	assert.Contains(t, unreachable.Message, "cannot reach ctx-a")

	midProbe := staleNamespaceCheck(func(context.Context, string, []string) ([]StaleNamespace, error) {
		return nil, errors.New("list Helm secrets in nvcf: stream error")
	}, "ctx-a", []string{"nvcf"}).Run(context.Background())
	assert.False(t, midProbe.Passed)
	assert.Equal(t, SeverityWarning, midProbe.Severity)
}

// A stuck namespace found before the probe share ran out still blocks the
// run: it is not graded as cut short, which would exit 5. A warning found
// before the cutoff is cut short, still blocking since a namespace the scan
// did not reach could be stuck, and keeps the namespaces it found.
func TestStaleNamespaceCheck_StuckFindingSurvivesTheBudget(t *testing.T) {
	pinCurrentKubeContext(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	cutOff := errors.Join(errors.New("get namespace sis: timeout"), context.DeadlineExceeded)
	probe := func(reason string) CheckResult {
		spec := staleNamespaceCheck(func(context.Context, string, []string) ([]StaleNamespace, error) {
			return []StaleNamespace{{Name: "cassandra-system", Reason: reason}}, cutOff
		}, "", []string{"cassandra-system", "sis"})
		return normaliseResult(ctx, spec.Run(ctx), spec.worstSeverity())
	}

	stuck := probe(StaleStuckTerminating)
	assert.False(t, stuck.CutShort)
	assert.True(t, stuck.IsBlockingFailure())
	assert.Contains(t, stuck.Message, "cassandra-system (stuck Terminating")
	assert.Contains(t, stuck.Message, "Additionally could not probe: get namespace sis: timeout")

	partial := probe(StaleNoHelmRelease)
	assert.True(t, partial.CutShort, "a warning beside a spent budget is not the whole answer")
	assert.True(t, partial.IsBlockingFailure())
	assert.True(t, strings.HasPrefix(partial.Message, cutShortMessage+"; found before then: "), partial.Message)
	assert.Contains(t, partial.Message, "cassandra-system (no Helm release)")

	deleting := probe(StaleTerminating)
	assert.True(t, deleting.CutShort)
	assert.Equal(t, SeverityError, deleting.Severity)
	assert.False(t, deleting.Transient, "only a warning is transient")
}

// A check the budget stops keeps the most severe grade it could have given,
// and the row says it was cut short on the wire. A registry whose rejection
// only warns stays a warning, cut short or not run, so a slow one cannot fail
// the run; a critical one still blocks. A tool a post-install run only
// advises on warns too.
func TestRunPreflight_CutShortRowsKeepTheirWorstSeverity(t *testing.T) {
	prevLocal := localCheckShare
	localCheckShare = 200 * time.Millisecond
	t.Cleanup(func() { localCheckShare = prevLocal })
	hang := func(ctx context.Context, _, _ string, _ bool) error {
		<-ctx.Done()
		return ctx.Err()
	}
	sink := &captureSink{}
	res := RunPreflightForRole(context.Background(), PreflightConfig{
		Registries: []RegistryEntry{
			{Registry: "slow.example"}, {Registry: "nvcr.io", Critical: true}, {Registry: "other.example"},
		},
		RegistryChecker: hang,
	}, RoleLocalOnly, RoleConfig{}, sink)
	want := map[string]struct {
		severity Severity
		message  string
	}{
		"registry-cred-slow.example":  {SeverityWarning, "cut short: "},
		"registry-cred-nvcr.io":       {SeverityError, "not run: "},
		"registry-cred-other.example": {SeverityWarning, "not run: "},
	}
	require.Len(t, res, len(want))
	for _, r := range res {
		assert.True(t, r.CutShort, r.ID)
		assert.Equal(t, want[r.ID].severity, r.Severity, r.ID)
		assert.True(t, strings.HasPrefix(r.Message, want[r.ID].message), r.ID+": "+r.Message)
	}
	rows := completedRows(sink)
	require.Len(t, rows, len(want))
	for _, row := range rows {
		assert.True(t, row.CutShort, row.ID)
		assert.Equal(t, want[row.ID].severity, row.Severity, row.ID)
	}

	for advisory, severity := range map[bool]Severity{false: SeverityError, true: SeverityWarning} {
		tool := passingToolSpec("helmfile", "1.0.0")
		tool.Version = func(ctx context.Context, _ string) (*semver.Version, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		res := RunPreflightForRole(context.Background(), PreflightConfig{Tools: []BinarySpec{tool},
			ToolsAdvisory: advisory}, RoleLocalOnly, RoleConfig{}, &captureSink{})
		require.Len(t, res, 1)
		assert.True(t, res[0].CutShort, "advisory=%v", advisory)
		assert.Equal(t, severity, res[0].Severity, "advisory=%v", advisory)
	}
}

// After install a critical registry's rejection only warns, so a cut-short or
// not-run row for it warns too and cannot fail the run.
func TestRunPreflight_CutShortCriticalRegistryAfterInstallWarns(t *testing.T) {
	prevLocal := localCheckShare
	localCheckShare = 200 * time.Millisecond
	t.Cleanup(func() { localCheckShare = prevLocal })
	hang := func(ctx context.Context, _, _ string, _ bool) error {
		<-ctx.Done()
		return ctx.Err()
	}
	res := RunPreflightForRole(context.Background(), PreflightConfig{
		Registries: []RegistryEntry{
			{Registry: "nvcr.io", Critical: true}, {Registry: "harbor.example.com", Critical: true},
		},
		RegistryChecker:     hang,
		RegistryPostInstall: true,
	}, RoleLocalOnly, RoleConfig{}, &captureSink{})
	require.Len(t, res, 2)
	for _, r := range res {
		assert.True(t, r.CutShort, r.ID)
		assert.Equal(t, SeverityWarning, r.Severity, r.ID+": "+r.Message)
	}
}

// Each term of the validator's run ceiling counts as often as one run can
// spend it. Bumping a term by one second grows the ceiling by its count.
func TestClusterValidatorRunCeiling_CountsEveryTerm(t *testing.T) {
	for name, tc := range map[string]struct {
		term  *time.Duration
		count time.Duration
	}{
		"its own timeout":         {&clusterValidatorTimeout, 1},
		"the deadline grace":      {&validatorDeadlineGrace, 1},
		"the reads that grade it": {&clusterValidatorLogFetchTimeout, 4},
		"the deferred sweeps":     {&validatorCleanupTimeout, 3},
		"the margin":              {&validatorRunMargin, 1},
	} {
		before := ClusterValidatorRunCeiling()
		prev := *tc.term
		*tc.term += time.Second
		after := ClusterValidatorRunCeiling()
		*tc.term = prev
		assert.Equal(t, tc.count*time.Second, after-before, name)
	}
}
