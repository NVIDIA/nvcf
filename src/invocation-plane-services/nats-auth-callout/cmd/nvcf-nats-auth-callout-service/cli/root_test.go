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

package cli

import (
	"testing"
)

func TestNewRootCommand(t *testing.T) {
	cmd := NewRootCommand()
	if cmd == nil {
		t.Fatal("Expected root command to be created, got nil")
	}
	if cmd.Use != "nvcf-nats-auth-callout-service" {
		t.Errorf("Expected Use to be 'nvcf-nats-auth-callout-service', got '%s'", cmd.Use)
	}
	if len(cmd.Commands()) == 0 {
		t.Error("Expected at least one subcommand to be added")
	}
	// Check that the subcommand is 'server'
	found := false
	for _, c := range cmd.Commands() {
		if c.Use == "server" {
			found = true
		}
	}
	if !found {
		t.Error("Expected 'server' subcommand to be present")
	}
}
