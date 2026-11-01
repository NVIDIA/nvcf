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
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"helm.sh/helm/v3/pkg/chart"
)

// ── logFromContext ─────────────────────────────────────────────────────────────

func TestLogFromContext_NoLogger_ReturnsNop(t *testing.T) {
	// When context has no logger, logFromContext should return a nop logger.
	l := logFromContext(context.Background())
	assert.NotNil(t, l)
}

// ── checkIfInstallable ────────────────────────────────────────────────────────

func TestCheckIfInstallable_EmptyType_OK(t *testing.T) {
	ch := &chart.Chart{Metadata: &chart.Metadata{Type: ""}}
	assert.NoError(t, checkIfInstallable(ch))
}

func TestCheckIfInstallable_ApplicationType_OK(t *testing.T) {
	ch := &chart.Chart{Metadata: &chart.Metadata{Type: "application"}}
	assert.NoError(t, checkIfInstallable(ch))
}

func TestCheckIfInstallable_LibraryType_Error(t *testing.T) {
	ch := &chart.Chart{Metadata: &chart.Metadata{Type: "library"}}
	err := checkIfInstallable(ch)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "library charts are not installable")
}

// ── isErrHTTPAuthIssue ────────────────────────────────────────────────────────

func TestIsErrHTTPAuthIssue_Nil_False(t *testing.T) {
	assert.False(t, isErrHTTPAuthIssue(nil))
}

func TestIsErrHTTPAuthIssue_NoMatch_False(t *testing.T) {
	assert.False(t, isErrHTTPAuthIssue(errors.New("some unrelated error")))
}

func TestIsErrHTTPAuthIssue_401_True(t *testing.T) {
	err := errors.New("failed to fetch https://example.com : 401 Unauthorized")
	assert.True(t, isErrHTTPAuthIssue(err))
}

func TestIsErrHTTPAuthIssue_403_True(t *testing.T) {
	err := errors.New("failed to fetch https://example.com : 403 Forbidden")
	assert.True(t, isErrHTTPAuthIssue(err))
}

func TestIsErrHTTPAuthIssue_500_False(t *testing.T) {
	err := errors.New("failed to fetch https://example.com : 500 Internal Server Error")
	assert.False(t, isErrHTTPAuthIssue(err))
}
