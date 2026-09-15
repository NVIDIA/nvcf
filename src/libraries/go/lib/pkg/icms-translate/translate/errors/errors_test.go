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

package errors

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLaunchArtifactError_Error(t *testing.T) {
	tests := []struct {
		name string
		err  *LaunchArtifactError
		want string
	}{
		{
			name: "unknown reason",
			err: &LaunchArtifactError{
				Message: "test message",
				Reason:  LaunchArtifactErrorReasonUnknown,
			},
			want: "test message",
		},
		{
			name: "types do not exist reason",
			err: &LaunchArtifactError{
				Message: "test message",
				Reason:  LaunchArtifactErrorReasonTypesDoNotExist,
			},
			want: "test message",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.err.Error())
		})
	}
}

func TestReasonForError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want LaunchArtifactErrorReason
	}{
		{
			name: "launch artifact error with unknown reason",
			err: &LaunchArtifactError{
				Reason: LaunchArtifactErrorReasonUnknown,
			},
			want: LaunchArtifactErrorReasonUnknown,
		},
		{
			name: "launch artifact error with types do not exist reason",
			err: &LaunchArtifactError{
				Reason: LaunchArtifactErrorReasonTypesDoNotExist,
			},
			want: LaunchArtifactErrorReasonTypesDoNotExist,
		},
		{
			name: "non-launch artifact error",
			err:  fmt.Errorf("test error"),
			want: LaunchArtifactErrorReasonUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ReasonForError(tt.err))
		})
	}
}

func TestIsTypesDoNotExist(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "launch artifact error with unknown reason",
			err: &LaunchArtifactError{
				Reason: LaunchArtifactErrorReasonUnknown,
			},
			want: false,
		},
		{
			name: "launch artifact error with types do not exist reason",
			err: &LaunchArtifactError{
				Reason: LaunchArtifactErrorReasonTypesDoNotExist,
			},
			want: true,
		},
		{
			name: "non-launch artifact error",
			err:  fmt.Errorf("test error"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsTypesDoNotExist(tt.err))
		})
	}
}

func TestNewTypesDoNotExist(t *testing.T) {
	tests := []struct {
		name  string
		types []string
		want  *LaunchArtifactError
	}{
		{
			name:  "single type",
			types: []string{"type1"},
			want: &LaunchArtifactError{
				Message: "unknown function launch artifact type: [type1]",
				Reason:  LaunchArtifactErrorReasonTypesDoNotExist,
			},
		},
		{
			name:  "multiple types",
			types: []string{"type1", "type2"},
			want: &LaunchArtifactError{
				Message: "unknown function launch artifact type: [type1 type2]",
				Reason:  LaunchArtifactErrorReasonTypesDoNotExist,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, NewTypesDoNotExist(tt.types...))
		})
	}
}
