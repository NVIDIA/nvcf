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
	"io"
	"os"
	"os/exec"
)

// RenderOptions controls a single invocation of 'helmfile template' or
// 'helmfile apply'. Stdout/Stderr writers are caller-supplied so the install
// command can split YAML output (stdout) from helmfile progress (stderr) per
// spec §6.1.
type RenderOptions struct {
	// StackPath is the directory containing helmfile.d/.
	StackPath string
	// HelmfileFile (optional) overrides the default helmfile target. Empty
	// resolves to "helmfile.d/" (the bundled-stack layout). Set to a relative
	// file path (e.g. "helmfile-nvca-operator.yaml.gotmpl") to drive a stack
	// that splits compute-plane into a separate file (multi-cluster topology).
	HelmfileFile string
	// Env is the HELMFILE_ENV value passed via environment variable to helmfile.
	Env string
	// HelmfileBin is the path to the helmfile binary. Defaults to "helmfile"
	// (resolved via PATH) when empty.
	HelmfileBin string
	// Selector (optional) is the -l flag value for narrowing to a single
	// release group (e.g. "component=control-plane").
	Selector string
	// Apply runs 'helmfile apply' when true; otherwise runs 'helmfile template'.
	// Apply=true is reserved for the up orchestrator (M5).
	Apply bool
	// ExtraEnv holds additional environment variables in "KEY=VALUE" form that
	// are appended to the helmfile subprocess environment after HELMFILE_ENV.
	// Used by install --compute-plane to forward CLUSTER_ID, CLUSTER_GROUP_ID,
	// CLUSTER_NAME, and IDENTITY_SOURCE into the worker-layer render.
	ExtraEnv []string

	// KubeContext, when non-empty, is passed to helmfile via --kube-context.
	// Helmfile threads this through to kubectl + helm subprocesses, so split-
	// cluster `up` can target the control plane and compute plane on different
	// kubeconfig contexts within the same kubeconfig file. Empty value means
	// "use whatever the current context is" — preserves single-cluster behavior.
	KubeContext string

	Stdout io.Writer
	Stderr io.Writer
	Ctx    context.Context
}

// Render invokes 'helmfile {template,apply}' against the resolved stack tree,
// plumbing HELMFILE_ENV through the subprocess environment. Stdout and Stderr
// are written to the caller-supplied writers. A non-zero exit code from
// helmfile is returned as a wrapped error containing the exit status.
func Render(opts RenderOptions) error {
	if opts.HelmfileBin == "" {
		opts.HelmfileBin = "helmfile"
	}
	target := opts.HelmfileFile
	if target == "" {
		target = "helmfile.d/"
	}
	args := []string{"-f", opts.StackPath + "/" + target}
	if opts.Selector != "" {
		args = append(args, "-l", opts.Selector)
	}
	if opts.KubeContext != "" {
		args = append(args, "--kube-context="+opts.KubeContext)
	}
	if opts.Apply {
		args = append(args, "apply")
	} else {
		args = append(args, "template")
	}

	ctx := opts.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, opts.HelmfileBin, args...)
	cmd.Stdout = opts.Stdout
	cmd.Stderr = opts.Stderr
	cmd.Env = append(os.Environ(), "HELMFILE_ENV="+opts.Env)
	cmd.Env = append(cmd.Env, opts.ExtraEnv...)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("helmfile %v: %w", args, err)
	}
	return nil
}
