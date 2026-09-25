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
	"os"
	"testing"

	"nvcf-cli/internal/selfhosted"
)

// Default-stub every seam that would otherwise reach the developer's cluster or
// the network. Individual tests reassign a variable when they explicitly want
// to exercise that path.
//
// These are not conveniences. Without them `go test ./cmd/` creates a hostPath
// busybox pod per node in `default` (the inotify probe), lists Secrets across
// the stack namespaces of whatever kubeconfig happens to be current, and makes
// an outbound request to nvcr.io per configured registry. That mutates a real
// cluster from a unit test, and it is why the package took minutes and failed
// on a proxied kubeconfig rather than seconds and deterministically.
func TestMain(m *testing.M) {
	resolveLatestValidatorTagForSelfHosted = func(_ context.Context, _ string) (string, bool) {
		return "", false
	}
	// Nil prober: the inotify check is skipped rather than creating pods.
	newInotifyProberForSelfHosted = func() selfhosted.NodeInotifyProber { return nil }
	// No cluster contact, and a clean result so the category still renders.
	newStaleNamespaceProberForSelfHosted = func() selfhosted.StaleNamespaceProber {
		return func(context.Context, string, []string) ([]selfhosted.StaleNamespace, error) {
			return nil, nil
		}
	}
	// No outbound registry request.
	newRegistryCredentialCheckerForSelfHosted = func() selfhosted.RegistryCredentialChecker {
		return func(context.Context, string, string, bool) error { return nil }
	}
	os.Exit(m.Run())
}
