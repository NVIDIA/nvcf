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

package nvca

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sapitypes "k8s.io/apimachinery/pkg/types"

	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

type mockNamespacePatcher struct {
	mock.Mock
}

func (m *mockNamespacePatcher) Patch(
	ctx context.Context,
	name string,
	pt k8sapitypes.PatchType,
	data []byte,
	opts metav1.PatchOptions,
	subresources ...string) (result *corev1.Namespace, err error) {
	inArgs := []any{ctx, name, pt, data, opts}
	for _, v := range subresources {
		inArgs = append(inArgs, v)
	}
	args := m.Called(inArgs...)
	arg0 := args.Get(0)
	if arg0 == nil {
		return nil, args.Error(1)
	}
	return arg0.(*corev1.Namespace), nil
}

func TestEnsureGXCacheNamespaceLabels(t *testing.T) {
	tests := []struct {
		name        string
		namespace   string
		patchData   []byte
		expectedErr error
	}{
		{
			name:      "successful patch",
			namespace: "test-namespace",
			patchData: []byte(fmt.Sprintf(`[{"op": "replace", "path": "/metadata/labels/%s", "value": "true"}]`,
				strings.ReplaceAll(nvcatypes.ShaderCacheLabelKey, "/", "~1"))),
			expectedErr: nil,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			nsPatcher := &mockNamespacePatcher{}
			nsPatcher.On("Patch", mock.Anything, test.namespace, k8sapitypes.JSONPatchType, test.patchData, metav1.PatchOptions{}).Return(&corev1.Namespace{}, test.expectedErr)

			err := ensureGXCacheNamespaceLabels(context.Background(), nsPatcher, test.namespace)
			assert.Equal(t, test.expectedErr, err)
		})
	}
}

func TestEnsureGXCacheNamespaceLabels_PatchError(t *testing.T) {
	nsPatcher := &mockNamespacePatcher{}
	nsPatcher.On("Patch", mock.Anything, "test-namespace", k8sapitypes.JSONPatchType, mock.Anything, metav1.PatchOptions{}).Return(nil, fmt.Errorf("patch error"))

	err := ensureGXCacheNamespaceLabels(context.Background(), nsPatcher, "test-namespace")
	assert.NotNil(t, err)
}
