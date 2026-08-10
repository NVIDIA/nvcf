/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package agent

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gorilla/mux"
)

// resolveWithinRoot validates a caller-supplied relative path against a
// serving root and returns the fully symlink-resolved absolute path that
// is safe to open. It defends the agent's file-serving endpoints
// (/v1/checkpoints/{id}/file, /v1/captures/{hash}/file) against symlink
// traversal (nvsnap#92): a symlink committed inside a checkpoint/capture
// tree must not let a peer read files outside the tree.
//
// The earlier lexical-only check was insufficient on two counts:
//   - a bare strings.HasPrefix(target, root) lets "<root>-evil" pass, and
//   - os.Stat/os.Open follow symlinks, so a link inside the tree pointing
//     at /etc/passwd would be served despite the lexical check passing.
//
// Defense: reject "..", then resolve symlinks on BOTH the root and the
// target with filepath.EvalSymlinks (which also confirms the target
// exists), and require the resolved target to equal the resolved root or
// sit under it with a path-separator boundary. A symlink escaping the
// tree resolves to a path outside realRoot and is rejected.
//
// Returns the resolved absolute path on success. Errors are intentionally
// generic (callers map them to 400/404) so we don't leak filesystem layout.
func resolveWithinRoot(root, relPath string) (string, error) {
	cleaned := filepath.Clean("/" + relPath) // anchor so "../" can't climb above /
	if cleaned == "/" {
		return "", fmt.Errorf("empty path")
	}
	target := filepath.Join(root, cleaned)

	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve root: %w", err)
	}
	realTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", fmt.Errorf("resolve target: %w", err)
	}

	if realTarget != realRoot && !strings.HasPrefix(realTarget, realRoot+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes serving root")
	}
	return realTarget, nil
}

// pathSegment is the shape every identifier that becomes a directory name
// must have: one benign path component. Deliberately a shape check rather
// than the exact "<shorthash>__<timestamp>" buildCheckpointID emits, so
// checkpoints written by older agents stay readable; the security property
// is "cannot leave the parent directory", not "matches today's generator".
var pathSegment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// validPathSegment rejects an identifier that would not be a single, benign
// path component.
//
// Checkpoint IDs, capture hashes and pod UIDs all arrive over the agent's
// HTTP API and are joined onto a host directory with no further checking:
//
//	checkpointDir := filepath.Join(a.config.CheckpointDir, req.CheckpointID)
//
// The agent runs privileged with hostPath mounts covering /var/lib and the
// containerd root, so a "../" here is not a contained bug -- it is a read or
// write anywhere on the node as root. Leading dots are refused too, since a
// bare ".." is otherwise a legal segment.
func validPathSegment(kind, s string) error {
	if s == "" {
		return fmt.Errorf("%s is required", kind)
	}
	if !pathSegment.MatchString(s) {
		return fmt.Errorf("%s %q is not a valid identifier", kind, s)
	}
	return nil
}

// pathVarGuard validates the route variables that name a directory before
// any handler runs.
//
// Applied as router middleware rather than at each call site: the agent has
// a dozen {id}/{hash}/{pod-uid} routes today, and a per-handler check is one
// forgotten line away from reopening the hole on the next route added. Here
// a new route is covered by construction.
func pathVarGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, k := range []string{"id", "hash", "pod-uid"} {
			v, ok := mux.Vars(r)[k]
			if !ok {
				continue
			}
			if err := validPathSegment(k, v); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// joinWithinRoot is the write-side counterpart to resolveWithinRoot: it
// returns where a file named by an untrusted relative path may be created
// under root, without requiring that the file already exist.
//
// The relative paths driving cascade fetch come from a manifest served by
// another agent over HTTP, and are joined straight onto the local
// destination directory. Two escapes are possible and both are closed here:
// "../" in the manifest entry (handled by anchoring the clean at "/"), and a
// symlink the peer planted earlier in the same transfer -- send "cache" as a
// link to /etc, then "cache/cron.d/x" as a regular file. Since the target
// itself does not exist yet, the deepest ancestor that does exist is the one
// resolved and boundary-checked.
func joinWithinRoot(root, relPath string) (string, error) {
	cleaned := filepath.Clean("/" + relPath)
	if cleaned == "/" {
		return "", fmt.Errorf("empty path")
	}
	target := filepath.Join(root, cleaned)

	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve root: %w", err)
	}

	// Walk up to the deepest component that exists; everything below it is
	// ours to create, so it cannot be a symlink we did not just make.
	probe := target
	for {
		resolved, rErr := filepath.EvalSymlinks(probe)
		if rErr == nil {
			if resolved != realRoot && !strings.HasPrefix(resolved, realRoot+string(os.PathSeparator)) {
				return "", fmt.Errorf("path escapes destination root")
			}
			return target, nil
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return "", fmt.Errorf("resolve path: %w", rErr)
		}
		probe = parent
	}
}
