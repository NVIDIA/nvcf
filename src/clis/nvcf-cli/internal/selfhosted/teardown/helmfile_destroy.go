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

// Package teardown implements the helpers that the `nvcf self-hosted down`
// orchestrator composes: helmfile destroy, drain, unregister, PVC removal,
// manifest rendering, and plan dry-run.
package teardown

import (
	"context"
	"io"
	"os"
	"os/exec"

	"nvcf-cli/internal/selfhosted/progress"
)

// DestroyOpts controls a single invocation of 'helmfile destroy'. Mirrors
// selfhosted.RenderOptions for the destroy direction.
type DestroyOpts struct {
	Plane        string // "control-plane" | "compute-plane"
	ClusterName  string // required for compute-plane; threaded into ExtraEnv
	KubeContext  string
	StackPath    string
	HelmfileFile string // override; if empty, defaults to StackPath+"/helmfile.d/" (the bundled layout, mirrors selfhosted.Render)
	Selector     string
	Env          string
	ExtraEnv     []string
	Stdout       io.Writer
	Stderr       io.Writer
	Ctx          context.Context
}

// destroyRunner is the test seam for the helmfile subprocess. Tests override
// this to capture the constructed *exec.Cmd without actually forking. The
// production value calls cmd.Run() directly, mirroring the pattern in
// internal/selfhosted/render.go (selfhostedRunner).
var destroyRunner = func(cmd *exec.Cmd) error { return cmd.Run() }

// Destroy runs `helmfile destroy` for the given plane. Returns the helmfile
// exit error on failure.
//
// TODO(M+11.I): parse helmfile stderr line-by-line and emit per-release
// phase_progress events into sink as releases are removed.
func Destroy(opts DestroyOpts, _ progress.EventSink) error {
	var args []string
	if opts.HelmfileFile != "" {
		args = append(args, "-f", opts.HelmfileFile)
	} else {
		// Default mirrors selfhosted.Render's "helmfile.d/" target (the bundled
		// stack layout). When the helmfile target is a directory, helmfile
		// recursively finds *.yaml in it.
		args = append(args, "-f", opts.StackPath+"/helmfile.d/")
	}
	// NOTE: do NOT pass `-e $env` here. The bundled helmfile defines only
	// `environments.default` (and reads HELMFILE_ENV at template time);
	// passing `-e local` makes helmfile look for `environments.local` which
	// doesn't exist and reports "no releases found that matches selector()
	// and environment(local)". Mirroring selfhosted.Render: HELMFILE_ENV
	// env var is the sole communication channel.
	if opts.KubeContext != "" {
		args = append(args, "--kube-context="+opts.KubeContext)
	}
	if opts.Selector != "" {
		args = append(args, "--selector", opts.Selector)
	}
	args = append(args, "destroy")

	ctx := opts.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, "helmfile", args...)
	cmd.Stdout = opts.Stdout
	cmd.Stderr = opts.Stderr
	cmd.Dir = opts.StackPath
	// Mirror selfhosted.Render's env setup so helmfile templates that read
	// `requiredEnv "HELMFILE_ENV"` (and any other inherited env like PATH /
	// KUBECONFIG / KUBE_CONTEXT) work the same way they do for `up`.
	cmd.Env = append(os.Environ(), "HELMFILE_ENV="+opts.Env)
	cmd.Env = append(cmd.Env, opts.ExtraEnv...)

	return destroyRunner(cmd)
}
