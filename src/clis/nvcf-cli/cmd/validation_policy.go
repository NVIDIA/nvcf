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
	"fmt"
	"strings"

	"nvcf-cli/internal/client"
)

// Flags shared by task create, deploy create, and cluster register for the
// Helm validation policy. The CLI only builds the request body; the valid
// policy names and the Helm-only rule are enforced server-side.
const (
	flagValidationPolicy    = "validation-policy"
	flagValidationExtraType = "validation-extra-type"

	// defaultValidationPolicyName is applied when a caller supplies extra
	// types but no policy name.
	defaultValidationPolicyName = "Default"
)

// parseWorkloadExtraType parses a workload-side --validation-extra-type entry
// of the form group/version/kind. The workload side does not carry a resource.
func parseWorkloadExtraType(raw string) (client.KubernetesType, error) {
	parts, err := splitExtraType(raw, 3)
	if err != nil {
		return client.KubernetesType{}, err
	}
	return client.KubernetesType{Group: parts[0], Version: parts[1], Kind: parts[2]}, nil
}

// parseClusterExtraType parses a cluster-side --validation-extra-type entry of
// the form group/version/kind/resource. The extra resource (plural) name is
// what the operator needs to grant RBAC.
func parseClusterExtraType(raw string) (client.KubernetesType, error) {
	parts, err := splitExtraType(raw, 4)
	if err != nil {
		return client.KubernetesType{}, err
	}
	return client.KubernetesType{Group: parts[0], Version: parts[1], Kind: parts[2], Resource: parts[3]}, nil
}

// splitExtraType splits raw on "/" and verifies it has exactly want non-empty
// parts. It only checks shape; it does not validate the values themselves.
func splitExtraType(raw string, want int) ([]string, error) {
	shape := "group/version/kind"
	if want == 4 {
		shape = "group/version/kind/resource"
	}
	parts := strings.Split(raw, "/")
	if len(parts) != want {
		return nil, fmt.Errorf("invalid --%s %q: expected %s (%d parts, got %d)", flagValidationExtraType, raw, shape, want, len(parts))
	}
	for i, p := range parts {
		if strings.TrimSpace(p) == "" {
			return nil, fmt.Errorf("invalid --%s %q: %s part %d is empty", flagValidationExtraType, raw, shape, i+1)
		}
	}
	return parts, nil
}

// buildWorkloadValidationPolicy applies the policy name and extra-type flags
// over an existing policy (which may have come from --input-file) following
// the CLI precedence rules: the name flag overrides the file name, extra-type
// flags replace the file list, and a missing name defaults to Default.
//
// nameChanged and typesChanged report whether each flag was set on the command
// line. When neither is set, a non-nil existing policy is still normalized so an
// unnamed input-file policy receives the documented Default name; a nil existing
// policy is returned as nil.
func buildWorkloadValidationPolicy(existing *client.HelmValidationPolicyDto, name string, nameChanged bool, extraTypes []string, typesChanged bool) (*client.HelmValidationPolicyDto, error) {
	if !nameChanged && !typesChanged {
		if existing != nil && existing.Name == "" {
			existing.Name = defaultValidationPolicyName
		}
		return existing, nil
	}
	policy := existing
	if policy == nil {
		policy = &client.HelmValidationPolicyDto{}
	}
	if nameChanged {
		policy.Name = name
	}
	if typesChanged {
		types := make([]client.KubernetesType, 0, len(extraTypes))
		for _, raw := range extraTypes {
			kt, err := parseWorkloadExtraType(raw)
			if err != nil {
				return nil, err
			}
			types = append(types, kt)
		}
		policy.ExtraKubernetesTypes = types
	}
	if policy.Name == "" {
		policy.Name = defaultValidationPolicyName
	}
	return policy, nil
}
