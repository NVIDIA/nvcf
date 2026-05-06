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

package nverrors

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNVError_GettersNonNil(t *testing.T) {
	e := &NVError{
		Reason:   "test reason",
		Origin:   "test origin",
		ErrorId:  "error-id-123",
		Metadata: map[string]string{"key": "value"},
	}
	assert.Equal(t, "test reason", e.GetReason())
	assert.Equal(t, "test origin", e.GetOrigin())
	assert.Equal(t, "error-id-123", e.GetErrorId())
	assert.Equal(t, map[string]string{"key": "value"}, e.GetMetadata())
}

func TestNVError_GettersNilReceiver(t *testing.T) {
	var e *NVError
	assert.Equal(t, "", e.GetReason())
	assert.Equal(t, "", e.GetOrigin())
	assert.Equal(t, "", e.GetErrorId())
	assert.Nil(t, e.GetMetadata())
}

func TestNVError_Reset(t *testing.T) {
	e := &NVError{Reason: "before", Origin: "origin", ErrorId: "id", Metadata: map[string]string{"k": "v"}}
	e.Reset()
	assert.Equal(t, "", e.Reason)
	assert.Equal(t, "", e.Origin)
	assert.Equal(t, "", e.ErrorId)
	assert.Nil(t, e.Metadata, "Reset() should zero out the Metadata field")
}

func TestNVError_String(t *testing.T) {
	e := &NVError{Reason: "some reason"}
	s := e.String()
	assert.NotEmpty(t, s)
}

func TestNVError_ProtoMessage(t *testing.T) {
	e := &NVError{}
	e.ProtoMessage()
}

func TestNVError_ProtoReflect(t *testing.T) {
	e := &NVError{Reason: "reason"}
	msg := e.ProtoReflect()
	assert.NotNil(t, msg)
}

func TestNVError_ProtoReflectNilReceiver(t *testing.T) {
	var e *NVError
	msg := e.ProtoReflect()
	assert.NotNil(t, msg)
}

func TestNVError_Descriptor(t *testing.T) {
	b, path := (&NVError{}).Descriptor()
	assert.NotNil(t, b)
	assert.Equal(t, []int{0}, path)
}
