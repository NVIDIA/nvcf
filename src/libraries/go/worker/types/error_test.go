/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package types

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWorkerErrorError(t *testing.T) {
	require.Equal(t, "internal error: boom", NewInternalError(errors.New("boom")).Error())
	require.Equal(t, "user actionable error: bad input", NewUserActionableError(errors.New("bad input")).Error())
	require.Equal(t, "", NewInternalError(nil).Error(), "nil inner error yields empty string")
}

func TestWorkerErrorUnwrap(t *testing.T) {
	inner := errors.New("inner")
	require.ErrorIs(t, NewInternalError(inner), inner)
	require.Equal(t, inner, NewUserActionableError(inner).Unwrap())
}

func TestWorkerErrorWrap(t *testing.T) {
	base := NewInternalError(errors.New("base"))
	extra := errors.New("extra")
	wrapped := base.Wrap(extra)

	require.True(t, wrapped.isInternal, "isInternal is preserved across Wrap")
	require.ErrorIs(t, wrapped, extra, "the wrapped error is reachable via errors.Is")
}

func TestWorkerErrorJSONRoundTrip(t *testing.T) {
	original := NewUserActionableError(errors.New("oops"))

	data, err := json.Marshal(original)
	require.NoError(t, err)
	require.JSONEq(t, `{"message":"oops","isInternal":false}`, string(data),
		"message field carries the raw inner message (no prefix)")

	var decoded WorkerError
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.False(t, decoded.isInternal)
	require.Equal(t, "user actionable error: oops", decoded.Error())
}

// Documents current behavior: MarshalJSON dereferences the inner error without a
// nil check, so a WorkerError built with a nil inner panics on marshal. Error()
// is nil-safe; this asymmetry is flagged for a follow-up fix.
func TestWorkerErrorMarshalNilInnerPanics(t *testing.T) {
	require.Panics(t, func() {
		_, _ = NewInternalError(nil).MarshalJSON()
	})
}
