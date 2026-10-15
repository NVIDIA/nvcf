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

package controlplaneprofile

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateAcceptsCompleteControlPlaneProfile(t *testing.T) {
	result, err := ParseAndValidate([]byte(validControlPlaneProfileYAML()), ValidateOptions{Require: RequireBoth})
	require.NoError(t, err)

	assert.True(t, result.InClusterUsable)
	assert.True(t, result.ComputeReachableUsable)
	assert.Contains(t, result.Summary(), "in-cluster: usable")
	assert.Contains(t, result.Summary(), "compute-reachable: usable")
}

func TestValidateRequireComputeReachableReportsMissingNATSURL(t *testing.T) {
	doc := strings.Replace(validControlPlaneProfileYAML(), "      natsURL: tls://nats.nvcf-cp.internal:4222\n", "", 1)

	_, err := ParseAndValidate([]byte(doc), ValidateOptions{Require: RequireComputeReachable})
	require.Error(t, err)

	assert.Contains(t, err.Error(), "controlPlane.endpoints.computeReachable.natsURL")
	assert.Contains(t, err.Error(), "required")
}

func TestValidateRequireAnyReportsMissingEndpointScopes(t *testing.T) {
	doc := strings.Replace(validControlPlaneProfileYAML(), `  endpoints:
    inCluster:
      icmsURL: http://api.sis.svc.cluster.local:8080
      revalURL: http://reval.nvcf.svc.cluster.local:8080
      natsURL: nats://nats.nats-system.svc.cluster.local:4222

    computeReachable:
      icmsURL: https://sis.nvcf-cp.internal
      revalURL: https://reval.nvcf-cp.internal
      natsURL: tls://nats.nvcf-cp.internal:4222
`, "  endpoints: {}\n", 1)

	_, err := ParseAndValidate([]byte(doc), ValidateOptions{})
	require.Error(t, err)

	assert.Contains(t, err.Error(), "controlPlane.endpoints")
	assert.Contains(t, err.Error(), "at least one endpoint scope is required")
}

func TestValidateNoDNSGatewayAddressRequiresHostHeader(t *testing.T) {
	doc := strings.Replace(validControlPlaneProfileYAML(), "      icmsURL: https://sis.nvcf-cp.internal", "      icmsURL: http://10.0.0.10", 1)
	doc = strings.Replace(doc, "    sis: sis.nvcf-cp.internal\n", "", 1)

	_, err := ParseAndValidate([]byte(doc), ValidateOptions{Require: RequireComputeReachable})
	require.Error(t, err)

	assert.Contains(t, err.Error(), "controlPlane.hosts.sis")
	assert.Contains(t, err.Error(), "Host header")
}

func TestValidateEndpointScopeReportsUnusableWhenPresentURLInvalid(t *testing.T) {
	v := validator{}

	usable := v.validateEndpointScope("controlPlane.endpoints.computeReachable", EndpointScope{
		ICMSURL:  "ftp://sis.nvcf-cp.internal",
		ReValURL: "https://reval.nvcf-cp.internal",
		NATSURL:  "tls://nats.nvcf-cp.internal:4222",
	}, false)

	assert.False(t, usable)
	require.Len(t, v.problems, 1)
	assert.Contains(t, v.problems[0], "controlPlane.endpoints.computeReachable.icmsURL")
}

func TestValidateRejectsUnsupportedGatewayGRPCURLScheme(t *testing.T) {
	doc := strings.Replace(validControlPlaneProfileYAML(), "    grpcURL: api.nvcf-cp.internal:10081", "    grpcURL: ftp://api.nvcf-cp.internal:10081", 1)

	_, err := ParseAndValidate([]byte(doc), ValidateOptions{Require: RequireBoth})
	require.Error(t, err)

	assert.Contains(t, err.Error(), "controlPlane.gateway.grpcURL")
	assert.Contains(t, err.Error(), "scheme must be grpc, grpcs, or https")
}

func validControlPlaneProfileYAML() string {
	return `apiVersion: nvcf.nvidia.com/v1alpha1
kind: ControlPlaneProfile

controlPlane:
  clusterName: nvcf-cp-euw1
  ncaID: nvcf-default
  region: eu-west-1

  endpoints:
    inCluster:
      icmsURL: http://api.sis.svc.cluster.local:8080
      revalURL: http://reval.nvcf.svc.cluster.local:8080
      natsURL: nats://nats.nats-system.svc.cluster.local:4222

    computeReachable:
      icmsURL: https://sis.nvcf-cp.internal
      revalURL: https://reval.nvcf-cp.internal
      natsURL: tls://nats.nvcf-cp.internal:4222

  gateway:
    httpURL: https://api.nvcf-cp.internal
    grpcURL: api.nvcf-cp.internal:10081

  hosts:
    api: api.nvcf-cp.internal
    apiKeys: api-keys.nvcf-cp.internal
    sis: sis.nvcf-cp.internal
    reval: reval.nvcf-cp.internal
    nats: nats.nvcf-cp.internal
    invocation: invocation.nvcf-cp.internal

  addons:
    llm:
      requestRouterAddress: llm-request-router.nvcf-cp.internal:50071
`
}
