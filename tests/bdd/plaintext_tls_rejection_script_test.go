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

package bdd_tmp

import (
	"os/exec"
	"strings"
	"testing"
)

func TestPlaintextTLSRejectionAcceptsObservedGrpcurlDeadline(t *testing.T) {
	cmd := exec.Command("bash", "scripts/assert-grpcurl-plaintext-tls-rejection.sh")
	cmd.Stdin = strings.NewReader(
		`Failed to dial target host "127.0.0.1:50071": context deadline exceeded`,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("classify grpcurl deadline: %v\n%s", err, output)
	}
	if got, want := strings.TrimSpace(string(output)), "plaintext-watch-rejected=tls-listener-timeout"; got != want {
		t.Fatalf("normalized output = %q, want %q", got, want)
	}
}

func TestPlaintextTLSRejectionRejectsSnapshotThenRPCDeadline(t *testing.T) {
	cmd := exec.Command("bash", "scripts/assert-grpcurl-plaintext-tls-rejection.sh")
	cmd.Stdin = strings.NewReader(`{
  "stargates": []
}
ERROR:
  Code: DeadlineExceeded
  Message: context deadline exceeded`)

	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("successful plaintext snapshot followed by RPC deadline was accepted: %s", output)
	}
	if strings.Contains(string(output), "plaintext-watch-rejected=") {
		t.Fatalf("successful plaintext snapshot emitted success marker: %s", output)
	}
}

func TestPlaintextTLSRejectionRejectsUnrelatedGrpcurlFailures(t *testing.T) {
	for name, diagnostic := range map[string]string{
		"binary missing":            "bash: grpcurl: command not found",
		"connection refused":        "Failed to dial target host 127.0.0.1:50071: connection refused",
		"dial timeout plus output":  "Failed to dial target host \"127.0.0.1:50071\": context deadline exceeded\n{}",
		"proto import":              "Failed to process proto source files.: missing.proto does not reside in any import path",
		"usage":                     "flag provided but not defined: -bad-flag",
		"wrong target dial timeout": "Failed to dial target host \"127.0.0.1:50443\": context deadline exceeded",
	} {
		t.Run(name, func(t *testing.T) {
			cmd := exec.Command("bash", "scripts/assert-grpcurl-plaintext-tls-rejection.sh")
			cmd.Stdin = strings.NewReader(diagnostic)

			output, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("unrelated failure was accepted: %s", output)
			}
			if strings.Contains(string(output), "plaintext-watch-rejected=") {
				t.Fatalf("unrelated failure emitted success marker: %s", output)
			}
		})
	}
}
