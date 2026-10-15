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

package semconv

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"testing"
)

// timeoutError is a net.Error reporting a timeout, matching what the net package
// returns for an I/O deadline rather than a context deadline.
type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

// notTimeoutError is a net.Error that is not a timeout, to prove the net.Error
// branch keys off Timeout() rather than the interface alone.
type notTimeoutError struct{}

func (notTimeoutError) Error() string   { return "network unreachable" }
func (notTimeoutError) Timeout() bool   { return false }
func (notTimeoutError) Temporary() bool { return false }

// TestClassifyError pins the bounded error.type vocabulary. Every outcome must
// map to one of the four constants or the empty string, because this value
// becomes a metric label: an unmapped error leaking through would add an
// unbounded dimension to every client series.
func TestClassifyError(t *testing.T) {
	var _ net.Error = timeoutError{}
	var _ net.Error = notTimeoutError{}

	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil is omitted", nil, ""},
		{"context deadline", context.DeadlineExceeded, ErrorTypeTimeout},
		{"os deadline", os.ErrDeadlineExceeded, ErrorTypeTimeout},
		{"context canceled", context.Canceled, ErrorTypeCanceled},
		{"connection refused", syscall.ECONNREFUSED, ErrorTypeConnectionRefused},
		{"net.Error reporting a timeout", timeoutError{}, ErrorTypeTimeout},
		{"net.Error that is not a timeout", notTimeoutError{}, ErrorTypeOther},
		{"generic error", errors.New("boom"), ErrorTypeOther},

		// Real transport errors arrive wrapped, for example by url.Error and
		// net.OpError, so every branch must survive wrapping.
		{"wrapped deadline", fmt.Errorf("get %q: %w", "http://x", context.DeadlineExceeded), ErrorTypeTimeout},
		{"wrapped cancel", fmt.Errorf("get %q: %w", "http://x", context.Canceled), ErrorTypeCanceled},
		{"wrapped refusal", fmt.Errorf("dial tcp: %w", syscall.ECONNREFUSED), ErrorTypeConnectionRefused},
		{"wrapped net timeout", fmt.Errorf("dial tcp: %w", timeoutError{}), ErrorTypeTimeout},
		{"doubly wrapped refusal", fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", syscall.ECONNREFUSED)), ErrorTypeConnectionRefused},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyError(tt.err); got != tt.want {
				t.Errorf("ClassifyError(%v) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}

// TestClassifyErrorVocabularyIsBounded guards the label contract itself: any
// error, however exotic, must land in the known set.
func TestClassifyErrorVocabularyIsBounded(t *testing.T) {
	allowed := map[string]struct{}{
		"":                         {},
		ErrorTypeTimeout:           {},
		ErrorTypeCanceled:          {},
		ErrorTypeConnectionRefused: {},
		ErrorTypeOther:             {},
	}
	for _, err := range []error{
		nil,
		errors.New("arbitrary"),
		fmt.Errorf("wrapped: %w", errors.New("arbitrary")),
		syscall.ECONNRESET,
		syscall.EHOSTUNREACH,
		&net.DNSError{Err: "no such host", Name: "nope.invalid", IsNotFound: true},
		&net.AddrError{Err: "bad address", Addr: "::"},
	} {
		got := ClassifyError(err)
		if _, ok := allowed[got]; !ok {
			t.Errorf("ClassifyError(%v) returned %q, which is outside the bounded error.type set", err, got)
		}
	}
}
