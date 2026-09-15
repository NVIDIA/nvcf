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
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHTTPStatusError(t *testing.T) {
	tests := []struct {
		name        string
		code        int
		wrappedErr  error
		expectedMsg string
	}{
		{
			name:        "nil wrapped error",
			code:        http.StatusNotFound,
			wrappedErr:  nil,
			expectedMsg: "HTTP 404: Not Found",
		},
		{
			name:        "with wrapped error",
			code:        http.StatusInternalServerError,
			wrappedErr:  errors.New("internal error"),
			expectedMsg: "HTTP 500: internal error",
		},
		{
			name:        "custom status code",
			code:        418, // I'm a teapot
			wrappedErr:  nil,
			expectedMsg: "HTTP 418: I'm a teapot",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := HTTPStatusError(tt.code, tt.wrappedErr)

			// Test error message
			assert.Equal(t, tt.expectedMsg, err.Error())

			// Test Code() method
			httpErr := err.(*httpStatusError)
			assert.Equal(t, tt.code, httpErr.Code())

			// Test error unwrapping
			if tt.wrappedErr != nil {
				assert.Equal(t, tt.wrappedErr, errors.Unwrap(err))
			}
		})
	}
}

func TestIsHTTPStatus(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "direct HTTP status error",
			err:      HTTPStatusError(http.StatusBadRequest, nil),
			expected: true,
		},
		{
			name:     "wrapped HTTP status error",
			err:      fmt.Errorf("wrapped: %w", HTTPStatusError(http.StatusBadRequest, nil)),
			expected: true,
		},
		{
			name:     "regular error",
			err:      errors.New("some error"),
			expected: false,
		},
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, IsHTTPStatus(tt.err))
		})
	}
}

func TestGetHTTPStatusCode(t *testing.T) {
	tests := []struct {
		name         string
		err          error
		expectedCode int
	}{
		{
			name:         "direct HTTP status error",
			err:          HTTPStatusError(http.StatusBadRequest, nil),
			expectedCode: http.StatusBadRequest,
		},
		{
			name:         "wrapped HTTP status error",
			err:          fmt.Errorf("wrapped: %w", HTTPStatusError(http.StatusUnauthorized, nil)),
			expectedCode: http.StatusUnauthorized,
		},
		{
			name:         "regular error",
			err:          errors.New("some error"),
			expectedCode: 0,
		},
		{
			name:         "nil error",
			err:          nil,
			expectedCode: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expectedCode, GetHTTPStatusCode(tt.err))
		})
	}
}

func TestHTTPStatusErrorIs(t *testing.T) {
	err1 := HTTPStatusError(http.StatusBadRequest, nil)
	err2 := HTTPStatusError(http.StatusNotFound, nil)

	// Test Is relationship with same type
	assert.True(t, errors.Is(err1, err2))
	assert.True(t, errors.Is(err2, err1))

	// Test Is relationship with wrapped errors
	wrappedErr := fmt.Errorf("wrapped: %w", err1)
	assert.True(t, errors.Is(wrappedErr, err2))
}

func TestHTTPStatusErrorChaining(t *testing.T) {
	baseErr := errors.New("base error")
	httpErr := HTTPStatusError(http.StatusBadRequest, baseErr)
	wrappedErr := fmt.Errorf("wrapped: %w", httpErr)

	// Test error chain unwrapping
	assert.True(t, IsHTTPStatus(wrappedErr))
	assert.Equal(t, http.StatusBadRequest, GetHTTPStatusCode(wrappedErr))
	assert.Equal(t, baseErr, errors.Unwrap(httpErr))
}
