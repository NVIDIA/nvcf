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
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRender_ShellsOutAndCapturesYAML(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "helmfile")
	// Use printf instead of echo so \n is interpreted correctly on both
	// macOS (sh -> bash) and Linux (sh -> dash).
	body := "#!/bin/sh\nprintf 'apiVersion: v1\\nkind: ConfigMap\\nmetadata:\\n  name: fake\\n'\n"
	require.NoError(t, os.WriteFile(fake, []byte(body), 0o755))

	stack := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(stack, "helmfile.d"), 0o755))

	var stdout, stderr bytes.Buffer
	err := Render(RenderOptions{
		StackPath:   stack,
		Env:         "local",
		HelmfileBin: fake,
		Stdout:      &stdout,
		Stderr:      &stderr,
	})
	require.NoError(t, err)
	assert.Contains(t, stdout.String(), "kind: ConfigMap")
}

func TestRender_ExtraEnvIsForwarded(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "helmfile")
	// Fake helmfile prints $CLUSTER_NAME to stdout so we can assert it was forwarded.
	require.NoError(t, os.WriteFile(fake,
		[]byte("#!/bin/sh\nprintf 'cluster=%s\\n' \"$CLUSTER_NAME\"\n"), 0o755))

	stack := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(stack, "helmfile.d"), 0o755))

	var stdout, stderr bytes.Buffer
	err := Render(RenderOptions{
		StackPath:   stack,
		Env:         "local",
		HelmfileBin: fake,
		ExtraEnv:    []string{"CLUSTER_NAME=ncp-test"},
		Stdout:      &stdout, Stderr: &stderr,
	})
	require.NoError(t, err)
	assert.Contains(t, stdout.String(), "cluster=ncp-test")
}

func TestRender_NonZeroExitPropagates(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "helmfile")
	require.NoError(t, os.WriteFile(fake, []byte("#!/bin/sh\nexit 7\n"), 0o755))

	stack := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(stack, "helmfile.d"), 0o755))

	var out, errb bytes.Buffer
	err := Render(RenderOptions{
		StackPath: stack, Env: "local",
		HelmfileBin: fake, Stdout: &out, Stderr: &errb,
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "exit status 7")
}

// TestRender_KubeContextFlag asserts that a non-empty KubeContext is forwarded
// to helmfile as --kube-context=<value> before the verb argument.
func TestRender_KubeContextFlag(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "helmfile")
	// Fake helmfile prints its arguments to stdout so we can inspect them.
	require.NoError(t, os.WriteFile(fake,
		[]byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0o755))

	stack := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(stack, "helmfile.d"), 0o755))

	var stdout, stderr bytes.Buffer
	err := Render(RenderOptions{
		StackPath:   stack,
		Env:         "local",
		HelmfileBin: fake,
		KubeContext: "admin@cp",
		Stdout:      &stdout,
		Stderr:      &stderr,
	})
	require.NoError(t, err)
	out := stdout.String()
	assert.Contains(t, out, "--kube-context=admin@cp",
		"helmfile should receive --kube-context when KubeContext is set")
	// Verify the flag precedes the verb.
	contextPos := strings.Index(out, "--kube-context=admin@cp")
	verbPos := strings.Index(out, "template")
	assert.Less(t, contextPos, verbPos,
		"--kube-context flag should appear before the verb in the argument list")
}

func TestRender_HelmfileDirectoryDoesNotEmitSequentialFlag(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "helmfile")
	require.NoError(t, os.WriteFile(fake,
		[]byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0o755))

	stack := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(stack, "helmfile.d"), 0o755))

	var stdout, stderr bytes.Buffer
	err := Render(RenderOptions{
		StackPath:   stack,
		Env:         "local",
		HelmfileBin: fake,
		Stdout:      &stdout,
		Stderr:      &stderr,
	})
	require.NoError(t, err)
	out := stdout.String()
	assert.NotContains(t, out, "--sequential-helmfiles")
}

// TestRender_NoKubeContextOmitsFlag asserts that when KubeContext is empty no
// --kube-context flag is emitted, preserving existing single-cluster behavior.
func TestRender_NoKubeContextOmitsFlag(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "helmfile")
	require.NoError(t, os.WriteFile(fake,
		[]byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0o755))

	stack := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(stack, "helmfile.d"), 0o755))

	var stdout, stderr bytes.Buffer
	err := Render(RenderOptions{
		StackPath:   stack,
		Env:         "local",
		HelmfileBin: fake,
		KubeContext: "", // explicitly empty
		Stdout:      &stdout,
		Stderr:      &stderr,
	})
	require.NoError(t, err)
	assert.NotContains(t, stdout.String(), "--kube-context",
		"no --kube-context flag should be emitted when KubeContext is empty")
}
