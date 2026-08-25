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

package cmd

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClusterRegistrationListUsesICMSWithAdminToken(t *testing.T) {
	t.Setenv("NVCF_TOKEN", "admin-token")
	t.Setenv("NVCF_API_KEY", "")
	viper.Reset()
	viper.Set("token", "admin-token")
	t.Cleanup(viper.Reset)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/accounts/nvcf-default/clusters", r.URL.Path)
		assert.Equal(t, "Bearer admin-token", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"clusterId":"cluster-1","clusterName":"test-compute-plane","clusterGroupId":"group-1","nvcaVersion":"3.2.7","status":"healthy"}]`))
	}))
	defer server.Close()

	cmd, _, err := clusterCmd.Find([]string{"registration", "list"})
	require.NoError(t, err)
	require.Equal(t, "list", cmd.Name())
	require.NoError(t, cmd.Flags().Set(clusterFlagNcaID, "nvcf-default"))
	require.NoError(t, cmd.Flags().Set(clusterFlagICMSURL, server.URL))
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = false })

	var output bytes.Buffer
	cmd.SetOut(&output)
	t.Cleanup(func() { cmd.SetOut(nil) })
	require.NoError(t, cmd.RunE(cmd, nil))
	assert.True(t, strings.Contains(output.String(), "test-compute-plane"), output.String())
}
