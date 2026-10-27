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
	"context"
	"errors"
	"flag"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ddb "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store/dynamodb"
)

// fakeClient is a SchemaAPI whose table becomes ACTIVE at once.
type fakeClient struct {
	exists  bool
	created *ddb.CreateTableInput
	ttl     bool
	pitr    bool
}

func (f *fakeClient) CreateTable(_ context.Context, in *ddb.CreateTableInput, _ ...func(*ddb.Options)) (*ddb.CreateTableOutput, error) {
	if f.exists {
		return nil, &ddbtypes.ResourceInUseException{Message: aws.String("exists")}
	}
	f.created = in
	return &ddb.CreateTableOutput{}, nil
}

func (f *fakeClient) DescribeTable(_ context.Context, in *ddb.DescribeTableInput, _ ...func(*ddb.Options)) (*ddb.DescribeTableOutput, error) {
	def := dynamodb.CreateTableInput(dynamodb.TableOptions{TableName: aws.ToString(in.TableName)})
	return &ddb.DescribeTableOutput{Table: &ddbtypes.TableDescription{
		TableName:            in.TableName,
		TableStatus:          ddbtypes.TableStatusActive,
		KeySchema:            def.KeySchema,
		AttributeDefinitions: def.AttributeDefinitions,
	}}, nil
}

func (f *fakeClient) DescribeTimeToLive(context.Context, *ddb.DescribeTimeToLiveInput, ...func(*ddb.Options)) (*ddb.DescribeTimeToLiveOutput, error) {
	return &ddb.DescribeTimeToLiveOutput{}, nil
}

func (f *fakeClient) UpdateTimeToLive(context.Context, *ddb.UpdateTimeToLiveInput, ...func(*ddb.Options)) (*ddb.UpdateTimeToLiveOutput, error) {
	f.ttl = true
	return &ddb.UpdateTimeToLiveOutput{}, nil
}

func (f *fakeClient) DescribeContinuousBackups(context.Context, *ddb.DescribeContinuousBackupsInput, ...func(*ddb.Options)) (*ddb.DescribeContinuousBackupsOutput, error) {
	return &ddb.DescribeContinuousBackupsOutput{}, nil
}

func (f *fakeClient) UpdateContinuousBackups(context.Context, *ddb.UpdateContinuousBackupsInput, ...func(*ddb.Options)) (*ddb.UpdateContinuousBackupsOutput, error) {
	f.pitr = true
	return &ddb.UpdateContinuousBackupsOutput{}, nil
}

func runFake(t *testing.T, f *fakeClient, args ...string) (dynamodb.ClientOptions, string, string, error) {
	t.Helper()
	var got dynamodb.ClientOptions
	var stdout, stderr bytes.Buffer
	err := RunWith(t.Context(), args, &stdout, &stderr, func(_ context.Context, opts dynamodb.ClientOptions) (dynamodb.SchemaAPI, error) {
		got = opts
		return f, nil
	})
	return got, stdout.String(), stderr.String(), err
}

func TestCreate(t *testing.T) {
	f := &fakeClient{}
	opts, stdout, _, err := runFake(t, f, "--table", "gds-test", "--region", "us-west-2", "--endpoint", "http://localhost:8000")
	require.NoError(t, err)
	assert.Equal(t, dynamodb.ClientOptions{Region: "us-west-2", Endpoint: "http://localhost:8000"}, opts)
	assert.Equal(t, "gds-test", aws.ToString(f.created.TableName))
	assert.True(t, aws.ToBool(f.created.DeletionProtectionEnabled))
	assert.True(t, f.ttl)
	assert.True(t, f.pitr)
	assert.Equal(t, "created table gds-test\nTTL on expires_at is enabled\npoint-in-time recovery is enabled\n", stdout)
}

func TestExistingTableWithoutExtras(t *testing.T) {
	f := &fakeClient{exists: true}
	_, stdout, _, err := runFake(t, f, "--pitr=false", "--ttl=false", "--deletion-protection=false", "--wait-timeout", time.Second.String())
	require.NoError(t, err)
	assert.False(t, f.ttl)
	assert.False(t, f.pitr)
	assert.Equal(t, "table gds exists; key schema checked\n", stdout)
}

func TestUsageErrors(t *testing.T) {
	for _, args := range [][]string{{"--nope"}, {"extra"}, {"--table", ""}} {
		_, _, _, err := runFake(t, &fakeClient{}, args...)
		assert.ErrorAs(t, err, new(UsageError), "%v", args)
	}
	_, _, stderr, err := runFake(t, &fakeClient{}, "-h")
	assert.ErrorIs(t, err, flag.ErrHelp)
	assert.Contains(t, stderr, "-endpoint string")
}

func TestClientError(t *testing.T) {
	boom := errors.New("no credentials")
	err := RunWith(t.Context(), nil, &bytes.Buffer{}, &bytes.Buffer{}, func(context.Context, dynamodb.ClientOptions) (dynamodb.SchemaAPI, error) {
		return nil, boom
	})
	assert.ErrorIs(t, err, boom)
}
