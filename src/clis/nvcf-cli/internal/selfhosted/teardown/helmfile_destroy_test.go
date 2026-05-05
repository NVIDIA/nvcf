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
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDestroy_ArgsContainDestroy(t *testing.T) {
	var recorded *exec.Cmd
	orig := destroyRunner
	destroyRunner = func(cmd *exec.Cmd) error {
		recorded = cmd
		return nil
	}
	t.Cleanup(func() { destroyRunner = orig })

	opts := DestroyOpts{
		Plane:       "control-plane",
		StackPath:   "/stack",
		Env:         "local",
		KubeContext: "admin@cp",
		Stdout:      &bytes.Buffer{},
		Stderr:      &bytes.Buffer{},
		Ctx:         context.Background(),
	}
	require.NoError(t, Destroy(opts, &discardSink{}))

	require.NotNil(t, recorded)
	args := recorded.Args // [helmfile, -f, ..., --kube-context=admin@cp, destroy]

	assert.Contains(t, args, "destroy")
	// HELMFILE_ENV is communicated via env var (see cmd.Env assertion below),
	// not via `-e` flag. Bundled helmfile only defines environments.default;
	// passing `-e local` would fail with "no releases found".
	assert.NotContains(t, args, "-e")
	assert.Contains(t, args, "--kube-context=admin@cp")
	assert.Equal(t, "/stack", recorded.Dir)
	// HELMFILE_ENV must be in the subprocess env.
	envFound := false
	for _, e := range recorded.Env {
		if e == "HELMFILE_ENV=local" {
			envFound = true
			break
		}
	}
	assert.True(t, envFound, "HELMFILE_ENV=local must be set in cmd.Env: %v", recorded.Env)
}

func TestDestroy_DefaultHelmfileFile(t *testing.T) {
	var recorded *exec.Cmd
	orig := destroyRunner
	destroyRunner = func(cmd *exec.Cmd) error { recorded = cmd; return nil }
	t.Cleanup(func() { destroyRunner = orig })

	opts := DestroyOpts{
		StackPath: "/mystack",
		Env:       "prod",
		Stdout:    &bytes.Buffer{},
		Stderr:    &bytes.Buffer{},
		Ctx:       context.Background(),
	}
	require.NoError(t, Destroy(opts, &discardSink{}))

	args := recorded.Args
	// Without HelmfileFile override, -f should point to StackPath+"/helmfile.yaml.gotmpl"
	assert.Contains(t, args, "/mystack/helmfile.d/")
}

func TestDestroy_HelmfileFileOverride(t *testing.T) {
	var recorded *exec.Cmd
	orig := destroyRunner
	destroyRunner = func(cmd *exec.Cmd) error { recorded = cmd; return nil }
	t.Cleanup(func() { destroyRunner = orig })

	opts := DestroyOpts{
		StackPath:    "/stack",
		HelmfileFile: "helmfile-compute.yaml.gotmpl",
		Env:          "dev",
		Stdout:       &bytes.Buffer{},
		Stderr:       &bytes.Buffer{},
		Ctx:          context.Background(),
	}
	require.NoError(t, Destroy(opts, &discardSink{}))

	args := recorded.Args
	assert.Contains(t, args, "helmfile-compute.yaml.gotmpl")
	// The default StackPath+"/helmfile.yaml.gotmpl" must NOT appear when overridden.
	for _, a := range args {
		assert.NotEqual(t, "/stack/helmfile.d/", a)
	}
}

func TestDestroy_SelectorForwarded(t *testing.T) {
	var recorded *exec.Cmd
	orig := destroyRunner
	destroyRunner = func(cmd *exec.Cmd) error { recorded = cmd; return nil }
	t.Cleanup(func() { destroyRunner = orig })

	opts := DestroyOpts{
		StackPath: "/stack",
		Env:       "local",
		Selector:  "component=control-plane",
		Stdout:    &bytes.Buffer{},
		Stderr:    &bytes.Buffer{},
		Ctx:       context.Background(),
	}
	require.NoError(t, Destroy(opts, &discardSink{}))

	args := recorded.Args
	assert.Contains(t, args, "--selector")
	assert.Contains(t, args, "component=control-plane")
}

func TestDestroy_ExtraEnvForwarded(t *testing.T) {
	var recorded *exec.Cmd
	orig := destroyRunner
	destroyRunner = func(cmd *exec.Cmd) error { recorded = cmd; return nil }
	t.Cleanup(func() { destroyRunner = orig })

	opts := DestroyOpts{
		StackPath: "/stack",
		Env:       "local",
		ExtraEnv:  []string{"CLUSTER_ID=foo", "CLUSTER_NAME=bar"},
		Stdout:    &bytes.Buffer{},
		Stderr:    &bytes.Buffer{},
		Ctx:       context.Background(),
	}
	require.NoError(t, Destroy(opts, &discardSink{}))

	require.NotNil(t, recorded.Env)
	assert.Contains(t, recorded.Env, "CLUSTER_ID=foo")
	assert.Contains(t, recorded.Env, "CLUSTER_NAME=bar")
}
