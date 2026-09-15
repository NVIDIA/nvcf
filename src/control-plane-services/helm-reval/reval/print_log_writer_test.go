// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package reval

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestPrintLogWriter_Write_ReturnsLenAndNilError(t *testing.T) {
	logger := zap.NewNop()
	w := printLogWriter{logger: logger}

	data := []byte("debug log line")
	n, err := w.Write(data)
	require.NoError(t, err)
	assert.Equal(t, len(data), n)
}

func TestPrintLogWriter_Write_EmptySlice(t *testing.T) {
	logger := zap.NewNop()
	w := printLogWriter{logger: logger}

	n, err := w.Write([]byte{})
	require.NoError(t, err)
	assert.Equal(t, 0, n)
}

func TestPrintLogWriter_Write_WithWhitespace(t *testing.T) {
	logger := zap.NewNop()
	w := printLogWriter{logger: logger}

	data := []byte("  line with surrounding spaces  \n")
	n, err := w.Write(data)
	require.NoError(t, err)
	assert.Equal(t, len(data), n)
}
