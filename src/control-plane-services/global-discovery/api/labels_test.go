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

package api

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The values are a wire contract with the Rust crate, Helm charts and other
// clusters; a change here is a breaking change.
func TestWellKnownKeys(t *testing.T) {
	want := map[string]string{
		LabelExport:                 "gds.nvidia.com/export",
		LabelRole:                   "gds.nvidia.com/role",
		LabelPool:                   "gds.nvidia.com/pool",
		AnnotationPoolModels:        "gds.nvidia.com/pool-models",
		AnnotationHardwareClass:     "gds.nvidia.com/hardware-class",
		AnnotationDC:                "gds.nvidia.com/dc",
		AnnotationDialScheme:        "gds.nvidia.com/dial-scheme",
		AnnotationGRPCPortName:      "gds.nvidia.com/grpc-port-name",
		AnnotationHTTPPortName:      "gds.nvidia.com/http-port-name",
		AnnotationQUICPortName:      "gds.nvidia.com/quic-port-name",
		AnnotationRoles:             "gds.nvidia.com/roles",
		LabelManagedBy:              "gds.nvidia.com/managed-by",
		LabelClusterSet:             "gds.nvidia.com/clusterset",
		LabelPoolID:                 "gds.nvidia.com/pool-id",
		FinalizerServiceExport:      "gds.nvidia.com/service-export",
		LabelEndpointSliceManagedBy: "endpointslice.kubernetes.io/managed-by",
		LabelKubernetesServiceName:  "kubernetes.io/service-name",
		LabelMCSServiceName:         "multicluster.kubernetes.io/service-name",
		LabelMCSSourceCluster:       "multicluster.kubernetes.io/source-cluster",
		AnnotationMCSDerivedService: "multicluster.kubernetes.io/derived-service",
	}
	for got, expected := range want {
		assert.Equal(t, expected, got)
	}

	assert.Equal(t, "true", ExportEnabled)
	assert.Equal(t, "router", RoleRouter)
	assert.Equal(t, "pool-frontend", RolePoolFrontend)
	assert.Equal(t, "kv-relay", RoleKVRelay)
	assert.Equal(t, "pylon", RolePylon)
	assert.Equal(t, "quic", DefaultQUICPortName)
	assert.Equal(t, "gds-mcs-controller", ManagedByController)
	assert.Equal(t, "gds-mcs-controller.gds.nvidia.com", EndpointSliceManagedBy)
	assert.Equal(t, "clusterset.k8s.io", ClusterPropertyClusterSet)
	assert.Equal(t, "cluster.clusterset.k8s.io", ClusterPropertyClusterID)
}

// Every key must be a valid Kubernetes qualified name: an optional DNS
// subdomain prefix and a name of at most 63 characters.
func TestKeysAreQualifiedNames(t *testing.T) {
	prefix := regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)
	name := regexp.MustCompile(`^[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$`)
	keys := []string{
		LabelExport, LabelRole, LabelPool, AnnotationPoolModels, AnnotationHardwareClass,
		AnnotationDC, AnnotationDialScheme, AnnotationGRPCPortName, AnnotationHTTPPortName,
		LabelManagedBy, LabelClusterSet, LabelPoolID, FinalizerServiceExport,
		LabelEndpointSliceManagedBy, LabelKubernetesServiceName, LabelMCSServiceName,
		LabelMCSSourceCluster, AnnotationMCSDerivedService,
	}
	for _, k := range keys {
		p, n, ok := cut(k)
		if assert.True(t, ok, k) {
			assert.Regexp(t, prefix, p, k)
			assert.LessOrEqual(t, len(p), 253, k)
			assert.Regexp(t, name, n, k)
			assert.LessOrEqual(t, len(n), 63, k)
		}
	}
	// Label values must be at most 63 characters as well.
	for _, v := range []string{ManagedByController, EndpointSliceManagedBy, RoleRouter, RolePoolFrontend, RoleKVRelay} {
		assert.Regexp(t, name, v)
		assert.LessOrEqual(t, len(v), 63, v)
	}
}

