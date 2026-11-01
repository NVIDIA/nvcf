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
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// RenderUninstallOpts controls a RenderUninstall call.
type RenderUninstallOpts struct {
	KubeContext string
	Releases    []ReleaseRef // releases to render, in order
	Stdout      io.Writer
	Stderr      io.Writer
}

// ReleaseRef identifies a single Helm release.
type ReleaseRef struct {
	Name      string
	Namespace string
}

// helmGetManifestRunner is the test seam for `helm get manifest` subprocess
// calls. Tests override this to return canned manifests without forking.
var helmGetManifestRunner = func(ctx context.Context, args []string) (string, error) {
	cmd := exec.CommandContext(ctx, "helm", args...)
	out, err := cmd.Output()
	return string(out), err
}

// RenderUninstall writes the YAML manifests currently deployed by each release
// to opts.Stdout, separated by "---". Releases that helm reports as missing
// (already uninstalled) are silently skipped so the call is idempotent against
// partially-torn-down stacks. Any other helm error causes RenderUninstall to
// return immediately.
func RenderUninstall(ctx context.Context, opts RenderUninstallOpts) error {
	first := true
	for _, rel := range opts.Releases {
		args := []string{"get", "manifest", rel.Name, "-n", rel.Namespace}
		if opts.KubeContext != "" {
			args = append(args, "--kube-context="+opts.KubeContext)
		}
		out, err := helmGetManifestRunner(ctx, args)
		if err != nil {
			// helm returns non-zero and prints "release: not found" on stderr
			// when a release has already been uninstalled. Treat this as a
			// no-op so RenderUninstall is safe to call on partially-torn-down
			// stacks.
			if strings.Contains(err.Error(), "not found") || strings.Contains(out, "release: not found") {
				continue
			}
			return fmt.Errorf("helm get manifest %s/%s: %w", rel.Namespace, rel.Name, err)
		}
		if !first {
			fmt.Fprintln(opts.Stdout, "---")
		}
		fmt.Fprint(opts.Stdout, out)
		first = false
	}
	return nil
}
