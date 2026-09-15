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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nvcf-cli/internal/clusterdump"
)

// fakeDumpCollector is a test double for dumpCollector.
type fakeDumpCollector struct {
	d   clusterdump.Dump
	err error
}

func (f *fakeDumpCollector) Collect(_ context.Context) (clusterdump.Dump, error) {
	return f.d, f.err
}

func resetDumpFlags(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		dumpControlPlaneCtx = ""
		dumpComputePlaneCtx = ""
		dumpComputeOnly = false
		dumpOutputFile = ""
		dumpBundle = ""
		dumpInclude = nil
		dumpRedact = clusterdump.RedactSecrets
		dumpLogTail = 2000
		dumpMaxLogBytes = 1 << 20
		jsonOutput = false
	})
}

func injectFakeDumpCollector(t *testing.T, fc dumpCollector, buildErr error) {
	t.Helper()
	prev := newDumpCollector
	t.Cleanup(func() { newDumpCollector = prev })
	newDumpCollector = func() (dumpCollector, error) {
		return fc, buildErr
	}
}

func sampleDump(withCompute bool) clusterdump.Dump {
	d := clusterdump.Dump{
		CollectedAt: time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC),
		ControlPlane: &clusterdump.PlaneSnapshot{
			Role:              clusterdump.RoleControlPlane,
			Context:           "k3d-ncp-local-cp",
			KubernetesVersion: "v1.29.4+k3s1",
			Nodes:             clusterdump.NodeSummary{Ready: 1, Total: 1},
			HelmReleases: []clusterdump.HelmRelease{
				{Name: "nats", Namespace: "nats-system", Chart: "nats-1.2.3", AppVersion: "1.2.3", Status: "deployed"},
			},
		},
	}
	if withCompute {
		d.ComputePlane = &clusterdump.PlaneSnapshot{
			Role:              clusterdump.RoleComputePlane,
			Context:           "k3d-ncp-local-compute-1",
			KubernetesVersion: "v1.29.4+k3s1",
			Nodes:             clusterdump.NodeSummary{Ready: 1, Total: 1, GPUCount: 8},
			NVCFBackends: []clusterdump.NVCFBackendRow{
				{Name: "ncp-local-compute-1", AgentStatus: "Healthy", NVCAVersion: "2.1.0", GPUCapacity: 8},
			},
		}
	}
	return d
}

func runDumpCmd(t *testing.T) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	err := runClusterDump(cmd, nil)
	return buf.String(), err
}

func TestClusterDump_TextOutput(t *testing.T) {
	resetDumpFlags(t)
	injectFakeDumpCollector(t, &fakeDumpCollector{d: sampleDump(true)}, nil)

	out, err := runDumpCmd(t)
	require.NoError(t, err)

	assert.Contains(t, out, "NVCF Cluster Dump")
	assert.Contains(t, out, "CONTROL PLANE  k3d-ncp-local-cp")
	assert.Contains(t, out, "COMPUTE PLANE  k3d-ncp-local-compute-1")
	assert.Contains(t, out, "nats")
	assert.Contains(t, out, "NVCFBackend")
}

// decodedDump is a relaxed view of the dump JSON for test assertions. It
// deliberately omits HumanDuration fields, which serialize to display strings
// ("2d") and are not meant to round-trip back into their Go type.
type decodedDump struct {
	ControlPlane struct {
		Role string `json:"role"`
	} `json:"controlPlane"`
	ComputePlane *struct {
		Role  string `json:"role"`
		Nodes struct {
			GPUCount int64 `json:"gpuCount"`
		} `json:"nodes"`
	} `json:"computePlane"`
}

func TestClusterDump_JSONOutput(t *testing.T) {
	resetDumpFlags(t)
	jsonOutput = true
	injectFakeDumpCollector(t, &fakeDumpCollector{d: sampleDump(true)}, nil)

	out, err := runDumpCmd(t)
	require.NoError(t, err)
	require.True(t, json.Valid([]byte(out)), "output must be valid JSON")

	var decoded decodedDump
	require.NoError(t, json.Unmarshal([]byte(out), &decoded))
	assert.Equal(t, clusterdump.RoleControlPlane, decoded.ControlPlane.Role)
	require.NotNil(t, decoded.ComputePlane)
	assert.Equal(t, int64(8), decoded.ComputePlane.Nodes.GPUCount)
}

func TestClusterDump_SingleClusterOmitsCompute(t *testing.T) {
	resetDumpFlags(t)
	injectFakeDumpCollector(t, &fakeDumpCollector{d: sampleDump(false)}, nil)

	out, err := runDumpCmd(t)
	require.NoError(t, err)

	assert.Contains(t, out, "CONTROL PLANE")
	assert.NotContains(t, out, "COMPUTE PLANE")
}

