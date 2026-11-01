//go:build e2e
// +build e2e

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

// Shared helpers for e2e tests that are NOT gated on the legacy NVCF_E2E=1 init()
// panic pattern. Tests that use requireE2E / requireNvcfBin can live outside the
// self_hosted_idempotency_test.go file without inheriting its panic-on-missing-env
// init() guard.
package e2e

import (
	"os"
	"testing"
)

// requireE2E skips the calling test unless NVCF_E2E=1 is set. Add this call at
// the top of every test that requires a live k3d cluster.
func requireE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("NVCF_E2E") != "1" {
		t.Skip("set NVCF_E2E=1 to run e2e tests (requires live k3d cluster, helm, kubectl)")
	}
}

// requireNvcfBin returns the path to the nvcf CLI binary. It prefers
// NVCF_BIN from the environment, then falls back to the canonical dev-VM
// location /tmp/nvcf-cli (matching the idempotency test's cliBin const).
func requireNvcfBin(t *testing.T) string {
	t.Helper()
	if b := os.Getenv("NVCF_BIN"); b != "" {
		return b
	}
	return cliBin // /tmp/nvcf-cli from self_hosted_idempotency_test.go
}

// envOr returns the value of the named environment variable, or fallback when
// the variable is unset or empty.
func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
