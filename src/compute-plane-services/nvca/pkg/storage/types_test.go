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

package storage

import (
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	netv1 "k8s.io/api/networking/v1"
)

func TestHasStorageAnnotation(t *testing.T) {
	tests := []struct {
		name        string
		annotations map[string]string
		want        bool
	}{
		{
			name:        "no annotations",
			annotations: map[string]string{},
			want:        false,
		},
		{
			name: "non-storage annotation",
			annotations: map[string]string{
				"foo": "bar",
			},
			want: false,
		},
		{
			name: "storage annotation",
			annotations: map[string]string{
				WebhookModelCachePVCNameAnnotationKey: "some-value",
			},
			want: true,
		},
		{
			name: "multiple storage annotations",
			annotations: map[string]string{
				WebhookModelCachePVCNameAnnotationKey:                        "some-value",
				HelmWebhookSharedStorageSecretsReadWritePVCNameAnnotationKey: "another-value",
			},
			want: true,
		},
		{
			name: "HelmWebhookSharedStorageSecretsReadWritePVCNameAnnotationKey storage annotation",
			annotations: map[string]string{
				HelmWebhookSharedStorageSecretsReadWritePVCNameAnnotationKey: "some-value",
			},
			want: true,
		},
		{
			name: "HelmWebhookSharedStorageSecretsReadOnlyPVCNameAnnotationKey storage annotation",
			annotations: map[string]string{
				HelmWebhookSharedStorageSecretsReadOnlyPVCNameAnnotationKey: "some-value",
			},
			want: true,
		},
		{
			name: "HelmWebhookSharedStorageKNSReadWritePVCNameAnnotationKey storage annotation",
			annotations: map[string]string{
				HelmWebhookSharedStorageKNSReadWritePVCNameAnnotationKey: "some-value",
			},
			want: true,
		},
		{
			name: "HelmWebhookSharedStorageKNSReadOnlyPVCNameAnnotationKey storage annotation",
			annotations: map[string]string{
				HelmWebhookSharedStorageKNSReadOnlyPVCNameAnnotationKey: "some-value",
			},
			want: true,
		},
		{
			name: "HelmWebhookInternalPersistentStorageStorageClassNameAnnotationKey storage annotation",
			annotations: map[string]string{
				HelmWebhookInternalPersistentStorageStorageClassNameAnnotationKey: "some-value",
			},
			want: true,
		},
		{
			name: "non-storage annotation with similar prefix",
			annotations: map[string]string{
				"nvca.nvcf.nvidia.io/some-other-annotation": "some-value",
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HasStorageAnnotation(tt.annotations)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestISharedStoragePVC(t *testing.T) {

	tests := []struct {
		input    string
		expected bool
	}{
		{SharedStorageSecretsReadOnlyPVCName, true},
		{SharedStorageSecretsReadWritePVCName, true},
		{SharedStorageKNSReadOnlyPVCName, true},
		{SharedStorageKNSReadWritePVCName, true},
		{"not-a-match", false},
	}

	for _, test := range tests {
		t.Run(fmt.Sprintf("input=%s", test.input), func(t *testing.T) {
			assert.Equal(t, test.expected, IsSharedStoragePVC(test.input))
		})
	}
}

func TestIsSharedStorageVolumeName(t *testing.T) {

	tests := []struct {
		input    string
		expected bool
	}{
		{SharedStorageVolumeKNSTokenVolumeName, true},
		{SharedStorageSecretsVolumeName, true},
		{"not-a-match", false},
	}

	for _, test := range tests {
		t.Run(fmt.Sprintf("input=%s", test.input), func(t *testing.T) {
			assert.Equal(t, test.expected, IsSharedStorageVolumeName(test.input))
		})
	}
}

func TestIsSharedStorageVolumeMountPath(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{SharedStorageVolumeSecretsMountPath, true},
		{SharedStorageVolumeKNSTokenMountPath, true},
		{"not-a-match", false},
	}

	for _, test := range tests {
		t.Run(fmt.Sprintf("input=%s", test.input), func(t *testing.T) {
			assert.Equal(t, test.expected, IsSharedStorageVolumeMountPath(test.input))
		})
	}
}

func TestGetIngressNetworkPolicy(t *testing.T) {
	netPols := GetIngressNetworkPolicies()

	require.NotNil(t, netPols[0])
	assert.Equal(t, "allow-ingress-sharedstorage", netPols[0].Name)
	assert.Equal(t, []netv1.PolicyType{netv1.PolicyTypeIngress}, netPols[0].Spec.PolicyTypes)
	assert.Equal(t, 445, netPols[0].Spec.Ingress[0].Ports[0].Port.IntValue())
	tcpProtocol := corev1.ProtocolTCP
	assert.Equal(t, &tcpProtocol, netPols[0].Spec.Ingress[0].Ports[0].Protocol)

	require.NotNil(t, netPols[1])
	assert.Equal(t, "allow-ingress-sharedstorage-monitoring", netPols[1].Name)
	assert.Equal(t, []netv1.PolicyType{netv1.PolicyTypeIngress}, netPols[1].Spec.PolicyTypes)
	assert.Equal(t, 9922, netPols[1].Spec.Ingress[0].Ports[0].Port.IntValue())
	assert.Equal(t, &tcpProtocol, netPols[1].Spec.Ingress[0].Ports[0].Protocol)
}
