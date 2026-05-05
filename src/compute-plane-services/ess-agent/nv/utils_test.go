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

package nv

import "testing"

func TestParseActionAndPath(t *testing.T) {
	tests := []struct {
		input          string
		expectedAction string
		expectedPath   string
	}{
		// valid cases
		{"ess.read(kv2/data/test)", "read", "kv2/data/test"},
		{"ess.list(kv2/data/test)", "list", "kv2/data/test"},
		{"ess.write(my/secret/path -> test)", "write", "my/secret/path -> test"},
		{"data.delete(some/other/path)", undetermined, undetermined},
		{"ess.read(nested/path/with.parentheses)", "read", "nested/path/with.parentheses"},
		{"  ess.read(some/path)  ", "read", "some/path"},
		{"ess.pki(nested/path/with.parentheses -> pki)", "pki", "nested/path/with.parentheses -> pki"},

		// invalid cases
		{".read(some/path)", undetermined, undetermined},
		{"ess.read(some/path", undetermined, undetermined},
		{"ess.read", undetermined, undetermined},
		{"ess.read()", undetermined, undetermined},
		{"ess.(noaction)", undetermined, undetermined},
		{"invalid.input", undetermined, undetermined},
		{"ess.update(multiword path/with spaces)", undetermined, undetermined},
		{"ess.read(path/with-special_characters!@#$%^&*())", undetermined, undetermined},
		{"ESS.READ(some/path)", undetermined, undetermined},
		{"", undetermined, undetermined},
		{"ess.action123(some/path)", undetermined, undetermined},
		{"ess.read-write(some/path)", undetermined, undetermined},
		{"ess.invalid.read(some/path)", undetermined, undetermined},
	}

	for _, test := range tests {
		action, path := ParseActionAndPath(test.input)
		if action != test.expectedAction || path != test.expectedPath {
			t.Errorf("for input '%s': expected operation='%s', path='%s' but got operation='%s', path='%s'",
				test.input, test.expectedAction, test.expectedPath, action, path)
		}
	}
}
