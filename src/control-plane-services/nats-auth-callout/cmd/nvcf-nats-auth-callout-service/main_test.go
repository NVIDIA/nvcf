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

package main

import (
	"os"
	"testing"
)

// TestMainFunction ensures the main function runs without panicking for a known-safe subcommand.
func TestMainFunction(t *testing.T) {
	old := os.Args
	os.Args = []string{"nvcf-nats-auth-callout-service", "version"}
	defer func() {
		os.Args = old
		if r := recover(); r != nil {
			t.Errorf("main() panicked with error: %v", r)
		}
	}()
	main()
}
