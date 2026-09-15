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

package policy

import (
	"bytes"
	"strings"
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/clients"
	nverrors "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/errors"
	"github.com/stretchr/testify/require"
)

func TestNewAuthzClientRejectsNilPolicyConfig(t *testing.T) {
	client, err := NewAuthzClient(&clients.BaseClientConfig{
		Type: string(clients.ClientTypeHTTP),
	}, nil)

	require.Nil(t, client)
	require.ErrorIs(t, err, nverrors.ErrBadConfig)
}

func TestReadPolicyResponseBody(t *testing.T) {
	t.Run("accepts response within limit", func(t *testing.T) {
		body, err := readPolicyResponseBody(strings.NewReader("allowed"))

		require.NoError(t, err)
		require.Equal(t, []byte("allowed"), body)
	})

	t.Run("rejects response over limit", func(t *testing.T) {
		reader := bytes.NewReader(make([]byte, maxPolicyResponseBodyBytes+1))

		body, err := readPolicyResponseBody(reader)

		require.Nil(t, body)
		require.Error(t, err)
	})
}
