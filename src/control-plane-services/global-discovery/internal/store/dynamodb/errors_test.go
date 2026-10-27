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
	"errors"
	"fmt"
	"net"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store"
)

func TestMapError(t *testing.T) {
	opErr := func(err error) error {
		return &smithy.OperationError{ServiceID: "DynamoDB", OperationName: "PutItem", Err: err}
	}
	tests := []struct {
		name string
		err  error
		is   []error
		not  []error
	}{
		{"canceled", opErr(context.Canceled), []error{context.Canceled}, []error{store.ErrUnavailable}},
		{"deadline", opErr(context.DeadlineExceeded), []error{context.DeadlineExceeded}, []error{store.ErrUnavailable}},
		{"already unavailable", fmt.Errorf("x: %w", store.ErrUnavailable), []error{store.ErrUnavailable}, nil},
		{"already too large", store.ErrTooLarge, []error{store.ErrTooLarge}, nil},
		{"item collection", opErr(&ddbtypes.ItemCollectionSizeLimitExceededException{Message: aws.String("10 GB")}), []error{store.ErrTooLarge}, []error{store.ErrUnavailable}},
		{"item size", opErr(apiError("ValidationException", "Item size has exceeded the maximum allowed size")), []error{store.ErrTooLarge}, nil},
		{"update size", opErr(apiError("ValidationException", "Item size to update has exceeded the maximum allowed size")), []error{store.ErrTooLarge}, nil},
		{"validation", opErr(apiError("ValidationException", "One or more parameter values were invalid")), nil, []error{store.ErrUnavailable, store.ErrTooLarge}},
		{"serialization", opErr(apiError("SerializationException", "bad")), nil, []error{store.ErrUnavailable}},
		{"throttled", opErr(&ddbtypes.ProvisionedThroughputExceededException{Message: aws.String("slow down")}), []error{store.ErrUnavailable}, nil},
		{"request limit", opErr(&ddbtypes.RequestLimitExceeded{Message: aws.String("slow down")}), []error{store.ErrUnavailable}, nil},
		{"throttling", opErr(apiError("ThrottlingException", "Rate exceeded")), []error{store.ErrUnavailable}, nil},
		{"internal", opErr(&ddbtypes.InternalServerError{Message: aws.String("oops")}), []error{store.ErrUnavailable}, nil},
		{"missing table", opErr(&ddbtypes.ResourceNotFoundException{Message: aws.String("no table")}), []error{store.ErrUnavailable}, nil},
		{"access denied", opErr(apiError("AccessDeniedException", "no")), []error{store.ErrUnavailable}, nil},
		{"transport", opErr(&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}), []error{store.ErrUnavailable}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mapError("PutItem", tt.err)
			assert.ErrorIs(t, got, tt.err, "the cause stays in the chain")
			for _, want := range tt.is {
				assert.ErrorIs(t, got, want)
			}
			for _, not := range tt.not {
				assert.NotErrorIs(t, got, not)
			}
		})
	}
	assert.NoError(t, mapError("PutItem", nil))
}

func TestConditionHelpers(t *testing.T) {
	old := map[string]ddbtypes.AttributeValue{attrGen: numU(3)}
	got, ok := conditionFailed(fmt.Errorf("wrapped: %w", &ddbtypes.ConditionalCheckFailedException{Item: old}))
	assert.True(t, ok)
	assert.Equal(t, old, got)
	_, ok = conditionFailed(errors.New("other"))
	assert.False(t, ok)

	reasons := []ddbtypes.CancellationReason{{Code: aws.String(reasonNone)}, {Code: aws.String("TransactionConflict")}, {}}
	got2, ok := transactionCanceled(&ddbtypes.TransactionCanceledException{CancellationReasons: reasons})
	assert.True(t, ok)
	assert.Len(t, got2, 3)
	_, ok = transactionCanceled(errors.New("other"))
	assert.False(t, ok)

	assert.Equal(t, reasonNone, reasonCode(reasons, 0))
	assert.Equal(t, "", reasonCode(reasons, 2))
	assert.Equal(t, "", reasonCode(reasons, 7))
	assert.True(t, onlyConflicts(reasons))
	assert.False(t, onlyConflicts(reasons[:1]), "no conflict at all")
	assert.False(t, onlyConflicts([]ddbtypes.CancellationReason{{Code: aws.String("TransactionConflict")}, {Code: aws.String("ThrottlingError")}}))

	assert.ErrorIs(t, errContention, store.ErrUnavailable)
}
