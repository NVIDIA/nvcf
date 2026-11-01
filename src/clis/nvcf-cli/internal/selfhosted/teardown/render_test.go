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

package teardown

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderUninstall_CombinesManifests(t *testing.T) {
	manifests := map[string]string{
		"nvcf-api":     "kind: Deployment\nmetadata:\n  name: nvcf-api\n",
		"sis":          "kind: StatefulSet\nmetadata:\n  name: sis\n",
		"cassandra-db": "kind: StatefulSet\nmetadata:\n  name: cassandra\n",
	}

	orig := helmGetManifestRunner
	helmGetManifestRunner = func(_ context.Context, args []string) (string, error) {
		// args: [get, manifest, <name>, -n, <ns>]
		name := args[2]
		m, ok := manifests[name]
		if !ok {
			return "", errors.New("release: not found")
		}
		return m, nil
	}
	t.Cleanup(func() { helmGetManifestRunner = orig })

	var out bytes.Buffer
	opts := RenderUninstallOpts{
		Releases: []ReleaseRef{
			{Name: "nvcf-api", Namespace: "nvcf-system"},
			{Name: "sis", Namespace: "sis-system"},
			{Name: "cassandra-db", Namespace: "cassandra-system"},
		},
		Stdout: &out,
		Stderr: &bytes.Buffer{},
	}
	require.NoError(t, RenderUninstall(context.Background(), opts))

	got := out.String()
	assert.Contains(t, got, "name: nvcf-api")
	assert.Contains(t, got, "name: sis")
	assert.Contains(t, got, "name: cassandra")

	// Manifests must be separated by "---"
	assert.Contains(t, got, "---")
}

func TestRenderUninstall_SkipsNotFound(t *testing.T) {
	orig := helmGetManifestRunner
	helmGetManifestRunner = func(_ context.Context, args []string) (string, error) {
		name := args[2]
		if name == "already-gone" {
			return "", errors.New("release: not found")
		}
		return "kind: Deployment\nmetadata:\n  name: " + name + "\n", nil
	}
	t.Cleanup(func() { helmGetManifestRunner = orig })

	var out bytes.Buffer
	opts := RenderUninstallOpts{
		Releases: []ReleaseRef{
			{Name: "still-there", Namespace: "ns"},
			{Name: "already-gone", Namespace: "ns"},
		},
		Stdout: &out,
		Stderr: &bytes.Buffer{},
	}
	require.NoError(t, RenderUninstall(context.Background(), opts))

	got := out.String()
	assert.Contains(t, got, "still-there")
	assert.NotContains(t, got, "already-gone")
	// Only one manifest → no separator needed.
	assert.NotContains(t, got, "---")
}

func TestRenderUninstall_PropagatesRealError(t *testing.T) {
	orig := helmGetManifestRunner
	helmGetManifestRunner = func(_ context.Context, _ []string) (string, error) {
		return "", errors.New("connection refused")
	}
	t.Cleanup(func() { helmGetManifestRunner = orig })

	var out bytes.Buffer
	opts := RenderUninstallOpts{
		Releases: []ReleaseRef{{Name: "foo", Namespace: "bar"}},
		Stdout:   &out,
		Stderr:   &bytes.Buffer{},
	}
	err := RenderUninstall(context.Background(), opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "helm get manifest")
}

func TestRenderUninstall_KubeContextForwarded(t *testing.T) {
	var capturedArgs []string
	orig := helmGetManifestRunner
	helmGetManifestRunner = func(_ context.Context, args []string) (string, error) {
		capturedArgs = args
		return "kind: Deployment\n", nil
	}
	t.Cleanup(func() { helmGetManifestRunner = orig })

	var out bytes.Buffer
	opts := RenderUninstallOpts{
		KubeContext: "admin@cp",
		Releases:    []ReleaseRef{{Name: "foo", Namespace: "nvcf-system"}},
		Stdout:      &out,
		Stderr:      &bytes.Buffer{},
	}
	require.NoError(t, RenderUninstall(context.Background(), opts))

	assert.Contains(t, capturedArgs, "--kube-context=admin@cp")
}
