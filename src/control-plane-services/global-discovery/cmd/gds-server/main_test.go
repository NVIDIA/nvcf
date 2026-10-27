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

func noEnv(string) (string, bool) { return "", false }

func TestRun(t *testing.T) {
	var out bytes.Buffer
	assert.Equal(t, 0, run([]string{"--region=us-east-1"}, noEnv, &out))
	assert.Contains(t, out.String(), `"region":"us-east-1"`)
	assert.Contains(t, out.String(), `"store":"memory"`)

	out.Reset()
	assert.Equal(t, 2, run([]string{"--store=etcd"}, noEnv, &out))
	assert.Contains(t, out.String(), "gds-server: --store")

	out.Reset()
	assert.Equal(t, 0, run([]string{"--help"}, noEnv, &out))
	assert.Contains(t, out.String(), "Usage of gds-server")
}
