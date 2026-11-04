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

package runtime

import (
	"fmt"
	"os"
	"strings"
)

// DefaultSocketPaths lists candidate socket paths in probe order.
// First existing socket wins; operators can override with an explicit path.
var DefaultSocketPaths = []struct {
	Path string
	Type Type
}{
	{"/run/containerd/containerd.sock", TypeContainerd},
	{"/var/run/containerd/containerd.sock", TypeContainerd},
	{"/run/crio/crio.sock", TypeCRIO},
	{"/var/run/crio/crio.sock", TypeCRIO},
}

// Detect returns the socket path and runtime type to use.
//
// If hint is non-empty AND the socket exists, it is used — the type is
// inferred from the path ("containerd" or "crio" substring).
//
// If hint is empty, or hint is set but the socket doesn't exist (common when
// the default /run/containerd/containerd.sock flag value is used on a CRI-O
// node), DefaultSocketPaths are probed in order and the first existing
// socket is returned.
//
// Returns an error if no runtime is detectable.
func Detect(hint string) (string, Type, error) {
	if hint != "" {
		if _, err := os.Stat(hint); err == nil {
			t := typeFromPath(hint)
			if t == "" {
				return "", "", fmt.Errorf("cannot infer runtime type from socket path %q (expected 'containerd' or 'crio' in path)", hint)
			}
			return hint, t, nil
		}
		// Hint socket doesn't exist — fall through to auto-detection below.
		// This is important for daemonsets deployed with a default
		// --containerd-socket flag onto CRI-O nodes.
	}

	for _, candidate := range DefaultSocketPaths {
		if _, err := os.Stat(candidate.Path); err == nil {
			return candidate.Path, candidate.Type, nil
		}
	}

	return "", "", fmt.Errorf("no container runtime socket found (tried %d paths)", len(DefaultSocketPaths))
}

func typeFromPath(path string) Type {
	switch {
	case strings.Contains(path, "containerd"):
		return TypeContainerd
	case strings.Contains(path, "crio"):
		return TypeCRIO
	default:
		return ""
	}
}
