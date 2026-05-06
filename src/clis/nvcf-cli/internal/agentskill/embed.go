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

// Package agentskill embeds the nvcf-cli agent skill markdown bundle and
// provides install/uninstall/verify operations. The bundle source of truth
// lives at mcamp/docs/nvcf/one-click-deploy/agent-skill/; this package's
// data/ subdirectory is a release-time copy verified against manifest.json.
package agentskill

import "embed"

//go:embed all:data
var embeddedFS embed.FS

// FS returns the embedded skill files (rooted at "data").
func FS() embed.FS {
	return embeddedFS
}
