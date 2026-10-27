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
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ddb "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCreateTableInputMatchesJSON keeps testdata/create-table.json, the
// --cli-input-json form docs/storage.md shows, equal to the request
// CreateTable sends.
func TestCreateTableInputMatchesJSON(t *testing.T) {
	raw, err := os.ReadFile("testdata/create-table.json")
	require.NoError(t, err)
	var want ddb.CreateTableInput
	require.NoError(t, json.Unmarshal(raw, &want))
	assert.Equal(t, &want, CreateTableInput(DefaultTableOptions(DefaultTableName)))
}

func TestCreateTableInputTags(t *testing.T) {
	in := CreateTableInput(TableOptions{TableName: "t", Tags: map[string]string{"b": "2", "a": "1"}})
	assert.Equal(t, []ddbtypes.Tag{{Key: aws.String("a"), Value: aws.String("1")}, {Key: aws.String("b"), Value: aws.String("2")}}, in.Tags)
	assert.False(t, aws.ToBool(in.DeletionProtectionEnabled))
}

// fakeSchema is a scripted SchemaAPI.
type fakeSchema struct {
	createErr   error
	describeErr error
	// statuses are returned by successive DescribeTable calls; the last
	// one repeats.
	statuses  []ddbtypes.TableStatus
	keySchema []ddbtypes.KeySchemaElement
	keyType   ddbtypes.ScalarAttributeType

	ttl          *ddbtypes.TimeToLiveDescription
	ttlErr       error
	updateTTLErr error
	pitr         ddbtypes.PointInTimeRecoveryStatus
	pitrErr      error
	updatePITErr error

	created     *ddb.CreateTableInput
	describes   int
	ttlUpdates  int
	pitrUpdates int
}

func (f *fakeSchema) CreateTable(_ context.Context, in *ddb.CreateTableInput, _ ...func(*ddb.Options)) (*ddb.CreateTableOutput, error) {
	f.created = in
	return &ddb.CreateTableOutput{}, f.createErr
}

func (f *fakeSchema) DescribeTable(_ context.Context, in *ddb.DescribeTableInput, _ ...func(*ddb.Options)) (*ddb.DescribeTableOutput, error) {
	if f.describeErr != nil {
		return nil, f.describeErr
	}
	status := f.statuses[min(f.describes, len(f.statuses)-1)]
	f.describes++
	keys := f.keySchema
	if keys == nil {
		keys = CreateTableInput(TableOptions{}).KeySchema
	}
	keyType := f.keyType
	if keyType == "" {
		keyType = ddbtypes.ScalarAttributeTypeS
	}
	return &ddb.DescribeTableOutput{Table: &ddbtypes.TableDescription{
		TableName:   in.TableName,
		TableStatus: status,
		KeySchema:   keys,
		AttributeDefinitions: []ddbtypes.AttributeDefinition{
			{AttributeName: aws.String(AttributePK), AttributeType: keyType},
			{AttributeName: aws.String(AttributeSK), AttributeType: ddbtypes.ScalarAttributeTypeS},
		},
	}}, nil
}

func (f *fakeSchema) DescribeTimeToLive(context.Context, *ddb.DescribeTimeToLiveInput, ...func(*ddb.Options)) (*ddb.DescribeTimeToLiveOutput, error) {
	return &ddb.DescribeTimeToLiveOutput{TimeToLiveDescription: f.ttl}, f.ttlErr
}

func (f *fakeSchema) UpdateTimeToLive(_ context.Context, in *ddb.UpdateTimeToLiveInput, _ ...func(*ddb.Options)) (*ddb.UpdateTimeToLiveOutput, error) {
	f.ttlUpdates++
	if f.updateTTLErr == nil {
		f.ttl = &ddbtypes.TimeToLiveDescription{AttributeName: in.TimeToLiveSpecification.AttributeName, TimeToLiveStatus: ddbtypes.TimeToLiveStatusEnabling}
	}
	return &ddb.UpdateTimeToLiveOutput{}, f.updateTTLErr
}

func (f *fakeSchema) DescribeContinuousBackups(context.Context, *ddb.DescribeContinuousBackupsInput, ...func(*ddb.Options)) (*ddb.DescribeContinuousBackupsOutput, error) {
	if f.pitrErr != nil {
		return nil, f.pitrErr
	}
	return &ddb.DescribeContinuousBackupsOutput{ContinuousBackupsDescription: &ddbtypes.ContinuousBackupsDescription{
		ContinuousBackupsStatus:        ddbtypes.ContinuousBackupsStatusEnabled,
		PointInTimeRecoveryDescription: &ddbtypes.PointInTimeRecoveryDescription{PointInTimeRecoveryStatus: f.pitr},
	}}, nil
}

