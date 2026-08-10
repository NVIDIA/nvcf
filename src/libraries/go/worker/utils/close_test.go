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

package utils

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClosePreservingError(t *testing.T) {
	closeErr := errors.New("close failed")

	t.Run("existing error takes precedence", func(t *testing.T) {
		existing := errors.New("original")
		err := existing
		ClosePreservingError(&err, CloserFunc(func() error { return closeErr }))
		require.Equal(t, existing, err, "close error must not overwrite an existing error")
	})
	t.Run("nil existing error captures the close error", func(t *testing.T) {
		var err error
		ClosePreservingError(&err, CloserFunc(func() error { return closeErr }))
		require.Equal(t, closeErr, err)
	})
	t.Run("nil existing error and successful close stays nil", func(t *testing.T) {
		var err error
		ClosePreservingError(&err, CloserFunc(func() error { return nil }))
		require.NoError(t, err)
	})
}

func TestCloserFunc(t *testing.T) {
	want := errors.New("boom")
	require.Equal(t, want, CloserFunc(func() error { return want }).Close())
}

func TestClose(t *testing.T) {
	// Close swallows the error (it only logs a warning); exercise both paths.
	Close(func() error { return nil })
	Close(func() error { return errors.New("ignored") })
}
