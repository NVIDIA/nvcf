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

package shutdown

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"testing"
)

func TestIsSignalError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil is not a shutdown",
			err:  nil,
			want: false,
		},
		{
			name: "typed SIGTERM",
			err:  NewSignalError(syscall.SIGTERM),
			want: true,
		},
		{
			name: "typed SIGINT",
			err:  NewSignalError(syscall.SIGINT),
			want: true,
		},
		{
			name: "wrapped typed error",
			err:  fmt.Errorf("server stopped: %w", NewSignalError(syscall.SIGTERM)),
			want: true,
		},
		{
			// The wording an older pinned lib, or the separate nvcf-go copy of
			// nvkit, still produces. Recognizing it is why the fallback exists.
			name: "untyped SIGTERM wording",
			err:  errors.New("received signal terminated"),
			want: true,
		},
		{
			name: "untyped SIGINT wording",
			err:  errors.New("received signal interrupt"),
			want: true,
		},
		{
			name: "untyped wording wrapped",
			err:  fmt.Errorf("run group: %w", errors.New("received signal terminated")),
			want: true,
		},
		{
			name: "signal-shaped message for a signal we do not handle",
			err:  errors.New("received signal hangup"),
			want: false,
		},
		{
			name: "unrelated failure",
			err:  errors.New("listen tcp :8080: bind: address already in use"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsSignalError(tt.err); got != tt.want {
				t.Errorf("IsSignalError(%v) = %t, want %t", tt.err, got, tt.want)
			}
		})
	}
}

func TestSignalErrorMatchesSentinel(t *testing.T) {
	err := NewSignalError(syscall.SIGTERM)

	if !errors.Is(err, ErrSignal) {
		t.Errorf("errors.Is(%v, ErrSignal) = false, want true", err)
	}
	if errors.Is(errors.New("boom"), ErrSignal) {
		t.Error("errors.Is(unrelated error, ErrSignal) = true, want false")
	}
}

func TestSignalErrorMessageMatchesHistoricalWording(t *testing.T) {
	// Consumers pinned to an older lib still compare against this text, so the
	// wording is part of the contract and must not drift.
	if got, want := NewSignalError(syscall.SIGTERM).Error(), "received signal terminated"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestSignalErrorUnwrapsToSignal(t *testing.T) {
	err := NewSignalError(syscall.SIGTERM)

	var signalErr *SignalError
	if !errors.As(err, &signalErr) {
		t.Fatalf("errors.As(%v, *SignalError) = false, want true", err)
	}
	if signalErr.Signal != syscall.SIGTERM {
		t.Errorf("Signal = %v, want %v", signalErr.Signal, syscall.SIGTERM)
	}
}

func TestSignalErrorWithoutSignalDoesNotPanic(t *testing.T) {
	// A zero value should degrade to a plain message rather than panicking on
	// a nil os.Signal.
	if got := (&SignalError{}).Error(); got == "" {
		t.Error("Error() on zero value = empty string, want a message")
	}
}

func TestSignalsAreTheOnesHandlersInstall(t *testing.T) {
	want := []os.Signal{syscall.SIGINT, syscall.SIGTERM}

	got := Signals()
	if len(got) != len(want) {
		t.Fatalf("Signals() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Signals()[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestSignalsReturnsACopy(t *testing.T) {
	// Callers pass the result straight to signal.Notify; a shared backing array
	// would let one of them corrupt the list for everyone.
	first := Signals()
	first[0] = syscall.SIGHUP

	if second := Signals(); second[0] != syscall.SIGINT {
		t.Errorf("Signals()[0] = %v after caller mutation, want %v", second[0], syscall.SIGINT)
	}
}
