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

package logging

import (
	"errors"
	"os"
	"reflect"
	"testing"
)

func Test_ParseLogFormat(t *testing.T) {
	type testData struct {
		format      string
		expected    LogFormat
		expectedErr error
	}

	tests := []testData{
		{format: "", expected: UnspecifiedFormat, expectedErr: nil},
		{format: " ", expected: UnspecifiedFormat, expectedErr: nil},
		{format: "standard", expected: StandardFormat, expectedErr: nil},
		{format: "STANDARD", expected: StandardFormat, expectedErr: nil},
		{format: "json", expected: JSONFormat, expectedErr: nil},
		{format: " json ", expected: JSONFormat, expectedErr: nil},
		{format: "bogus", expected: UnspecifiedFormat, expectedErr: errors.New("unknown log format: bogus")},
	}

	for _, test := range tests {
		result, err := ParseLogFormat(test.format)
		if test.expected != result {
			t.Errorf("expected %s, got %s", test.expected, result)
		}
		if !reflect.DeepEqual(test.expectedErr, err) {
			t.Errorf("expected error %v, got %v", test.expectedErr, err)
		}
	}
}

func Test_ParseEnv_VAULT_LOG_FORMAT(t *testing.T) {
	oldVLF := os.Getenv("VAULT_LOG_FORMAT")
	defer os.Setenv("VAULT_LOG_FORMAT", oldVLF)

	testParseEnvLogFormat(t, "VAULT_LOG_FORMAT")
}

func testParseEnvLogFormat(t *testing.T, name string) {
	env := []string{
		"json", "vauLT_Json", "VAULT-JSON", "vaulTJSon",
		"standard", "STANDARD",
		"bogus",
	}

	formats := []LogFormat{
		JSONFormat, JSONFormat, JSONFormat, JSONFormat,
		StandardFormat, StandardFormat,
		UnspecifiedFormat,
	}

	for i, e := range env {
		os.Setenv(name, e)
		if lf := ParseEnvLogFormat(); formats[i] != lf {
			t.Errorf("expected %s, got %s", formats[i], lf)
		}
	}
}
