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

package dsl

import "testing"

func TestTaskStatusReturnsTheRawStatusString(t *testing.T) {
	for _, status := range []string{"RUNNING", "completed", "NOT_A_KNOWN_STATUS", ""} {
		got, err := TaskStatus(`{"task":{"id":"task-1","status":"` + status + `","percentComplete":5}}`)
		if err != nil {
			t.Fatalf("status %q: %v", status, err)
		}
		if got != status {
			t.Fatalf("status = %q, want %q", got, status)
		}
	}
}

func TestTaskStatusIsEmptyWhenTheFieldIsAbsent(t *testing.T) {
	got, err := TaskStatus(`{"task":{"id":"task-1"}}`)
	if err != nil || got != "" {
		t.Fatalf("status = %q, err = %v, want empty status and no error", got, err)
	}
}

func TestTaskStatusRejectsInvalidJSON(t *testing.T) {
	if _, err := TaskStatus("not json"); err == nil {
		t.Fatal("expected a parse error")
	}
}
