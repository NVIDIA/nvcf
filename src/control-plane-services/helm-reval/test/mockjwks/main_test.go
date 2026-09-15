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

package main

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBigEndianBytes(t *testing.T) {
	cases := []struct {
		in   int
		want []byte
	}{
		{0, []byte{0}},
		{1, []byte{1}},
		{0xff, []byte{0xff}},
		{0x100, []byte{0x01, 0x00}},
		{65537, []byte{0x01, 0x00, 0x01}}, // common RSA exponent
		{0x010203, []byte{0x01, 0x02, 0x03}},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, bigEndianBytes(c.in), "bigEndianBytes(%d)", c.in)
	}
}

func TestBuildJWKS(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	out, err := buildJWKS(&priv.PublicKey, "test-kid")
	require.NoError(t, err)

	var parsed struct {
		Keys []map[string]string `json:"keys"`
	}
	require.NoError(t, json.Unmarshal(out, &parsed))
	require.Len(t, parsed.Keys, 1)

	k := parsed.Keys[0]
	assert.Equal(t, "RSA", k["kty"])
	assert.Equal(t, "RS256", k["alg"])
	assert.Equal(t, "sig", k["use"])
	assert.Equal(t, "test-kid", k["kid"])
	assert.NotEmpty(t, k["n"])
	assert.NotEmpty(t, k["e"])
}
