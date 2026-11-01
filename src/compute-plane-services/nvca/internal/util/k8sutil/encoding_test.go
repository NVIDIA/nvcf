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

package k8sutil

import (
	"encoding/base64"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
)

func TestGetObjectFromEncodedString(t *testing.T) {
	tests := []struct {
		name        string
		base64Str   string
		objType     reflect.Type
		decoder     runtime.Decoder
		wantErr     bool
		wantObjType reflect.Type
	}{
		{
			name: "valid base64 encoded string",
			base64Str: base64.StdEncoding.EncodeToString([]byte(`apiVersion: v1
kind: Pod
metadata:
  name: test-pod`)),
			objType:     reflect.TypeOf(&corev1.Pod{}),
			wantErr:     false,
			wantObjType: reflect.TypeOf(&corev1.Pod{}),
		},
		{
			name:        "invalid base64 encoded string",
			base64Str:   " invalid base64 string",
			objType:     reflect.TypeOf(&runtime.Unknown{}),
			wantErr:     true,
			wantObjType: nil,
		},
		{
			name: "valid base64 encoded string, wrong object type",
			base64Str: base64.StdEncoding.EncodeToString([]byte(`apiVersion: v1
kind: Pod
metadata:
  name: test-pod`)),
			objType:     reflect.TypeOf(&runtime.Unknown{}),
			wantErr:     true,
			wantObjType: nil,
		},
		{
			name: "custom decoder",
			base64Str: base64.StdEncoding.EncodeToString([]byte(`apiVersion: v1
kind: Pod
metadata:
  name: test-pod`)),
			objType:     reflect.TypeOf(&corev1.Pod{}),
			decoder:     scheme.Codecs.UniversalDeserializer(),
			wantErr:     false,
			wantObjType: reflect.TypeOf(&corev1.Pod{}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj, err := GetObjectFromEncodedString(tt.base64Str, tt.objType, tt.decoder)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetObjectFromEncodedString() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if obj != nil && reflect.TypeOf(obj) != tt.wantObjType {
				t.Errorf("GetObjectFromEncodedString() obj type = %v, want %v", reflect.TypeOf(obj), tt.wantObjType)
			}
		})
	}
}
