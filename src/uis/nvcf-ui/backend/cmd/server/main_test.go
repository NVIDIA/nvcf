// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package main

import (
	"testing"
	"time"
)

// TestDefaultTimeoutsAreSet guards the shipped defaults: a zero value means the
// phase has no deadline, which is the resource exhaustion these guard against.
func TestDefaultTimeoutsAreSet(t *testing.T) {
	defaults := map[string]time.Duration{
		readHeaderTimeoutSeconds: defaultReadHeaderTimeoutSeconds * time.Second,
		readTimeoutSeconds:       defaultReadTimeoutSeconds * time.Second,
		writeTimeoutSeconds:      defaultWriteTimeoutSeconds * time.Second,
		idleTimeoutSeconds:       defaultIdleTimeoutSeconds * time.Second,
	}
	for env := range defaults {
		t.Setenv(env, "")
	}

	timeouts, err := timeoutsFromEnv()
	if err != nil {
		t.Fatalf("timeoutsFromEnv() error = %v", err)
	}

	got := map[string]time.Duration{
		readHeaderTimeoutSeconds: timeouts.readHeader,
		readTimeoutSeconds:       timeouts.read,
		writeTimeoutSeconds:      timeouts.write,
		idleTimeoutSeconds:       timeouts.idle,
	}
	for env, want := range defaults {
		if got[env] <= 0 {
			t.Errorf("default for %s = %v, want a positive deadline", env, got[env])
		}
		if got[env] != want {
			t.Errorf("%s = %v, want %v", env, got[env], want)
		}
	}

	if timeouts.readHeader > timeouts.read {
		t.Errorf("read header timeout (%v) exceeds read timeout (%v); headers would "+
			"never be the limiting deadline", timeouts.readHeader, timeouts.read)
	}
}

func TestTimeoutsFromEnvOverrides(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want serverTimeouts
	}{
		{
			name: "each timeout is overridden in seconds",
			env: map[string]string{
				readHeaderTimeoutSeconds: "5",
				readTimeoutSeconds:       "15",
				writeTimeoutSeconds:      "45",
				idleTimeoutSeconds:       "90",
			},
			want: serverTimeouts{
				readHeader: 5 * time.Second,
				read:       15 * time.Second,
				write:      45 * time.Second,
				idle:       90 * time.Second,
			},
		},
		{
			name: "unset variables keep their defaults",
			env:  map[string]string{writeTimeoutSeconds: "600"},
			want: serverTimeouts{
				readHeader: defaultReadHeaderTimeoutSeconds * time.Second,
				read:       defaultReadTimeoutSeconds * time.Second,
				write:      600 * time.Second,
				idle:       defaultIdleTimeoutSeconds * time.Second,
			},
		},
		{
			name: "zero disables that deadline",
			env:  map[string]string{writeTimeoutSeconds: "0"},
			want: serverTimeouts{
				readHeader: defaultReadHeaderTimeoutSeconds * time.Second,
				read:       defaultReadTimeoutSeconds * time.Second,
				write:      0,
				idle:       defaultIdleTimeoutSeconds * time.Second,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, env := range []string{
				readHeaderTimeoutSeconds, readTimeoutSeconds,
				writeTimeoutSeconds, idleTimeoutSeconds,
			} {
				t.Setenv(env, tt.env[env])
			}

			got, err := timeoutsFromEnv()
			if err != nil {
				t.Fatalf("timeoutsFromEnv() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("timeoutsFromEnv() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestTimeoutsFromEnvRejectsBadValues covers the chart-typo case: an unusable
// value must fail startup rather than silently fall back to the default.
func TestTimeoutsFromEnvRejectsBadValues(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "not a number", value: "30s"},
		{name: "fractional", value: "1.5"},
		{name: "negative", value: "-1"},
		{name: "whitespace", value: " 30"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(readHeaderTimeoutSeconds, tt.value)

			if _, err := timeoutsFromEnv(); err == nil {
				t.Errorf("timeoutsFromEnv() with %s=%q returned no error, want one",
					readHeaderTimeoutSeconds, tt.value)
			}
		})
	}
}
