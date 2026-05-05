// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import "testing"

func TestParseArgsForceShort(t *testing.T) {
	t.Parallel()

	opts, err := parseArgs([]string{"-f"})
	if err != nil {
		t.Fatalf("parseArgs(-f): %v", err)
	}
	if !opts.force {
		t.Fatal("expected -f to enable force mode")
	}
}

func TestParseArgsForceLong(t *testing.T) {
	t.Parallel()

	opts, err := parseArgs([]string{"--force"})
	if err != nil {
		t.Fatalf("parseArgs(--force): %v", err)
	}
	if !opts.force {
		t.Fatal("expected --force to enable force mode")
	}
}

func TestParseArgsVerboseShort(t *testing.T) {
	t.Parallel()

	opts, err := parseArgs([]string{"-v"})
	if err != nil {
		t.Fatalf("parseArgs(-v): %v", err)
	}
	if !opts.verbose {
		t.Fatal("expected -v to enable verbose mode")
	}
}

func TestParseArgsVerboseLong(t *testing.T) {
	t.Parallel()

	opts, err := parseArgs([]string{"--verbose"})
	if err != nil {
		t.Fatalf("parseArgs(--verbose): %v", err)
	}
	if !opts.verbose {
		t.Fatal("expected --verbose to enable verbose mode")
	}
}

func TestParseArgsRejectsUnexpectedPositionalArgs(t *testing.T) {
	t.Parallel()

	if _, err := parseArgs([]string{"extra"}); err == nil {
		t.Fatal("expected positional argument to be rejected")
	}
}