func (f *fakeSchema) UpdateContinuousBackups(context.Context, *ddb.UpdateContinuousBackupsInput, ...func(*ddb.Options)) (*ddb.UpdateContinuousBackupsOutput, error) {
	f.pitrUpdates++
	return &ddb.UpdateContinuousBackupsOutput{}, f.updatePITErr
}

func fastTableOptions() TableOptions {
	opts := DefaultTableOptions("gds")
	opts.PollInterval = time.Millisecond
	return opts
}

func TestCreateTableNew(t *testing.T) {
	f := &fakeSchema{statuses: []ddbtypes.TableStatus{ddbtypes.TableStatusCreating, ddbtypes.TableStatusCreating, ddbtypes.TableStatusActive}, pitr: ddbtypes.PointInTimeRecoveryStatusDisabled}
	created, err := CreateTable(t.Context(), f, fastTableOptions())
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, 3, f.describes, "waits until ACTIVE")
	assert.True(t, aws.ToBool(f.created.DeletionProtectionEnabled))
	assert.Equal(t, 1, f.ttlUpdates)
	assert.Equal(t, 1, f.pitrUpdates)

	created, err = CreateTable(t.Context(), &fakeSchema{statuses: []ddbtypes.TableStatus{ddbtypes.TableStatusActive}}, TableOptions{TableName: "gds"})
	require.NoError(t, err)
	assert.True(t, created, "TTL and PITR are optional")
}

func TestCreateTableExisting(t *testing.T) {
	f := &fakeSchema{
		createErr: &ddbtypes.ResourceInUseException{Message: aws.String("Table already exists")},
		statuses:  []ddbtypes.TableStatus{ddbtypes.TableStatusActive},
		ttl:       &ddbtypes.TimeToLiveDescription{AttributeName: aws.String(AttributeExpiresAt), TimeToLiveStatus: ddbtypes.TimeToLiveStatusEnabled},
		pitr:      ddbtypes.PointInTimeRecoveryStatusEnabled,
	}
	created, err := CreateTable(t.Context(), f, fastTableOptions())
	require.NoError(t, err)
	assert.False(t, created)
	assert.Zero(t, f.ttlUpdates, "TTL is already enabled")
	assert.Zero(t, f.pitrUpdates, "PITR is already enabled")
}

func TestCreateTableErrors(t *testing.T) {
	active := []ddbtypes.TableStatus{ddbtypes.TableStatusActive}
	boom := errors.New("boom")
	tests := []struct {
		name string
		f    *fakeSchema
		opts func(*TableOptions)
		want string
	}{
		{"no name", &fakeSchema{statuses: active}, func(o *TableOptions) { o.TableName = "" }, "TableName is required"},
		{"create", &fakeSchema{createErr: boom, statuses: active}, nil, "create table gds: boom"},
		{"describe", &fakeSchema{describeErr: boom}, nil, "describe table gds: boom"},
		{"never active", &fakeSchema{statuses: []ddbtypes.TableStatus{ddbtypes.TableStatusCreating}}, func(o *TableOptions) { o.WaitTimeout = 20 * time.Millisecond }, "is not ACTIVE"},
		{"key names", &fakeSchema{statuses: active, keySchema: []ddbtypes.KeySchemaElement{{AttributeName: aws.String("id"), KeyType: ddbtypes.KeyTypeHash}}}, nil, "another key schema"},
		{"key types", &fakeSchema{statuses: active, keyType: ddbtypes.ScalarAttributeTypeN}, nil, "another key schema"},
		{"describe ttl", &fakeSchema{statuses: active, ttlErr: boom}, nil, "describe TTL of gds: boom"},
		{"ttl elsewhere", &fakeSchema{statuses: active, ttl: &ddbtypes.TimeToLiveDescription{AttributeName: aws.String("ttl"), TimeToLiveStatus: ddbtypes.TimeToLiveStatusEnabled}}, nil, `has TTL on "ttl"`},
		{"update ttl", &fakeSchema{statuses: active, updateTTLErr: boom}, nil, "enable TTL on gds: boom"},
		{"describe pitr", &fakeSchema{statuses: active, pitrErr: boom}, nil, "describe continuous backups of gds: boom"},
		{"update pitr", &fakeSchema{statuses: active, updatePITErr: boom}, nil, "enable point-in-time recovery on gds: boom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := fastTableOptions()
			if tt.opts != nil {
				tt.opts(&opts)
			}
			_, err := CreateTable(t.Context(), tt.f, opts)
			assert.ErrorContains(t, err, tt.want)
		})
	}
}
