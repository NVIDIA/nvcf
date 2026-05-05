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

package reval

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestSerializer_decode(t *testing.T) {
	type spec struct {
		name          string
		inYAML        func() string
		expObjs       []runtime.Object
		expDecodeErrs []string
		expErr        string
	}

	for _, tt := range []spec{
		{
			name: "one object",
			inYAML: func() string {
				return `
apiVersion: v1
kind: Pod
metadata:
  name: foo
`
			},
			expObjs: []runtime.Object{
				&corev1.Pod{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "v1", Kind: "Pod",
					},
					ObjectMeta: metav1.ObjectMeta{Name: "foo"},
				},
			},
		},
		{
			name: "multiple object",
			inYAML: func() string {
				return `
apiVersion: v1
kind: Pod
metadata:
  name: foo
---
apiVersion: v1
kind: Pod
metadata:
  name: bar
`
			},
			expObjs: []runtime.Object{
				&corev1.Pod{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "v1", Kind: "Pod",
					},
					ObjectMeta: metav1.ObjectMeta{Name: "foo"},
				},
				&corev1.Pod{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "v1", Kind: "Pod",
					},
					ObjectMeta: metav1.ObjectMeta{Name: "bar"},
				},
			},
		},
		{
			name: "extra core type",
			inYAML: func() string {
				return `
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: foo
`
			},
			expObjs: []runtime.Object{
				&autoscalingv2.HorizontalPodAutoscaler{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "autoscaling/v2", Kind: "HorizontalPodAutoscaler",
					},
					ObjectMeta: metav1.ObjectMeta{Name: "foo"},
				},
			},
		},
		{
			name: "extra external type",
			inYAML: func() string {
				return `
apiVersion: x-foo.mycompany.com/v1
kind: MyKind
metadata:
  name: foo
`
			},
			expObjs: []runtime.Object{
				&unstructured.Unstructured{
					Object: map[string]any{
						"apiVersion": "x-foo.mycompany.com/v1",
						"kind":       "MyKind",
						"metadata": map[string]any{
							"name": "foo",
						},
					},
				},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, err := newSerializer()
			require.NoError(t, err)
			logger := zaptest.NewLogger(t, zaptest.Level(zapcore.PanicLevel))
			gotObjs, gotDecodeErrs, gotErr := s.decode(logger, strings.NewReader(tt.inYAML()))
			if tt.expErr != "" {
				assert.EqualError(t, gotErr, tt.expErr)
			} else if len(tt.expDecodeErrs) != 0 {
				assert.NoError(t, gotErr)
				if assert.Len(t, gotDecodeErrs, len(tt.expDecodeErrs)) {
					for i := range tt.expDecodeErrs {
						assert.EqualError(t, gotDecodeErrs[i], tt.expDecodeErrs[i])
					}
				}
			} else {
				assert.NoError(t, gotErr)
				assert.Empty(t, gotDecodeErrs)
				assert.Equal(t, tt.expObjs, gotObjs)
			}
		})
	}
}

func Test_checkTypes(t *testing.T) {
	type spec struct {
		name      string
		inObjs    []runtime.Object
		extraGVKs []schema.GroupVersionKind
		expErr    string
	}

	for _, tt := range []spec{
		{
			name: "extra core type",
			extraGVKs: []schema.GroupVersionKind{
				{Group: "autoscaling", Version: "v2", Kind: "HorizontalPodAutoscaler"},
			},
			inObjs: []runtime.Object{
				&corev1.Pod{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "v1", Kind: "Pod",
					},
					ObjectMeta: metav1.ObjectMeta{Name: "foo"},
				},
				&autoscalingv2.HorizontalPodAutoscaler{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "autoscaling/v2", Kind: "HorizontalPodAutoscaler",
					},
					ObjectMeta: metav1.ObjectMeta{Name: "foo"},
				},
			},
		},
		{
			name: "extra external type",
			extraGVKs: []schema.GroupVersionKind{
				{Group: "x-foo.mycompany.com", Version: "v1", Kind: "MyKind"},
			},
			inObjs: []runtime.Object{
				&corev1.Pod{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "v1", Kind: "Pod",
					},
					ObjectMeta: metav1.ObjectMeta{Name: "foo"},
				},
				&unstructured.Unstructured{
					Object: map[string]any{
						"apiVersion": "x-foo.mycompany.com/v1",
						"kind":       "MyKind",
						"metadata": map[string]any{
							"name": "foo",
						},
					},
				},
			},
		},
		{
			name: "unsupported core type",
			inObjs: []runtime.Object{
				&corev1.Pod{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "v1", Kind: "Pod",
					},
					ObjectMeta: metav1.ObjectMeta{Name: "foo"},
				},
				&autoscalingv2.HorizontalPodAutoscaler{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "autoscaling/v2", Kind: "HorizontalPodAutoscaler",
					},
					ObjectMeta: metav1.ObjectMeta{Name: "foo"},
				},
			},
			expErr: `unsupported types: ["autoscaling/v2.HorizontalPodAutoscaler"]`,
		},
		{
			name: "unsupported extra type",
			inObjs: []runtime.Object{
				&corev1.Pod{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "v1", Kind: "Pod",
					},
					ObjectMeta: metav1.ObjectMeta{Name: "foo"},
				},
				&unstructured.Unstructured{
					Object: map[string]any{
						"apiVersion": "x-foo.mycompany.com/v1",
						"kind":       "MyKind",
						"metadata": map[string]any{
							"name": "foo",
						},
					},
				},
			},
			expErr: `unsupported types: ["x-foo.mycompany.com/v1.MyKind"]`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			logger := zaptest.NewLogger(t, zaptest.Level(zapcore.PanicLevel))
			gotErr := checkTypes(logger, tt.inObjs, tt.extraGVKs...)
			if tt.expErr != "" {
				assert.EqualError(t, gotErr, tt.expErr)
			} else {
				assert.NoError(t, gotErr)
			}
		})
	}
}
