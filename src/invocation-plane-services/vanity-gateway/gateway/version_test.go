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
package gateway

import (
	"testing"

	"github.com/carlmjohnson/versioninfo"
)

func TestGetVersion(t *testing.T) {
	tests := []struct {
		name           string
		env            string
		stampedVersion string
		want           string
	}{
		{
			name:           "env var wins over stamped version",
			env:            "env-1.2.3",
			stampedVersion: "1.30.0",
			want:           "env-1.2.3",
		},
		{
			name:           "stamped version used when env unset",
			stampedVersion: "1.30.0",
			want:           "1.30.0",
		},
		{
			name: "falls back to versioninfo when nothing stamped",
			want: versioninfo.Revision,
		},
		{
			name:           "unresolved stamp placeholder is ignored",
			stampedVersion: "{STABLE_VERSION}",
			want:           versioninfo.Revision,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.env != "" {
				t.Setenv("APP_VERSION", tt.env)
			}
			old := version
			version = tt.stampedVersion
			defer func() { version = old }()
			if got := GetVersion(); got != tt.want {
				t.Errorf("GetVersion() = %v, want %v", got, tt.want)
			}
		})
	}
}
