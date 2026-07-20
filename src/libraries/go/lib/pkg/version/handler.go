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

package version

import (
	"encoding/json"
	"net/http"
	"runtime/debug"
)

// Info holds the resolved build version metadata returned by the /version endpoint.
type Info struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

// Handler returns an http.Handler that serves build version info as JSON.
// Version and Commit are resolved from the package-level ldflags vars
// (Version, GitHash), with buildinfo used as a fallback for Commit
// when GitHash is empty.
func Handler() http.Handler {
	return HandlerFor(Version, GitHash)
}

// HandlerFor returns an http.Handler using the provided version and commit
// values, falling back to buildinfo for commit when commit is empty.
// Use this for services that maintain their own ldflags vars rather than
// pointing directly at this package.
func HandlerFor(ver, commit string) http.Handler {
	info := resolve(ver, commit)
	body, _ := json.Marshal(info)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	})
}

// resolve builds an Info by combining the given ver/commit,
// falling back to runtime buildinfo for commit when empty.
func resolve(ver, commit string) Info {
	if commit == "" {
		commit = commitFromBuildInfo()
	}
	if ver == "" {
		ver = "unknown"
	}
	if commit == "" {
		commit = "unknown"
	}
	return Info{Version: ver, Commit: commit}
}

// commitFromBuildInfo reads the VCS revision from Go's embedded build info
// (available when built inside a git repository without -buildvcs=false).
func commitFromBuildInfo() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" {
			return s.Value
		}
	}
	return ""
}
