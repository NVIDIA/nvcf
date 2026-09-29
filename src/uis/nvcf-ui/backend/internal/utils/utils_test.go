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

package utils

import (
	"os"
	"testing"

	"github.com/rs/zerolog"
)

func TestGetEnvOr(t *testing.T) {
	const key = "NVCF_UI_SHIM_TEST_ENV"

	tests := []struct {
		name     string
		set      bool
		value    string
		fallback string
		want     string
	}{
		{name: "unset returns fallback", set: false, fallback: "def", want: "def"},
		{name: "empty returns fallback", set: true, value: "", fallback: "def", want: "def"},
		{name: "set returns value", set: true, value: "custom", fallback: "def", want: "custom"},
		{name: "set wins over empty fallback", set: true, value: "v", fallback: "", want: "v"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.set {
				t.Setenv(key, tt.value)
			} else {
				// t.Setenv requires a value; register cleanup via it, then unset
				// so the variable is genuinely absent for this case.
				t.Setenv(key, "sentinel")
				if err := os.Unsetenv(key); err != nil {
					t.Fatalf("unset: %v", err)
				}
			}

			if got := GetEnvOr(key, tt.fallback); got != tt.want {
				t.Errorf("GetEnvOr(%q, %q) = %q, want %q", key, tt.fallback, got, tt.want)
			}
		})
	}
}

func TestConfigLogger(t *testing.T) {
	tests := []struct {
		name      string
		level     string
		want      zerolog.Level
		wantFatal bool // invalid level => logger.Fatal()
	}{
		{name: "empty defaults to info", level: "", want: zerolog.InfoLevel},
		{name: "debug", level: "debug", want: zerolog.DebugLevel},
		{name: "warn", level: "warn", want: zerolog.WarnLevel},
		{name: "error", level: "error", want: zerolog.ErrorLevel},
		{name: "invalid level is fatal", level: "bogus", wantFatal: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(logLevel, tt.level)

			if tt.wantFatal {
				// zerolog's Fatal() calls os.Exit(1) by default, which recover()
				// cannot catch. Redirect it to panic so the branch runs in-process
				// (and is covered), then recover the panic to assert it fired.
				origExit := zerolog.FatalExitFunc
				zerolog.FatalExitFunc = func() { panic("fatal") }
				t.Cleanup(func() { zerolog.FatalExitFunc = origExit })

				// Fatal() also closes its writer; ConfigLogger logs to os.Stdout,
				// so redirect it to a throwaway file to spare the test runner's
				// real stdout, and restore it afterwards.
				realStdout := os.Stdout
				f, err := os.CreateTemp(t.TempDir(), "stdout")
				if err != nil {
					t.Fatalf("create temp stdout: %v", err)
				}
				os.Stdout = f
				t.Cleanup(func() { os.Stdout = realStdout })

				defer func() {
					if recover() == nil {
						t.Errorf("ConfigLogger(%q) did not fatal", tt.level)
					}
				}()
				ConfigLogger()
				return
			}

			if got := ConfigLogger().GetLevel(); got != tt.want {
				t.Errorf("ConfigLogger level = %v, want %v", got, tt.want)
			}
		})
	}
}
