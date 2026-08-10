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

package clusterdump

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

func TestResolveGVRs_NilDiscoveryUsesDefaults(t *testing.T) {
	pc := &PlaneCollector{}
	in := []schema.GroupVersionResource{{Group: "resource.k8s.io", Version: "v1alpha3", Resource: "resourceclaims"}}
	assert.Equal(t, in, pc.resolveGVRs(in))
}

func TestResolveGVRs_OverridesWithServedVersion(t *testing.T) {
	kube := k8sfake.NewSimpleClientset()
	kube.Resources = []*metav1.APIResourceList{
		{GroupVersion: "v1", APIResources: []metav1.APIResource{{Name: "pods"}}},
		{GroupVersion: "resource.k8s.io/v1beta1", APIResources: []metav1.APIResource{
			{Name: "resourceclaims"},
			{Name: "resourceclaims/status"}, // subresource, ignored
		}},
	}
	pc := &PlaneCollector{Discovery: kube.Discovery()}

	got := pc.resolveGVRs([]schema.GroupVersionResource{
		{Group: "", Version: "v1", Resource: "pods"},
		{Group: "resource.k8s.io", Version: "v1alpha3", Resource: "resourceclaims"}, // stale default
		{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"},  // not served
	})
	assert.Equal(t, "v1", got[0].Version)
	assert.Equal(t, "v1beta1", got[1].Version, "served version overrides the stale default")
	assert.Equal(t, "v2", got[2].Version, "an unserved kind keeps its default version")
}

func TestCollectResourcesIn_ForbiddenWarns(t *testing.T) {
	cm := unstructuredObj("v1", "ConfigMap", "nvcf", "ok", map[string]interface{}{
		"data": map[string]interface{}{"k": "v"},
	})
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), allCoreListKinds(), cm)
	dc.PrependReactor("list", "secrets", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "", fmt.Errorf("denied"))
	})
	pc := &PlaneCollector{Role: RoleControlPlane, Dynamic: dc, Options: DumpOptions{Redact: RedactSecrets}}

	out, warns := pc.collectResourcesIn(context.Background(), []string{"nvcf"}, []schema.GroupVersionResource{
		{Group: "", Version: "v1", Resource: "configmaps"},
		{Group: "", Version: "v1", Resource: "secrets"},
	})

	// ConfigMap captured; the Forbidden secrets list surfaces a warning rather
	// than being silently skipped.
	require.Len(t, out, 1)
	assert.Equal(t, "configmaps", out[0].Resource)
	require.Len(t, warns, 1)
	assert.Contains(t, warns[0], "secrets")
	assert.Contains(t, warns[0], "RBAC")
}
