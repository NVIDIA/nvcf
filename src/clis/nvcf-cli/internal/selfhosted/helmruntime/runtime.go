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

package helmruntime

import (
	"fmt"
	"strings"

	"github.com/Masterminds/semver/v3"
)

const (
	Helm3Legacy Mode = "helm3-legacy"
	Helm4Compat Mode = "helm4-compat"
)

var (
	minHelmVersion             = semver.MustParse("3.14.0")
	minHelmfileVersion         = semver.MustParse("1.0.0")
	minHelmfileVersionForHelm4 = semver.MustParse("1.5.0")
)

type Mode string

func SelectMode(helmVersion, helmfileVersion *semver.Version) (Mode, error) {
	if helmVersion == nil {
		return "", fmt.Errorf("helm version is required")
	}
	if helmfileVersion == nil {
		return "", fmt.Errorf("helmfile version is required")
	}
	if helmVersion.LessThan(minHelmVersion) {
		return "", fmt.Errorf("Helm %s is too old; required >= %s", helmVersion, minHelmVersion)
	}
	if helmfileVersion.LessThan(minHelmfileVersion) {
		return "", fmt.Errorf("helmfile %s is too old; required >= %s", helmfileVersion, minHelmfileVersion)
	}
	switch helmVersion.Major() {
	case 3:
		return Helm3Legacy, nil
	case 4:
		if helmfileVersion.LessThan(minHelmfileVersionForHelm4) {
			return "", fmt.Errorf("Helm %s requires helmfile >= %s for the Helm 4 compatibility path; found helmfile %s", helmVersion, minHelmfileVersionForHelm4, helmfileVersion)
		}
		return Helm4Compat, nil
	default:
		return "", fmt.Errorf("Helm %s is not supported; supported major versions are 3 and 4", helmVersion)
	}
}

func IsKnown(mode Mode) bool {
	switch mode {
	case Helm3Legacy, Helm4Compat:
		return true
	default:
		return false
	}
}

func HelmfileCompatibilityArgs(mode Mode, target string, verb string) []string {
	if mode != Helm4Compat {
		return nil
	}
	var args []string
	if strings.HasSuffix(target, "/") {
		args = append(args, "--sequential-helmfiles")
	}
	if verb == "apply" || verb == "sync" {
		args = append(args, "--track-mode", "helm-legacy")
	}
	return args
}
