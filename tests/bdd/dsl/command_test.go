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

package dsl

import "testing"

func TestBuildCommandPreservesArgumentsForShlex(t *testing.T) {
	got := BuildCommand(
		"/tmp/nvcf cli",
		"--config",
		"",
		"--llm-model",
		"name=model,uris=/v1/chat|/v1/embed,routingMethod=not-an-api-value",
		"value with 'quotes'",
	)
	want := "'/tmp/nvcf cli' --config '' --llm-model 'name=model,uris=/v1/chat|/v1/embed,routingMethod=not-an-api-value' 'value with '\"'\"'quotes'\"'\"''"
	if got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
}
