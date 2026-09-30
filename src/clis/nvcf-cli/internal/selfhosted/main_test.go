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

package selfhosted

import (
	"os"
	"testing"
)

// TestMain points HOME at an empty directory so no test reads the developer's
// ~/.docker/config.json, or runs a credential helper it names. Tests that need
// a docker config write their own under a temporary HOME. HELM_DRIVER is
// cleared too: an exported sql driver turns off the stale-namespace probe's
// no-release signal, which the probe tests assert on.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "selfhosted-test-home-")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("HOME", home)
	_ = os.Unsetenv("HELM_DRIVER")
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
