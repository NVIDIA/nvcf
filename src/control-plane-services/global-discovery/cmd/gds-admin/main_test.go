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

package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name   string
		args   []string
		code   int
		stdout string
		stderr string
	}{
		{name: "no command", args: nil, code: 2, stderr: "Usage: gds-admin <command>"},
		{name: "help", args: []string{"--help"}, code: 0, stdout: "create-table"},
		{name: "unknown", args: []string{"drop-table"}, code: 2, stderr: `unknown command "drop-table"`},
		{name: "command help", args: []string{"create-table", "-h"}, code: 0, stderr: "Usage: gds-admin create-table"},
		{name: "bad flag", args: []string{"create-table", "--nope"}, code: 2, stderr: "flag provided but not defined"},
		{name: "extra argument", args: []string{"create-table", "extra"}, code: 2, stderr: "unexpected arguments"},
		{name: "failure", args: []string{"create-table", "--region", "", "--table", "gds"}, code: 1, stderr: "gds-admin create-table:"},
	}
	t.Setenv("AWS_CONFIG_FILE", t.TempDir()+"/config")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", t.TempDir()+"/credentials")
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "")
	t.Setenv("AWS_PROFILE", "")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(t.Context(), tt.args, &stdout, &stderr)
			assert.Equal(t, tt.code, code, "stderr: %s", stderr.String())
			assert.Contains(t, stdout.String(), tt.stdout)
			assert.Contains(t, stderr.String(), tt.stderr)
		})
	}
}
