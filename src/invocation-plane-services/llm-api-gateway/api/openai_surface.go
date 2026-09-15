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

package api

import "strings"

func normalizePublicOpenAIPath(path string) string {
	switch {
	case strings.HasPrefix(path, "/api/openai/"):
		return "/openai" + strings.TrimPrefix(path, "/api/openai")
	case strings.HasPrefix(path, "/openai/"):
		return path
	case strings.HasPrefix(path, "/v1/"):
		return "/openai" + path
	default:
		return path
	}
}

func stargatePath(path string) string {
	if strings.HasPrefix(path, "/api/openai/") {
		return strings.TrimPrefix(path, "/api/openai")
	}
	if strings.HasPrefix(path, "/openai/") {
		return strings.TrimPrefix(path, "/openai")
	}
	return path
}
