// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"flag"
	"fmt"
	"os"
	"regexp"
	"strconv"
)

const (
	maxStandaloneMajor = 3
	maxStandaloneMinor = 0
)

var releaseTagPattern = regexp.MustCompile(`^v([0-9]+)\.([0-9]+)\.([0-9]+)(?:-(rc|dev)\.[0-9]+)?$`)

func main() {
	tag := flag.String("tag", os.Getenv("CI_COMMIT_TAG"), "release tag to validate")
	flag.Parse()

	if err := validateTag(*tag); err != nil {
		fmt.Fprintf(os.Stderr, "invalid standalone NVCA release tag %q: %v\n", *tag, err)
		os.Exit(1)
	}

	fmt.Printf("standalone NVCA release tag %q is allowed\n", *tag)
}

func validateTag(tag string) error {
	matches := releaseTagPattern.FindStringSubmatch(tag)
	if matches == nil {
		return fmt.Errorf("expected vMAJOR.MINOR.PATCH with optional -rc.N or -dev.N suffix")
	}

	major, err := strconv.Atoi(matches[1])
	if err != nil {
		return fmt.Errorf("parse major version: %w", err)
	}

	minor, err := strconv.Atoi(matches[2])
	if err != nil {
		return fmt.Errorf("parse minor version: %w", err)
	}

	if major < maxStandaloneMajor {
		return nil
	}
	if major == maxStandaloneMajor && minor <= maxStandaloneMinor {
		return nil
	}

	return fmt.Errorf("standalone NVCA releases only allow v3.0.x and earlier; move v3.1+ tags to nvcf/nvcf")
}
