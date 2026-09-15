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
package gateway

import (
	"os"
	"strings"

	"github.com/carlmjohnson/versioninfo"
)

// version is injected at link time via x_defs in the root BUILD.bazel when
// building with --stamp. Unstamped builds leave the placeholder or an empty
// string, so GetVersion falls through to versioninfo.
var version string

func GetVersion() string {
	if v := os.Getenv("APP_VERSION"); v != "" {
		return v
	}
	if version != "" && !strings.Contains(version, "{") {
		return version
	}
	return versioninfo.Revision
}
