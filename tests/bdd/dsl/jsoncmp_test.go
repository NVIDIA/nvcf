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

const helmListOutput = `[
  {"name": "nats", "namespace": "nats-system", "status": "deployed"},
  {"name": "cert-manager", "namespace": "cert-manager", "status": "deployed"},
  {"name": "api", "namespace": "nvcf", "status": "deployed"}
]`

func TestJSONContainsRowsMatchesExpected(t *testing.T) {
	rows := []map[string]string{
		{"name": "nats", "namespace": "nats-system", "status": "deployed"},
		{"name": "api", "namespace": "nvcf", "status": "deployed"},
	}
	if err := JSONContainsRows(helmListOutput, rows); err != nil {
		t.Fatalf("contains rows: %v", err)
	}
}

func TestJSONContainsRowsFailsWhenRowMissing(t *testing.T) {
	rows := []map[string]string{
		{"name": "missing", "namespace": "nvcf"},
	}
	if err := JSONContainsRows(helmListOutput, rows); err == nil {
		t.Fatal("expected error when row absent")
	}
}

func TestJSONContainsRowsInterpolatesCells(t *testing.T) {
	t.Setenv("EXPECTED_RELEASE", "api")
	rows := []map[string]string{
		{"name": "${EXPECTED_RELEASE}", "namespace": "nvcf"},
	}
	if err := JSONContainsRows(helmListOutput, rows); err != nil {
		t.Fatalf("contains rows: %v", err)
	}
}

func TestJSONContainsRowsParseError(t *testing.T) {
	if err := JSONContainsRows("not json", nil); err == nil {
		t.Fatal("expected parse error")
	}
}
