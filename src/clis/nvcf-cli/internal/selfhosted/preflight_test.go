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
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

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
	assert.Equal(t, "error", cc1.Severity)
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

	// Only local-host-tools category emitted; 3 checks (kubectl/helmfile/helm).
	for _, e := range sink.events {
		if cs, ok := e.(progress.CheckStarted); ok {
			assert.Equal(t, "local-host-tools", cs.Category, "unexpected category for check %s", cs.ID)
		}
	}
	assert.Len(t, res, 3, "expected 3 results (one per tool)")
}

func TestRunPreflightForRole_ControlPlaneAddsClusterCategory(t *testing.T) {
	sink := &captureSink{}
	cfg := PreflightConfig{Tools: []BinarySpec{passingToolSpec("kubectl", "1.30.0")}}
	res := RunPreflightForRole(context.Background(), cfg, RoleControlPlane, RoleConfig{KubeContext: "admin@cp"}, sink)

	seen := map[string]bool{}
	for _, e := range sink.events {
		if cc, ok := e.(progress.CategoryCompleted); ok {
			seen[cc.Category] = true
		}
	}
	assert.True(t, seen["local-host-tools"], "expected local-host-tools category")
	assert.True(t, seen["control-plane-cluster"], "expected control-plane-cluster category")

	var gotCheckIDs []string
	for _, r := range res {
		gotCheckIDs = append(gotCheckIDs, r.ID)
	}
	assert.Contains(t, gotCheckIDs, "gateway-api-crds")
	assert.Contains(t, gotCheckIDs, "default-storageclass")
}

func TestRunPreflightForRole_ComputePlaneWithoutSISURL(t *testing.T) {
	sink := &captureSink{}
	cfg := PreflightConfig{Tools: []BinarySpec{passingToolSpec("kubectl", "1.30.0")}}
	res := RunPreflightForRole(context.Background(), cfg, RoleComputePlane, RoleConfig{}, sink)

	var gotIDs []string
	for _, r := range res {
		gotIDs = append(gotIDs, r.ID)
	}
	assert.Contains(t, gotIDs, "gpu-operator")
	assert.Contains(t, gotIDs, "gpu-node-labels")
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
