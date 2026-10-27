//go:build integration

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

package createtable

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ddb "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store/dynamodb"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store/dynamodb/dynamodblocal"
)

func TestMain(m *testing.M) {
	code := m.Run()
	if err := dynamodblocal.Terminate(); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(code)
}

// TestRunAgainstDynamoDBLocal runs the command as an operator would, with
// credentials and region from the environment.
func TestRunAgainstDynamoDBLocal(t *testing.T) {
	endpoint := dynamodblocal.Endpoint(t)
	dir := t.TempDir()
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(dir, "config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "credentials"))
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_REGION", dynamodblocal.Region)
	t.Setenv("AWS_ACCESS_KEY_ID", "local")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "local")

	args := []string{"--endpoint", endpoint, "--table", "gds-admin-test", "--pitr=false", "--deletion-protection=false"}
	var stdout bytes.Buffer
	require.NoError(t, Run(t.Context(), args, &stdout, os.Stderr))
	assert.Contains(t, stdout.String(), "created table gds-admin-test")

	stdout.Reset()
	require.NoError(t, Run(t.Context(), args, &stdout, os.Stderr), "running it again is safe")
	assert.Contains(t, stdout.String(), "table gds-admin-test exists")

	client := dynamodblocal.NewClient(endpoint)
	out, err := client.DescribeTimeToLive(t.Context(), &ddb.DescribeTimeToLiveInput{TableName: aws.String("gds-admin-test")})
	require.NoError(t, err)
	assert.Equal(t, dynamodb.AttributeExpiresAt, aws.ToString(out.TimeToLiveDescription.AttributeName))
	_, err = client.DeleteTable(t.Context(), &ddb.DeleteTableInput{TableName: aws.String("gds-admin-test")})
	require.NoError(t, err)
}
