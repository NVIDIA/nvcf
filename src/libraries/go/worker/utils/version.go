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

package utils

import (
	"runtime/debug"

	"github.com/carlmjohnson/versioninfo"
)

var Version = "unknown"

func init() {
	// try for a tag
	if !versioninfo.DirtyBuild {
		if info, ok := debug.ReadBuildInfo(); ok {
			for _, kv := range info.Settings {
				if kv.Value == "" {
					continue
				}
				switch kv.Key {
				case "vcs.tag":
					Version = kv.Value
					return
				}
			}
		}
	}
	if versioninfo.Version != "unknown" && versioninfo.Version != "(devel)" {
		Version = versioninfo.Version
		return
	}
	// fallback to a commit sha
	if versioninfo.Revision != "unknown" && versioninfo.Revision != "" {
		Version = versioninfo.Revision
		return
	}
}