func TestClusterDump_OutputFileJSONByExtension(t *testing.T) {
	resetDumpFlags(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "nvcf-dump.json")
	dumpOutputFile = path
	injectFakeDumpCollector(t, &fakeDumpCollector{d: sampleDump(true)}, nil)

	out, err := runDumpCmd(t)
	require.NoError(t, err)
	assert.Empty(t, out, "nothing should be written to stdout when --output is set")

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.True(t, json.Valid(data), "file must contain valid JSON by .json extension")
	var decoded decodedDump
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, clusterdump.RoleControlPlane, decoded.ControlPlane.Role)
}

func TestClusterDump_OutputFileTextByExtension(t *testing.T) {
	resetDumpFlags(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "nvcf-dump.txt")
	dumpOutputFile = path
	injectFakeDumpCollector(t, &fakeDumpCollector{d: sampleDump(false)}, nil)

	_, err := runDumpCmd(t)
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "NVCF Cluster Dump")
	assert.False(t, json.Valid(data), "non-.json extension must produce text, not JSON")
}

func TestClusterDump_CollectorBuildError(t *testing.T) {
	resetDumpFlags(t)
	injectFakeDumpCollector(t, nil, errors.New("kubeconfig not found"))

	_, err := runDumpCmd(t)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "kubeconfig not found")
}

func TestClusterDump_CollectError(t *testing.T) {
	resetDumpFlags(t)
	injectFakeDumpCollector(t, &fakeDumpCollector{err: errors.New("collect failed")}, nil)

	_, err := runDumpCmd(t)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "collect failed")
}

func TestIsArchivePath(t *testing.T) {
	assert.True(t, isArchivePath("/tmp/support.tar.gz"))
	assert.True(t, isArchivePath("support.tgz"))
	assert.False(t, isArchivePath("/tmp/support"))
	assert.False(t, isArchivePath("dump.json"))
}

func TestDumpOptionsFromFlags(t *testing.T) {
	resetDumpFlags(t)

	// No bundle target: no deep sections enabled.
	o, err := dumpOptionsFromFlags()
	require.NoError(t, err)
	assert.False(t, o.Deep())

	// A bundle with no explicit --include enables the default heavy sections.
	dumpBundle = "/tmp/b.tgz"
	o, err = dumpOptionsFromFlags()
	require.NoError(t, err)
	assert.True(t, o.Has(clusterdump.SectionResources))
	assert.True(t, o.Has(clusterdump.SectionLogs))
	assert.True(t, o.Has(clusterdump.SectionHelm))

	// An explicit --include narrows it.
	dumpInclude = []string{"logs"}
	o, err = dumpOptionsFromFlags()
	require.NoError(t, err)
	assert.True(t, o.Has(clusterdump.SectionLogs))
	assert.False(t, o.Has(clusterdump.SectionResources))
}

func TestDumpOptionsFromFlags_UnknownInclude(t *testing.T) {
	resetDumpFlags(t)
	dumpBundle = "/tmp/b.tgz"
	dumpInclude = []string{"resourcess"} // typo

	_, err := dumpOptionsFromFlags()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resourcess")
}

func TestClusterDump_BundleArchive(t *testing.T) {
	resetDumpFlags(t)
	path := filepath.Join(t.TempDir(), "support.tgz")
	dumpBundle = path
	injectFakeDumpCollector(t, &fakeDumpCollector{d: sampleDump(true)}, nil)

	out, err := runDumpCmd(t)
	require.NoError(t, err)
	// The concise summary still prints, and the bundle path is reported.
	assert.Contains(t, out, "NVCF Cluster Dump")
	assert.Contains(t, out, "Diagnostic bundle written to")

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(data), 2)
	assert.Equal(t, byte(0x1f), data[0], "gzip magic byte 0")
	assert.Equal(t, byte(0x8b), data[1], "gzip magic byte 1")

	// The bundle can hold Secret manifests; it must be owner-only.
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "bundle archive must be 0600")
}

func TestClusterDump_BundleDirectory(t *testing.T) {
	resetDumpFlags(t)
	dir := filepath.Join(t.TempDir(), "bundle")
	dumpBundle = dir
	injectFakeDumpCollector(t, &fakeDumpCollector{d: sampleDump(true)}, nil)

	out, err := runDumpCmd(t)
	require.NoError(t, err)
	assert.Contains(t, out, "NVCF Cluster Dump")

	for _, f := range []string{"dump.json", "summary.txt", "upload.txt"} {
		_, statErr := os.Stat(filepath.Join(dir, f))
		assert.NoError(t, statErr, f)
	}
	data, err := os.ReadFile(filepath.Join(dir, "dump.json"))
	require.NoError(t, err)
	assert.True(t, json.Valid(data), "dump.json must be valid JSON")
}
