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

package types

import "testing"

func TestResultHandlingStrategyValues(t *testing.T) {
	tests := []struct {
		name string
		got  ResultHandlingStrategy
		want int64
	}{
		{"UPLOAD_STRATEGY", UPLOAD_STRATEGY, 0},
		{"NO_STRATEGY", NO_STRATEGY, 1},
		{"UNKNOWN_STRATEGY", UNKNOWN_STRATEGY, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if int64(tt.got) != tt.want {
				t.Errorf("%s = %d, want %d", tt.name, int64(tt.got), tt.want)
			}
		})
	}
}

func TestResultHandlingStrategyDistinct(t *testing.T) {
	if UPLOAD_STRATEGY == NO_STRATEGY {
		t.Error("UPLOAD_STRATEGY == NO_STRATEGY")
	}
	if NO_STRATEGY == UNKNOWN_STRATEGY {
		t.Error("NO_STRATEGY == UNKNOWN_STRATEGY")
	}
	if UPLOAD_STRATEGY == UNKNOWN_STRATEGY {
		t.Error("UPLOAD_STRATEGY == UNKNOWN_STRATEGY")
	}
}
