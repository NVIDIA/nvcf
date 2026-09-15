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

package progress

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var update = flag.Bool("update-golden", false, "rewrite testdata/*.golden to match current output")

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

// fakeClock returns a sequence of timestamps so tests can pin output
// timestamps to known values rather than time.Now().
type fakeClock struct {
	times []time.Time
	i     int
}

func (c *fakeClock) Now() time.Time {
	if c.i >= len(c.times) {
		return c.times[len(c.times)-1]
	}
	t := c.times[c.i]
	c.i++
	return t
}

// assertGolden compares actual output to a golden file. With -update-golden,
// rewrites the file to match. The convention follows Go's stdlib (cmd/gofmt,
// cmd/go) and avoids the brittleness of inline string literals.
func assertGolden(t *testing.T, path, actual string) {
	t.Helper()
	if *update {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(actual), 0o644))
		return
	}
	expected, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(expected), actual, "golden mismatch (run with -update-golden to refresh): %s", path)
}
