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

package cmd

import (
	"os"
	"path/filepath"
	"strings"

	"nvcf-cli/internal/selfhosted"
)

func selfHostedPreflightTools() []selfhosted.BinarySpec {
	return selfhosted.DefaultToolsWithPreferredDir(localStackBinDir(selfHostedStack))
}

func localStackBinDir(stackSource string) string {
	if stackSource == "" || strings.HasPrefix(stackSource, "oci://") ||
		strings.HasPrefix(stackSource, "git@") ||
		strings.HasPrefix(stackSource, "file://") ||
		(strings.HasPrefix(stackSource, "https://") && strings.Contains(stackSource, ".git")) {
		return ""
	}
	abs, err := filepath.Abs(stackSource)
	if err != nil {
		return ""
	}
	bin := filepath.Join(abs, "bin")
	if info, err := os.Stat(bin); err == nil && info.IsDir() {
		return bin
	}
	return ""
}
