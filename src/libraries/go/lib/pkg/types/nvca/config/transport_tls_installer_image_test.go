// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvcaconfig

import (
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTransportTLSConfigDoesNotRetainInstallerImage(t *testing.T) {
	decoded, err := DecodeConfig([]byte(`workload:
  transportTLS:
    trustMode: bundle
    installerImage: nvcr.io/legacy/nvca:old
`))
	require.NoError(t, err)

	encoded, err := EncodeConfig(decoded)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "installerImage")

	t.Setenv("NVCF_WORKLOAD_TRANSPORT_TLS_INSTALLER_IMAGE", "nvcr.io/legacy/nvca:old")
	v := viper.New()
	v.SetEnvPrefix("NVCF")
	v.AutomaticEnv()
	require.NoError(t, AutobindEnvs(v))
	assert.Empty(t, v.GetString("workload.transportTls.installerImage"))
}
