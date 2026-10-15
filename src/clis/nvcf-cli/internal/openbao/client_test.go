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

package openbao

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const openBaoTestCertPEM = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"

func TestRootCAPEMFromOpenBaoResponseAcceptsJSONCertificate(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"data": map[string]string{"certificate": openBaoTestCertPEM},
	})
	require.NoError(t, err)

	got, err := rootCAPEMFromOpenBaoResponse(string(body))
	require.NoError(t, err)
	assert.Equal(t, openBaoTestCertPEM, got)
}

func TestRootCAPEMFromOpenBaoResponseAcceptsRawPEM(t *testing.T) {
	got, err := rootCAPEMFromOpenBaoResponse(openBaoTestCertPEM)
	require.NoError(t, err)
	assert.Equal(t, openBaoTestCertPEM, got)
}

func TestRootCAPEMFromOpenBaoResponseMapsMissingPKIToSentinel(t *testing.T) {
	_, err := rootCAPEMFromOpenBaoResponse(`{"errors":["no handler for route \"services/all/pki/root/cert/ca\""]}`)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrPKICertificateNotFound))
}

func TestKubectlBaseArgsIncludesContext(t *testing.T) {
	c := NewClient(&Config{KubeconfigPath: "/tmp/kubeconfig", KubeContext: "cp-context"}, nil)
	assert.Equal(t, []string{"kubectl", "--kubeconfig", "/tmp/kubeconfig", "--context", "cp-context"}, c.kubectlBaseArgs())
}

func TestFilterKubectlOutputPreservesPEMBlockWithKubectlNoise(t *testing.T) {
	c := NewClient(&Config{}, nil)
	got := c.filterKubectlOutput("pod \"openbao-pki-root-ca\" deleted\n" + openBaoTestCertPEM + "pod \"openbao-pki-root-ca\" deleted\n")
	assert.Equal(t, openBaoTestCertPEM[:len(openBaoTestCertPEM)-1], got)
}
