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

package nvcaerrors

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNotExistError(t *testing.T) {
	tests := []struct {
		name     string
		wrapped  error
		expected string
	}{
		{
			name:     "with wrapped error",
			wrapped:  errors.New("test error"),
			expected: "not exist error: test error",
		},
		{
			name:     "with nil wrapped error",
			wrapped:  nil,
			expected: "nil not exist error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NotExistError(tt.wrapped)
			assert.Equal(t, tt.expected, err.Error())
		})
	}
}

func TestIsNotExist(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "with not exist error",
			err:      NotExistError(errors.New("test error")),
			expected: true,
		},
		{
			name:     "with not exist error and nil wrapped error",
			err:      NotExistError(nil),
			expected: true,
		},
		{
			name:     "with other error",
			err:      errors.New("test error"),
			expected: false,
		},
		{
			name:     "with nil error",
			err:      nil,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, IsNotExist(tt.err))
		})
	}
}

func TestNotExistErrorUnwrap(t *testing.T) {
	tests := []struct {
		name     string
		wrapped  error
		expected error
	}{
		{
			name:     "with wrapped error",
			wrapped:  errors.New("test error"),
			expected: errors.New("test error"),
		},
		{
			name:     "with nil wrapped error",
			wrapped:  nil,
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nee := &notExistError{err: tt.wrapped}
			assert.Equal(t, tt.expected, nee.Unwrap())
		})
	}
}

func TestNotExistErrorIs(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "with not exist error",
			err:      NotExistError(errors.New("test error")),
			expected: true,
		},
		{
			name:     "with not exist error and nil wrapped error",
			err:      NotExistError(nil),
			expected: true,
		},
		{
			name:     "with other error",
			err:      errors.New("test error"),
			expected: false,
		},
		{
			name:     "with nil error",
			err:      nil,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nee := &notExistError{err: tt.err}
			assert.Equal(t, tt.expected, nee.Is(tt.err))
		})
	}
}
