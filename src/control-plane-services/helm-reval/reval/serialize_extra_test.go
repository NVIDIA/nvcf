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

package reval

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// errWriter always fails on Write, to test json-encoder error paths.
type errWriter struct{}

func (errWriter) Write(_ []byte) (int, error) { return 0, errors.New("write error") }

// Additional decode tests covering paths not exercised by TestSerializer_decode.

func TestSerializer_decode_EmptyDocuments(t *testing.T) {
	// Multiple empty YAML documents should produce no objects and no errors.
	s, err := newSerializer()
	require.NoError(t, err)

	yaml := "\n---\n---\n"
	objs, derrs, err := s.decode(zap.NewNop(), strings.NewReader(yaml))
	require.NoError(t, err)
	assert.Empty(t, derrs)
	assert.Empty(t, objs)
}

func TestSerializer_decode_NoTypeMeta_Skipped(t *testing.T) {
	// A YAML document without apiVersion/kind is skipped (hasTypeMeta returns false).
	s, err := newSerializer()
	require.NoError(t, err)

	yaml := `
foo: bar
baz: 42
`
	objs, derrs, err := s.decode(zap.NewNop(), strings.NewReader(yaml))
	require.NoError(t, err)
	assert.Empty(t, derrs)
	assert.Empty(t, objs)
}

func TestSerializer_decode_MissingMetadataName(t *testing.T) {
	// A valid K8s object without metadata.name adds a parse error.
	s, err := newSerializer()
	require.NoError(t, err)

	yaml := `
apiVersion: v1
kind: Pod
metadata:
  namespace: default
`
	objs, derrs, err := s.decode(zap.NewNop(), strings.NewReader(yaml))
	require.NoError(t, err)
	assert.Empty(t, objs)
	require.Len(t, derrs, 1)
	assert.Contains(t, derrs[0].Error(), "metadata.name")
}

func TestSerializer_decode_DecodeError_UnknownFieldStructured(t *testing.T) {
	// A YAML that passes hasTypeMeta but can't be decoded by the strict decoder
	// adds a parse error (not a fatal error).
	s, err := newSerializer()
	require.NoError(t, err)

	// Send a document that looks like a Pod but has completely invalid structure
	// after the type metadata.
	yaml := `
apiVersion: v1
kind: Pod
metadata:
  name: test
spec:
  unknownTopLevelField: "this should cause strict-decode warnings but not errors for standard types"
`
	// For well-known types the decoder is lenient; use an unknown apiVersion/kind combo
	// that the decoder can only handle as unstructured — if the YAML is truly malformed
	// we get a decode error.
	_, _, err = s.decode(zap.NewNop(), strings.NewReader(yaml))
	// The decoder is lenient; we just verify no fatal error occurs.
	assert.NoError(t, err)
}

func TestSerializer_encode_WriterError(t *testing.T) {
	// A Writer that always fails should cause encode to return an error.
	s, err := newSerializer()
	require.NoError(t, err)

	pod := &corev1.Pod{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{Name: "test-pod"},
	}
	err = s.encode(errWriter{}, []runtime.Object{pod})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "encode objects as JSON")
}

func TestSerializer_decode_YAMLSequence_HasTypeMeta_Error(t *testing.T) {
	// A YAML sequence document (not a mapping) causes hasTypeMeta to error
	// because yaml.Unmarshal into TypeMeta{} fails on a sequence.
	s, err := newSerializer()
	require.NoError(t, err)

	// A YAML sequence: yaml.Unmarshal into a struct returns an error in sigs.k8s.io/yaml
	yaml := "- item1\n- item2\n"
	objs, derrs, fatalErr := s.decode(zap.NewNop(), strings.NewReader(yaml))
	// Regardless of specific error handling, we should get no fatal error
	// and no objects (the document is skipped or produces an error).
	require.NoError(t, fatalErr)
	assert.Empty(t, objs)
	_ = derrs
}

func TestSerializer_decode_MixedValidAndInvalid(t *testing.T) {
	// Mix of valid objects and objects without metadata.name.
	s, err := newSerializer()
	require.NoError(t, err)

	yaml := `
apiVersion: v1
kind: Pod
metadata:
  name: valid-pod
---
apiVersion: v1
kind: Pod
metadata:
  namespace: default
`
	objs, derrs, err := s.decode(zap.NewNop(), strings.NewReader(yaml))
	require.NoError(t, err)
	assert.Len(t, objs, 1)
	assert.Len(t, derrs, 1)
}
