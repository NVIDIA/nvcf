// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package service

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/reval"
)

// ── ValidationPolicy.validate ─────────────────────────────────────────────────

func TestValidationPolicy_validate(t *testing.T) {
	tests := []struct {
		name    string
		policy  ValidationPolicy
		wantErr bool
		errMsgs []string
	}{
		{
			name:    "Default policy, no GVKs",
			policy:  ValidationPolicy{ID: "p1", Name: reval.DefaultPolicy},
			wantErr: false,
		},
		{
			name:    "Unrestricted policy, no GVKs",
			policy:  ValidationPolicy{ID: "p2", Name: reval.UnrestrictedPolicy},
			wantErr: false,
		},
		{
			name:    "unknown policy name",
			policy:  ValidationPolicy{ID: "p3", Name: "invalid"},
			wantErr: true,
			errMsgs: []string{"unexpected name"},
		},
		{
			name: "valid GVK entry",
			policy: ValidationPolicy{
				Name: reval.DefaultPolicy,
				AllowedExtraKubernetesTypes: []PolicyAllowedExtraKubernetesType{
					{Group: "apps", Version: "v1", Kind: "Deployment", Resource: "deployments"},
				},
			},
			wantErr: false,
		},
		{
			name: "GVK missing group",
			policy: ValidationPolicy{
				Name: reval.DefaultPolicy,
				AllowedExtraKubernetesTypes: []PolicyAllowedExtraKubernetesType{
					{Group: "", Version: "v1", Kind: "Foo"},
				},
			},
			wantErr: true,
			errMsgs: []string{"group is unset"},
		},
		{
			name: "GVK missing version",
			policy: ValidationPolicy{
				Name: reval.DefaultPolicy,
				AllowedExtraKubernetesTypes: []PolicyAllowedExtraKubernetesType{
					{Group: "foo.com", Version: "", Kind: "Foo"},
				},
			},
			wantErr: true,
			errMsgs: []string{"version is unset"},
		},
		{
			name: "GVK missing kind",
			policy: ValidationPolicy{
				Name: reval.DefaultPolicy,
				AllowedExtraKubernetesTypes: []PolicyAllowedExtraKubernetesType{
					{Group: "foo.com", Version: "v1", Kind: ""},
				},
			},
			wantErr: true,
			errMsgs: []string{"kind is unset"},
		},
		{
			name: "multiple GVK errors accumulated",
			policy: ValidationPolicy{
				Name: reval.DefaultPolicy,
				AllowedExtraKubernetesTypes: []PolicyAllowedExtraKubernetesType{
					{Group: "", Version: "", Kind: ""},
				},
			},
			wantErr: true,
			errMsgs: []string{"group is unset", "version is unset", "kind is unset"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := tt.policy.validate()
			if tt.wantErr {
				require.NotEmpty(t, errs)
				for _, msg := range tt.errMsgs {
					found := false
					for _, e := range errs {
						if strings.Contains(e.Error(), msg) {
							found = true
							break
						}
					}
					assert.True(t, found, "expected error containing %q, got: %v", msg, errs)
				}
			} else {
				assert.Empty(t, errs)
			}
		})
	}
}

// ── RevalRequest.Bind ─────────────────────────────────────────────────────────

