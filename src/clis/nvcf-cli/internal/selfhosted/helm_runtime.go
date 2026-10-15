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
	"context"
	"fmt"

	"github.com/Masterminds/semver/v3"

	"nvcf-cli/internal/selfhosted/helmruntime"
)

const (
	HelmRuntimeHelm3Legacy HelmRuntimeMode = helmruntime.Helm3Legacy
	HelmRuntimeHelm4Compat HelmRuntimeMode = helmruntime.Helm4Compat
)

type HelmRuntimeMode = helmruntime.Mode

func SelectHelmRuntimeMode(helmVersion, helmfileVersion *semver.Version) (HelmRuntimeMode, error) {
	return helmruntime.SelectMode(helmVersion, helmfileVersion)
}

func IsKnownHelmRuntimeMode(mode HelmRuntimeMode) bool {
	return helmruntime.IsKnown(mode)
}

func HelmfileCompatibilityArgs(mode HelmRuntimeMode, target string, verb string) []string {
	return helmruntime.HelmfileCompatibilityArgs(mode, target, verb)
}

func helmRuntimeCompatibilityCheck(tools []BinarySpec) *binaryCheckSpec {
	var helmSpec, helmfileSpec *BinarySpec
	for i := range tools {
		switch tools[i].Name {
		case "helm":
			helmSpec = &tools[i]
		case "helmfile":
			helmfileSpec = &tools[i]
		}
	}
	if helmSpec == nil || helmfileSpec == nil {
		return nil
	}
	return &binaryCheckSpec{
		ID:         "local-host-tools-helm-runtime",
		HumanLabel: "checking Helm and Helmfile compatibility...",
		Run: func(ctx context.Context) CheckResult {
			return checkHelmRuntimeCompatibility(ctx, *helmfileSpec, *helmSpec)
		},
	}
}

func checkHelmRuntimeCompatibility(ctx context.Context, helmfileSpec, helmSpec BinarySpec) CheckResult {
	r := CheckResult{
		ID:       "local-host-tools-helm-runtime",
		Category: "local-host-tools",
		Severity: "error",
		HintURL:  "https://github.com/helmfile/helmfile#installation",
	}
	_, helmfileVersion, err := probeBinarySpecVersion(ctx, helmfileSpec)
	if err != nil {
		r.Message = "helmfile version unavailable for Helm runtime compatibility check: " + err.Error()
		r.Err = err
		return r
	}
	_, helmVersion, err := probeBinarySpecVersion(ctx, helmSpec)
	if err != nil {
		r.Message = "helm version unavailable for Helm runtime compatibility check: " + err.Error()
		r.Err = err
		return r
	}
	mode, err := SelectHelmRuntimeMode(helmVersion, helmfileVersion)
	if err != nil {
		r.Message = err.Error()
		r.Err = err
		return r
	}
	r.Passed = true
	r.Severity = "info"
	r.Detail = string(mode)
	r.Message = fmt.Sprintf("Helm %s with helmfile %s uses %s mode", helmVersion, helmfileVersion, mode)
	return r
}

func probeBinarySpecVersion(ctx context.Context, s BinarySpec) (string, *semver.Version, error) {
	path, err := s.LookPath(s.Name)
	if err != nil || path == "" {
		return "", nil, fmt.Errorf("%s not found on PATH", s.Name)
	}
	var version *semver.Version
	var probeErr error
	for attempt := 1; attempt <= versionProbeAttempts; attempt++ {
		version, probeErr = s.Version(ctx, path)
		if probeErr == nil {
			return path, version, nil
		}
		if ctx.Err() != nil {
			break
		}
	}
	return path, nil, probeErr
}
