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

package dynamodb

import (
	"path/filepath"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// isolateAWSConfig keeps the developer's AWS files and environment out of a
// test.
func isolateAWSConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(dir, "config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "credentials"))
	for _, v := range []string{"AWS_REGION", "AWS_DEFAULT_REGION", "AWS_PROFILE", "AWS_ENDPOINT_URL", "AWS_ENDPOINT_URL_DYNAMODB"} {
		t.Setenv(v, "")
	}
}

func TestNewClient(t *testing.T) {
	isolateAWSConfig(t)
	_, err := NewClient(t.Context(), ClientOptions{})
	assert.ErrorContains(t, err, "no AWS region")

	c, err := NewClient(t.Context(), ClientOptions{Region: "us-west-2", Endpoint: "http://localhost:8000"})
	require.NoError(t, err)
	assert.Equal(t, "us-west-2", c.Options().Region)
	assert.Equal(t, "http://localhost:8000", aws.ToString(c.Options().BaseEndpoint))

	t.Setenv("AWS_REGION", "eu-west-1")
	c, err = NewClient(t.Context(), ClientOptions{})
	require.NoError(t, err)
	assert.Equal(t, "eu-west-1", c.Options().Region)
	assert.Nil(t, c.Options().BaseEndpoint)

	t.Setenv("AWS_PROFILE", "missing")
	_, err = NewClient(t.Context(), ClientOptions{})
	assert.ErrorContains(t, err, "load AWS configuration")
}
