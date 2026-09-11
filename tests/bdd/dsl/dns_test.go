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
	"strings"
	"testing"
)

func TestDNSResolutionCommandInterpolatesAndBuildsScriptCommand(t *testing.T) {
	t.Setenv("BDD_DNS_NAME", "api.192-0-2-10.nip.io")
	t.Setenv("BDD_DNS_TIMEOUT", "180")

	got, err := DNSResolutionCommand(" ${BDD_DNS_NAME} ", " ${BDD_DNS_TIMEOUT} ")
	if err != nil {
		t.Fatalf("build DNS resolution command: %v", err)
	}
	want := "tests/bdd/scripts/wait-for-dns.sh api.192-0-2-10.nip.io 180"
	if got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
}

func TestDNSResolutionCommandNormalizesAndQuotesArguments(t *testing.T) {
	got, err := DNSResolutionCommand("gateway name", "0180")
	if err != nil {
		t.Fatalf("build DNS resolution command: %v", err)
	}
	want := "tests/bdd/scripts/wait-for-dns.sh 'gateway name' 180"
	if got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
}

func TestDNSResolutionCommandRejectsInvalidInputs(t *testing.T) {
	tests := []struct {
		name     string
		hostname string
		timeout  string
		want     string
	}{
		{name: "empty hostname", hostname: "   ", timeout: "180", want: "DNS name is empty"},
		{name: "missing hostname variable", hostname: "${BDD_DNS_NAME_MISSING}", timeout: "180", want: "DNS name is empty"},
		{name: "empty timeout", hostname: "gateway.example.com", timeout: " ", want: "timeout is empty"},
		{name: "negative timeout", hostname: "gateway.example.com", timeout: "-1", want: "not a non-negative integer"},
		{name: "duration timeout", hostname: "gateway.example.com", timeout: "3m", want: "not a non-negative integer"},
		{name: "overflowing timeout", hostname: "gateway.example.com", timeout: "9223372036854775808", want: "timeout \"9223372036854775808\" is invalid"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := DNSResolutionCommand(test.hostname, test.timeout)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestDNSResolutionCommandAllowsImmediateTimeout(t *testing.T) {
	got, err := DNSResolutionCommand("gateway.example.com", "0")
	if err != nil {
		t.Fatalf("build DNS resolution command: %v", err)
	}
	want := "tests/bdd/scripts/wait-for-dns.sh gateway.example.com 0"
	if got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
}
