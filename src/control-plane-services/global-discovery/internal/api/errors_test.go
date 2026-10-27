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

package api

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gds "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api"
	gdsv1 "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/view"
)

func TestStoreErrorMapping(t *testing.T) {
	cases := []struct {
		err    error
		code   codes.Code
		reason string
	}{
		{store.ErrUnavailable, codes.Unavailable, gds.ReasonStoreUnavailable},
		{fmt.Errorf("wrapped: %w", store.ErrLeaseEpochMismatch), codes.Aborted, gds.ReasonLeaseEpochMismatch},
		{store.ErrPoolNameTaken, codes.AlreadyExists, gds.ReasonPoolNameTaken},
		{store.ErrTooLarge, codes.InvalidArgument, gds.ReasonTooManyEndpoints},
		{store.ErrStaleGeneration{Current: 3}, codes.FailedPrecondition, gds.ReasonStaleGeneration},
		{errors.New("boom"), codes.Internal, gds.ReasonInternal},
	}
	for _, tc := range cases {
		requireError(t, storeError(tc.err, "op"), tc.code, tc.reason)
	}
	assert.Equal(t, codes.Canceled, status.Code(storeError(context.Canceled, "op")))
	assert.Equal(t, codes.DeadlineExceeded, status.Code(storeError(context.DeadlineExceeded, "op")))
}

func TestWatchErrorMapping(t *testing.T) {
	assert.NoError(t, watchError(nil))
	requireError(t, watchError(view.ErrTooManyWatchers), codes.ResourceExhausted, gds.ReasonTooManyWatchers)
	requireError(t, watchError(view.ErrShuttingDown), codes.Unavailable, gds.ReasonShuttingDown)
	assert.Equal(t, codes.Canceled, status.Code(watchError(context.Canceled)))
	assert.Equal(t, codes.Unavailable, status.Code(watchError(status.Error(codes.Unavailable, "transport"))))
	requireError(t, watchError(errors.New("boom")), codes.Internal, gds.ReasonInternal)
}

func TestInvalidArgumentMessage(t *testing.T) {
	var v validator
	v.add("a", "is bad")
	v.add("b", "is bad")
	st, _ := status.FromError(v.err())
	assert.Equal(t, "invalid request: a: is bad (and 1 more)", st.Message())
}

func TestAddressType(t *testing.T) {
	assert.Equal(t, gdsv1.AddressType_ADDRESS_TYPE_UNSPECIFIED, addressType(nil))
	assert.Equal(t, gdsv1.AddressType_ADDRESS_TYPE_IPV4, addressType([]string{"10.0.0.1"}))
	assert.Equal(t, gdsv1.AddressType_ADDRESS_TYPE_IPV6, addressType([]string{"fd00::1"}))
	assert.Equal(t, gdsv1.AddressType_ADDRESS_TYPE_FQDN, addressType([]string{"pod.example"}))
}

func TestGeneration(t *testing.T) {
	gen, assigned := generation(7, 3, t0)
	assert.Equal(t, uint64(7), gen)
	assert.False(t, assigned)
	gen, assigned = generation(0, 3, t0)
	assert.Equal(t, uint64(t0.UnixMilli()), gen)
	assert.True(t, assigned)
}
