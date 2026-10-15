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
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPortSafeUrl(t *testing.T) {
	t.Run("http defaults to :80", func(t *testing.T) {
		u, err := PortSafeUrl("http://example")
		require.NoError(t, err)
		require.Equal(t, "example:80", u.Host)
	})
	t.Run("https defaults to :443", func(t *testing.T) {
		u, err := PortSafeUrl("https://example")
		require.NoError(t, err)
		require.Equal(t, "example:443", u.Host)
	})
	t.Run("explicit http port preserved", func(t *testing.T) {
		u, err := PortSafeUrl("http://example:1234")
		require.NoError(t, err)
		require.Equal(t, "example:1234", u.Host)
	})
	t.Run("explicit https port preserved", func(t *testing.T) {
		u, err := PortSafeUrl("https://example:8443")
		require.NoError(t, err)
		require.Equal(t, "example:8443", u.Host)
	})
	t.Run("non-http scheme defaults to :443", func(t *testing.T) {
		u, err := PortSafeUrl("grpc://example")
		require.NoError(t, err)
		require.Equal(t, "example:443", u.Host)
	})
	t.Run("invalid url returns error", func(t *testing.T) {
		_, err := PortSafeUrl("%")
		require.Error(t, err)
	})
}