func TestRevalRequest_Bind_Validate(t *testing.T) {
	tests := []struct {
		name        string
		req         RevalRequest
		path        string
		wantErr     bool
		errContains string
		checkFn     func(t *testing.T, r *RevalRequest)
	}{
		{
			name:        "missing helmChart",
			req:         RevalRequest{},
			path:        "/v1/validate",
			wantErr:     true,
			errContains: "helmChart is required",
		},
		{
			name: "defaults releaseName and namespace",
			req:  RevalRequest{HelmChart: "oci://chart"},
			path: "/v1/validate",
			checkFn: func(t *testing.T, r *RevalRequest) {
				assert.Equal(t, "mini-service", r.ReleaseName)
				assert.Equal(t, "mini-service", r.Namespace)
			},
		},
		{
			name: "explicit releaseName propagates to namespace default",
			req:  RevalRequest{HelmChart: "oci://chart", ReleaseName: "myrelease"},
			path: "/v1/validate",
			checkFn: func(t *testing.T, r *RevalRequest) {
				assert.Equal(t, "myrelease", r.ReleaseName)
				assert.Equal(t, "myrelease", r.Namespace)
			},
		},
		{
			name: "explicit namespace is preserved",
			req:  RevalRequest{HelmChart: "oci://chart", ReleaseName: "rel", Namespace: "custom-ns"},
			path: "/v1/validate",
			checkFn: func(t *testing.T, r *RevalRequest) {
				assert.Equal(t, "custom-ns", r.Namespace)
			},
		},
		{
			name: "validate path clears RenderPolicy",
			req: RevalRequest{
				HelmChart:    "oci://chart",
				RenderPolicy: &ValidationPolicy{Name: reval.DefaultPolicy},
			},
			path: "/v1/validate",
			checkFn: func(t *testing.T, r *RevalRequest) {
				assert.Nil(t, r.RenderPolicy)
			},
		},
		{
			name: "validate with missing policy ID is rejected",
			req: RevalRequest{
				HelmChart: "oci://chart",
				ValidatePolicies: []ValidationPolicy{
					{ID: "", Name: reval.DefaultPolicy},
				},
			},
			path:        "/v1/validate",
			wantErr:     true,
			errContains: "missing ID",
		},
		{
			name: "validate with bad policy name is rejected",
			req: RevalRequest{
				HelmChart: "oci://chart",
				ValidatePolicies: []ValidationPolicy{
					{ID: "p1", Name: "badname"},
				},
			},
			path:        "/v1/validate",
			wantErr:     true,
			errContains: "unexpected name",
		},
		{
			name: "valid validate request with policies",
			req: RevalRequest{
				HelmChart: "oci://chart",
				ValidatePolicies: []ValidationPolicy{
					{ID: "p1", Name: reval.DefaultPolicy},
				},
			},
			path: "/v1/validate",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := tt.req
			httpReq := httptest.NewRequest("POST", tt.path, nil)
			err := r.Bind(httpReq)
			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				require.NoError(t, err)
				if tt.checkFn != nil {
					tt.checkFn(t, &r)
				}
			}
		})
	}
}

func TestRevalRequest_Bind_Render(t *testing.T) {
	tests := []struct {
		name        string
		req         RevalRequest
		wantErr     bool
		errContains string
		checkFn     func(t *testing.T, r *RevalRequest)
	}{
		{
			name:        "missing helmChart",
			req:         RevalRequest{},
			wantErr:     true,
			errContains: "helmChart is required",
		},
		{
			name: "render path clears ValidatePolicies",
			req: RevalRequest{
				HelmChart: "oci://chart",
				ValidatePolicies: []ValidationPolicy{
					{ID: "p1", Name: reval.DefaultPolicy},
				},
			},
			checkFn: func(t *testing.T, r *RevalRequest) {
				assert.Nil(t, r.ValidatePolicies)
			},
		},
		{
			name: "render with nil RenderPolicy is accepted",
			req: RevalRequest{
				HelmChart:    "oci://chart",
				RenderPolicy: nil,
			},
		},
		{
			name: "render with valid RenderPolicy is accepted",
			req: RevalRequest{
				HelmChart:    "oci://chart",
				RenderPolicy: &ValidationPolicy{Name: reval.DefaultPolicy},
			},
		},
		{
			name: "render with invalid RenderPolicy name is rejected",
			req: RevalRequest{
				HelmChart:    "oci://chart",
				RenderPolicy: &ValidationPolicy{Name: "garbage"},
			},
			wantErr:     true,
			errContains: "unexpected name",
		},
		{
			name: "render with GVK missing group is rejected",
			req: RevalRequest{
				HelmChart: "oci://chart",
				RenderPolicy: &ValidationPolicy{
					Name: reval.DefaultPolicy,
					AllowedExtraKubernetesTypes: []PolicyAllowedExtraKubernetesType{
						{Group: "", Version: "v1", Kind: "Foo"},
					},
				},
			},
			wantErr:     true,
			errContains: "group is unset",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := tt.req
			httpReq := httptest.NewRequest("POST", "/v1/render", nil)
			err := r.Bind(httpReq)
			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				require.NoError(t, err)
				if tt.checkFn != nil {
					tt.checkFn(t, &r)
				}
			}
		})
	}
}
