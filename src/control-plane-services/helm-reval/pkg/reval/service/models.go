/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"

	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/reval"
)

type RevalRequest struct {
	HelmChart                   string                    `json:"helmChart,omitempty"`
	Namespace                   string                    `json:"namespace,omitempty"`
	ReleaseName                 string                    `json:"releaseName,omitempty"`
	HelmChartServicePort        int32                     `json:"helmChartServicePort,omitempty"`
	HelmChartServiceName        string                    `json:"helmChartServiceName,omitempty"`
	HelmChartHTTPHealthEndpoint string                    `json:"helmChartHTTPHealthEndpoint,omitempty"`
	ApiKey                      string                    `json:"apiKey,omitempty"`
	InstanceType                string                    `json:"instanceType,omitempty"`
	Gpu                         string                    `json:"gpu,omitempty"`
	MaxInstances                int32                     `json:"maxInstances,omitempty"`
	MinInstances                int32                     `json:"minInstances,omitempty"`
	Configuration               json.RawMessage           `json:"configuration,omitempty"`
	Clusters                    []string                  `json:"clusters,omitempty"`
	K8SVersion                  string                    `json:"k8sVersion,omitempty"`
	ApiVersions                 []string                  `json:"apiVersions,omitempty"`
	HelmRegistryAuthConfig      common.RegistryAuthConfig `json:"helmRegistryAuthConfig,omitempty"`
	ImageRegistryAuthConfig     common.RegistryAuthConfig `json:"imageRegistryAuthConfig,omitempty"`
	RenderPolicy                *ValidationPolicy         `json:"validationPolicy,omitempty"`
	ValidatePolicies            []ValidationPolicy        `json:"validationPolicies,omitempty"`
}

type ValidationPolicy struct {
	ID                          string                             `json:"id"`
	Name                        reval.PolicyName                   `json:"name"`
	AllowedExtraKubernetesTypes []PolicyAllowedExtraKubernetesType `json:"allowedExtraKubernetesTypes"`
}

type PolicyAllowedExtraKubernetesType struct {
	Group    string `json:"group"`
	Version  string `json:"version"`
	Kind     string `json:"kind"`
	Resource string `json:"resource"`
}

func (vp *ValidationPolicy) validate() (errs []error) {
	switch vp.Name {
	case reval.DefaultPolicy, reval.UnrestrictedPolicy:
	default:
		errs = append(errs, fmt.Errorf("unexpected name %q (expected %q or %q)",
			vp.Name, reval.DefaultPolicy, reval.UnrestrictedPolicy))
	}

	for j, gvk := range vp.AllowedExtraKubernetesTypes {
		if gvk.Group == "" {
			errs = append(errs, fmt.Errorf("allowed extra Kubernetes type %d group is unset", j))
		}
		if gvk.Version == "" {
			errs = append(errs, fmt.Errorf("allowed extra Kubernetes type %d version is unset", j))
		}
		if gvk.Kind == "" {
			errs = append(errs, fmt.Errorf("allowed extra Kubernetes type %d kind is unset", j))
		}
	}
	return errs
}

// Bind to enable github.com/go-chi/render to work with this type
func (r *RevalRequest) Bind(httpReq *http.Request) error {
	if r.HelmChart == "" {
		return fmt.Errorf("helmChart is required")
	}
	if r.ReleaseName == "" {
		r.ReleaseName = "mini-service"
	}
	if r.Namespace == "" {
		r.Namespace = r.ReleaseName
	}

	isRender := strings.HasSuffix(httpReq.URL.Path, "/render")

	var verrs []error
	if isRender {
		r.ValidatePolicies = nil
		// An unset validation policy is allowed.
		if r.RenderPolicy != nil {
			if errs := r.RenderPolicy.validate(); len(errs) != 0 {
				verrs = append(verrs, fmt.Errorf("validation policy: %v", errors.Join(errs...)))
			}
		}
	} else {
		r.RenderPolicy = nil
		for i, vp := range r.ValidatePolicies {
			if vp.ID == "" {
				verrs = append(verrs, fmt.Errorf("validation policy %d is missing ID", i))
			}
			if errs := vp.validate(); len(errs) != 0 {
				verrs = append(verrs, fmt.Errorf("validation policy %s: %v", vp.ID, errors.Join(errs...)))
			}
		}
	}
	if len(verrs) != 0 {
		return errors.Join(verrs...)
	}

	return nil
}

type RevalResult struct {
	Valid              bool                `json:"valid"`
	ValidationErrors   []string            `json:"validationErrors"`
	ValidationPolicies []RevalPolicyResult `json:"validationPolicies,omitempty"`
	Output             json.RawMessage     `json:"output,omitempty"`
}

// RevalPolicyResult mirrors reval.ValidationPolicyResult for the HTTP response.
type RevalPolicyResult struct {
	ID               string   `json:"id"`
	Valid            bool     `json:"valid"`
	ValidationErrors []string `json:"validationErrors"`
}
