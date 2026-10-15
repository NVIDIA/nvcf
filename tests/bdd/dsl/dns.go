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

import (
	"fmt"
	"strconv"
	"strings"
)

const waitForDNSScript = "tests/bdd/scripts/wait-for-dns.sh"

// DNSResolutionCommand builds the existing host-resolver polling command after
// resolving interpolation and validating its visible inputs.
func DNSResolutionCommand(hostname, timeout string) (string, error) {
	hostname = strings.TrimSpace(Interpolate(hostname))
	timeout = strings.TrimSpace(Interpolate(timeout))
	if hostname == "" {
		return "", fmt.Errorf("DNS name is empty")
	}
	if timeout == "" {
		return "", fmt.Errorf("DNS resolution timeout is empty")
	}
	for _, char := range timeout {
		if char < '0' || char > '9' {
			return "", fmt.Errorf("DNS resolution timeout %q is not a non-negative integer", timeout)
		}
	}
	timeoutSeconds, err := strconv.ParseInt(timeout, 10, 64)
	if err != nil {
		return "", fmt.Errorf("DNS resolution timeout %q is invalid: %w", timeout, err)
	}
	timeout = strconv.FormatInt(timeoutSeconds, 10)

	return BuildCommand(waitForDNSScript, hostname, timeout), nil
}