func cut(key string) (prefix, name string, ok bool) {
	i := strings.LastIndexByte(key, '/')
	if i < 0 {
		return "", key, false
	}
	return key[:i], key[i+1:], true
}

func TestIsExported(t *testing.T) {
	assert.True(t, IsExported(map[string]string{LabelExport: "true"}))
	assert.False(t, IsExported(map[string]string{LabelExport: "True"}))
	assert.False(t, IsExported(map[string]string{LabelExport: "false"}))
	assert.False(t, IsExported(map[string]string{}))
	assert.False(t, IsExported(nil))
}

func TestIsRole(t *testing.T) {
	for _, r := range []string{RoleRouter, RolePoolFrontend, RoleKVRelay, RolePylon} {
		assert.True(t, IsRole(r), r)
	}
	for _, r := range []string{"", "Router", "frontend", "kv_relay"} {
		assert.False(t, IsRole(r), r)
	}
	for _, r := range []string{RolePoolFrontend, RoleKVRelay, RolePylon} {
		assert.True(t, IsPoolRole(r), r)
	}
	assert.False(t, IsPoolRole(RoleRouter))
	assert.False(t, IsPoolRole(""))
}

func TestParseRoles(t *testing.T) {
	assert.Equal(t, []string{RolePylon}, ParseRoles(map[string]string{LabelRole: RolePylon}, nil))
	assert.Empty(t, ParseRoles(map[string]string{LabelRole: RoleRouter}, nil))
	assert.Empty(t, ParseRoles(nil, nil))
	// The annotation replaces the label, drops router, unknowns and repeats.
	assert.Equal(t, []string{RolePoolFrontend, RolePylon}, ParseRoles(
		map[string]string{LabelRole: RoleKVRelay},
		map[string]string{AnnotationRoles: " pool-frontend, pylon ,router,bogus,pylon"}))
	// A blank annotation falls back to the label.
	assert.Equal(t, []string{RoleKVRelay}, ParseRoles(
		map[string]string{LabelRole: RoleKVRelay},
		map[string]string{AnnotationRoles: "  "}))
}

func TestIsGDSKey(t *testing.T) {
	assert.True(t, IsGDSKey(LabelRole))
	assert.True(t, IsGDSKey("gds.nvidia.com/anything"))
	assert.False(t, IsGDSKey("gds.nvidia.com/"))
	assert.False(t, IsGDSKey("app.kubernetes.io/name"))
	assert.False(t, IsGDSKey("x.gds.nvidia.com/role"))
}

func TestParsePoolModels(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{" , ,", nil},
		{"llama-3", []string{"llama-3"}},
		{"a, b ,c", []string{"a", "b", "c"}},
		{"a,b,a,,c,b", []string{"a", "b", "c"}},
		{"meta/llama-3.1-8b-instruct,deepseek-ai/DeepSeek-V4", []string{"meta/llama-3.1-8b-instruct", "deepseek-ai/DeepSeek-V4"}},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, ParsePoolModels(tt.in), tt.in)
	}
}

func TestPortNames(t *testing.T) {
	g, h := PortNames(nil)
	assert.Equal(t, "grpc", g)
	assert.Equal(t, "http", h)

	g, h = PortNames(map[string]string{AnnotationGRPCPortName: "control", AnnotationHTTPPortName: " web "})
	assert.Equal(t, "control", g)
	assert.Equal(t, "web", h)

	g, h = PortNames(map[string]string{AnnotationGRPCPortName: " "})
	assert.Equal(t, "grpc", g)
	assert.Equal(t, "http", h)

	assert.Equal(t, "quic", QUICPortName(nil))
	assert.Equal(t, "tunnel", QUICPortName(map[string]string{AnnotationQUICPortName: " tunnel "}))
}
