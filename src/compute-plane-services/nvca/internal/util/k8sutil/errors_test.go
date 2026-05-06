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

package k8sutil

import (
	"context"
	"fmt"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestIsTransientK8sError(t *testing.T) {
	gr := schema.GroupResource{Group: "test", Resource: "things"}

	tests := []struct {
		name      string
		err       error
		transient bool
	}{
		{
			name:      "nil error",
			err:       nil,
			transient: false,
		},
		{
			name:      "timeout",
			err:       apierrors.NewTimeoutError("timed out", 5),
			transient: true,
		},
		{
			name:      "server timeout",
			err:       apierrors.NewServerTimeout(gr, "get", 5),
			transient: true,
		},
		{
			name:      "too many requests",
			err:       apierrors.NewTooManyRequests("slow down", 5),
			transient: true,
		},
		{
			name:      "service unavailable",
			err:       apierrors.NewServiceUnavailable("unavailable"),
			transient: true,
		},
		{
			name:      "internal error",
			err:       apierrors.NewInternalError(fmt.Errorf("internal")),
			transient: true,
		},
		{
			name:      "conflict",
			err:       apierrors.NewConflict(gr, "thing", fmt.Errorf("conflict")),
			transient: true,
		},
		{
			name:      "context deadline exceeded",
			err:       context.DeadlineExceeded,
			transient: true,
		},
		{
			name:      "context canceled",
			err:       context.Canceled,
			transient: true,
		},
		{
			name:      "wrapped context deadline",
			err:       fmt.Errorf("getting pod: %w", context.DeadlineExceeded),
			transient: true,
		},
		{
			name:      "wrapped timeout",
			err:       fmt.Errorf("getting pod: %w", apierrors.NewTimeoutError("timed out", 5)),
			transient: true,
		},
		{
			name:      "net error",
			err:       &net.OpError{Op: "dial", Err: fmt.Errorf("connection refused")},
			transient: true,
		},
		// Non-transient errors
		{
			name:      "forbidden",
			err:       apierrors.NewForbidden(gr, "thing", fmt.Errorf("forbidden")),
			transient: false,
		},
		{
			name:      "unauthorized",
			err:       apierrors.NewUnauthorized("unauthorized"),
			transient: false,
		},
		{
			name:      "not found",
			err:       apierrors.NewNotFound(gr, "thing"),
			transient: false,
		},
		{
			name:      "invalid",
			err:       apierrors.NewInvalid(schema.GroupKind{Group: "test", Kind: "Thing"}, "thing", nil),
			transient: false,
		},
		{
			name:      "gone",
			err:       apierrors.NewGone("gone"),
			transient: false,
		},
		{
			name:      "generic error",
			err:       fmt.Errorf("something unexpected"),
			transient: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.transient, IsTransientK8sError(tt.err))
		})
	}
}

func TestAnyNonTransientK8sError(t *testing.T) {
	gr := schema.GroupResource{Group: "test", Resource: "things"}

	t.Run("all transient returns nil", func(t *testing.T) {
		errs := []error{
			apierrors.NewTimeoutError("timed out", 5),
			apierrors.NewInternalError(fmt.Errorf("internal")),
			context.DeadlineExceeded,
		}
		assert.Nil(t, AnyNonTransientK8sError(errs))
	})

	t.Run("one non-transient returns it", func(t *testing.T) {
		forbidden := apierrors.NewForbidden(gr, "thing", fmt.Errorf("forbidden"))
		errs := []error{
			apierrors.NewTimeoutError("timed out", 5),
			forbidden,
			apierrors.NewInternalError(fmt.Errorf("internal")),
		}
		assert.Equal(t, forbidden, AnyNonTransientK8sError(errs))
	})

	t.Run("empty slice returns nil", func(t *testing.T) {
		assert.Nil(t, AnyNonTransientK8sError(nil))
		assert.Nil(t, AnyNonTransientK8sError([]error{}))
	})

	t.Run("nil errors in slice are skipped", func(t *testing.T) {
		errs := []error{nil, apierrors.NewTimeoutError("timed out", 5), nil}
		assert.Nil(t, AnyNonTransientK8sError(errs))
	})

	t.Run("wrapped non-transient is detected", func(t *testing.T) {
		wrapped := fmt.Errorf("cleanup failed: %w",
			apierrors.NewForbidden(gr, "thing", fmt.Errorf("forbidden")))
		errs := []error{
			apierrors.NewTimeoutError("timed out", 5),
			wrapped,
		}
		assert.Equal(t, wrapped, AnyNonTransientK8sError(errs))
	})
}
