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
	"strings"

	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/smithy-go"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store"
)

// Cancellation reason codes of TransactWriteItems.
const (
	reasonNone                   = "None"
	reasonConditionalCheckFailed = "ConditionalCheckFailed"
)

// mapError converts an SDK error into the store's error contract:
//
//   - context cancellation and deadlines are returned as they are;
//   - an item above the size limit and an LSI item collection above 10 GB
//     are store.ErrTooLarge;
//   - request validation and serialization errors are programming errors
//     and are returned wrapped, without a sentinel;
//   - everything else (throttling, capacity, internal errors, a missing
//     table, denied access, transport and DNS failures) is
//     store.ErrUnavailable, so the API reports STORE_UNAVAILABLE and
//     callers retry.
//
// Conditional check failures are handled by each operation before
// mapError is called.
func mapError(op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("dynamodb: %s: %w", op, err)
	}
	if errors.Is(err, store.ErrUnavailable) || errors.Is(err, store.ErrTooLarge) {
		return err
	}
	var collection *ddbtypes.ItemCollectionSizeLimitExceededException
	if errors.As(err, &collection) {
		return fmt.Errorf("dynamodb: %s: %w: %w", op, store.ErrTooLarge, err)
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "ValidationException":
			if isItemSizeError(apiErr.ErrorMessage()) {
				return fmt.Errorf("dynamodb: %s: %w: %w", op, store.ErrTooLarge, err)
			}
			return fmt.Errorf("dynamodb: %s: %w", op, err)
		case "SerializationException":
			return fmt.Errorf("dynamodb: %s: %w", op, err)
		}
	}
	return fmt.Errorf("dynamodb: %s: %w: %w", op, store.ErrUnavailable, err)
}

// isItemSizeError reports whether a ValidationException message is about
// the 400 KB item size limit, for example "Item size has exceeded the
// maximum allowed size" or "Item size to update has exceeded the maximum
// allowed size".
func isItemSizeError(msg string) bool {
	msg = strings.ToLower(msg)
	return strings.Contains(msg, "item size") && strings.Contains(msg, "exceeded")
}

// conditionFailed reports whether err is a failed condition of a single
// item write and returns the item the condition was evaluated against,
// nil when the item did not exist. The write must have asked for
// ReturnValuesOnConditionCheckFailure ALL_OLD.
func conditionFailed(err error) (map[string]ddbtypes.AttributeValue, bool) {
	var ccf *ddbtypes.ConditionalCheckFailedException
	if !errors.As(err, &ccf) {
		return nil, false
	}
	return ccf.Item, true
}

// transactionCanceled returns the cancellation reasons of a canceled
// TransactWriteItems call, one per request item in order.
func transactionCanceled(err error) ([]ddbtypes.CancellationReason, bool) {
	var tce *ddbtypes.TransactionCanceledException
	if !errors.As(err, &tce) {
		return nil, false
	}
	return tce.CancellationReasons, true
}

func reasonCode(reasons []ddbtypes.CancellationReason, i int) string {
	if i >= len(reasons) || reasons[i].Code == nil {
		return ""
	}
	return *reasons[i].Code
}

// errContention is returned when an optimistic write keeps losing to
// concurrent writers of the same item.
var errContention = fmt.Errorf("%w: too many concurrent writes to one item", store.ErrUnavailable)
