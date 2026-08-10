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

package nats

import (
	"testing"

	natsgo "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNATSHostOverrideOptions(t *testing.T) {
	assert.Empty(t, natsHostOverrideOptions("nats://bare.example.test:4222", "nats.bare.example.test"))
	assert.Empty(t, natsHostOverrideOptions("tls://bare.example.test:4222", ""))
	assert.Empty(t, natsHostOverrideOptions("%", "nats.bare.example.test"))

	opts := natsHostOverrideOptions("tls://bare.example.test:4222", "nats.bare.example.test")
	require.Len(t, opts, 1)

	var natsOpts natsgo.Options
	require.NoError(t, opts[0](&natsOpts))
	require.NotNil(t, natsOpts.TLSConfig)
	assert.Equal(t, "nats.bare.example.test", natsOpts.TLSConfig.ServerName)
}
