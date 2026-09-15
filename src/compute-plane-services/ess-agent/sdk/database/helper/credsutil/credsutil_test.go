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

package credsutil

import (
	"strings"
	"testing"
)

func TestRandomAlphaNumeric(t *testing.T) {
	s, err := RandomAlphaNumeric(10, true)
	if err != nil {
		t.Fatalf("Unexpected error: %s", err)
	}
	if len(s) != 10 {
		t.Fatalf("Unexpected length of string, expected 10, got string: %s", s)
	}

	s, err = RandomAlphaNumeric(20, true)
	if err != nil {
		t.Fatalf("Unexpected error: %s", err)
	}
	if len(s) != 20 {
		t.Fatalf("Unexpected length of string, expected 20, got string: %s", s)
	}

	if !strings.Contains(s, reqStr) {
		t.Fatalf("Expected %s to contain %s", s, reqStr)
	}

	s, err = RandomAlphaNumeric(20, false)
	if err != nil {
		t.Fatalf("Unexpected error: %s", err)
	}
	if len(s) != 20 {
		t.Fatalf("Unexpected length of string, expected 20, got string: %s", s)
	}

	if strings.Contains(s, reqStr) {
		t.Fatalf("Expected %s not to contain %s", s, reqStr)
	}
}
