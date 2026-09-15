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
	"context"
	"fmt"

	"nvcf-cli/internal/selfhosted"
)

var resolveSelfHostedHelmRuntimeMode = func(ctx context.Context) (selfhosted.HelmRuntimeMode, error) {
	results := runUpPreflight(ctx, selfhosted.PreflightConfig{
		LocalOnly: true,
		Tools:     selfHostedHelmRuntimePreflightTools(),
	})
	for _, result := range results {
		if !result.Passed && result.Severity == "error" {
			return "", fmt.Errorf("%s: %s", result.ID, result.Message)
		}
	}
	return helmRuntimeModeFromPreflightResults(results), nil
}

func selfHostedHelmRuntimePreflightTools() []selfhosted.BinarySpec {
	tools := selfHostedPreflightTools()
	selected := make([]selfhosted.BinarySpec, 0, 2)
	for _, tool := range tools {
		switch tool.Name {
		case "helm", "helmfile":
			selected = append(selected, tool)
		}
	}
	return selected
}
