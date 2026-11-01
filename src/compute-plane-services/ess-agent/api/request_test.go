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

package api

import (
	"strings"
	"testing"
)

func TestRequestSetJSONBody(t *testing.T) {
	var r Request
	raw := map[string]interface{}{"foo": "bar"}
	if err := r.SetJSONBody(raw); err != nil {
		t.Fatalf("err: %s", err)
	}

	expected := `{"foo":"bar"}`
	actual := strings.TrimSpace(string(r.BodyBytes))
	if actual != expected {
		t.Fatalf("bad: %s", actual)
	}
}

func TestRequestResetJSONBody(t *testing.T) {
	var r Request
	raw := map[string]interface{}{"foo": "bar"}
	if err := r.SetJSONBody(raw); err != nil {
		t.Fatalf("err: %s", err)
	}

	if err := r.ResetJSONBody(); err != nil {
		t.Fatalf("err: %s", err)
	}

	buf := make([]byte, len(r.BodyBytes))
	copy(buf, r.BodyBytes)

	expected := `{"foo":"bar"}`
	actual := strings.TrimSpace(string(buf))
	if actual != expected {
		t.Fatalf("bad: actual %s, expected %s", actual, expected)
	}
}
