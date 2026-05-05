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

package nvcaoperatorerrors

import (
	"errors"
	"fmt"
	"testing"
)

func TestFatalError(t *testing.T) {
	// Test with regular error
	testErr := errors.New("test error")
	fatalErr := FatalError(testErr)

	// Check error message
	expectedMsg := "fatal error: test error"
	if fatalErr.Error() != expectedMsg {
		t.Errorf("FatalError().Error() = %q, want %q", fatalErr.Error(), expectedMsg)
	}

	// Check unwrapping
	unwrappedErr := errors.Unwrap(fatalErr)
	if unwrappedErr != testErr {
		t.Errorf("errors.Unwrap(FatalError()) = %v, want %v", unwrappedErr, testErr)
	}

	// Test with nil error
	nilFatalErr := FatalError(nil)
	expectedNilMsg := "nil fatal error"
	if nilFatalErr.Error() != expectedNilMsg {
		t.Errorf("FatalError(nil).Error() = %q, want %q", nilFatalErr.Error(), expectedNilMsg)
	}

	// Test with FatalError wrapping another FatalError
	nestedFatalErr := FatalError(fatalErr)
	expectedNestedMsg := "fatal error: fatal error: test error"
	if nestedFatalErr.Error() != expectedNestedMsg {
		t.Errorf("FatalError(FatalError()).Error() = %q, want %q", nestedFatalErr.Error(), expectedNestedMsg)
	}
}

func TestIsFatal(t *testing.T) {
	tests := []struct {
		name  string
		err   error
		fatal bool
	}{
		{
			name:  "nil error",
			err:   nil,
			fatal: false,
		},
		{
			name:  "regular error",
			err:   errors.New("regular error"),
			fatal: false,
		},
		{
			name:  "fatal error",
			err:   FatalError(errors.New("fatal")),
			fatal: true,
		},
		{
			name:  "wrapped regular error",
			err:   fmt.Errorf("wrapped: %w", errors.New("inner")),
			fatal: false,
		},
		{
			name:  "wrapped fatal error",
			err:   fmt.Errorf("wrapped: %w", FatalError(errors.New("inner fatal"))),
			fatal: true,
		},
		{
			name:  "double wrapped fatal error",
			err:   fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", FatalError(errors.New("innermost")))),
			fatal: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsFatal(tt.err); got != tt.fatal {
				t.Errorf("IsFatal() = %v, want %v", got, tt.fatal)
			}
		})
	}
}

func TestFatalError_Is(t *testing.T) {
	err1 := FatalError(errors.New("error1"))
	err2 := FatalError(errors.New("error2"))

	// Test that a fatal error considers itself fatal
	if !errors.Is(err1, err1) {
		t.Errorf("errors.Is(err1, err1) should be true")
	}

	// Test that different fatal errors are considered equal for Is() purposes
	if !errors.Is(err1, err2) {
		t.Errorf("errors.Is(err1, err2) should be true")
	}

	// Test with wrapped errors
	wrappedErr := fmt.Errorf("wrapped: %w", err1)
	if !errors.Is(wrappedErr, err2) {
		t.Errorf("errors.Is(wrappedErr, err2) should be true")
	}
}

func TestFatalError_NilUnwrap(t *testing.T) {
	// Create a fatal error with a nil wrapped error
	fe := &fatalError{err: nil}

	// Test unwrap returns nil
	if unwrapped := fe.Unwrap(); unwrapped != nil {
		t.Errorf("fe.Unwrap() = %v, want nil", unwrapped)
	}

	// Test error message for nil error
	expected := "nil fatal error"
	if fe.Error() != expected {
		t.Errorf("fe.Error() = %q, want %q", fe.Error(), expected)
	}
}
