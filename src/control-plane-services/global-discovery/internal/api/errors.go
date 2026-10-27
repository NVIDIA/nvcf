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
	"strconv"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/protoadapt"

	gds "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/view"
)

// gdsError returns a google.rpc.Status error with an ErrorInfo detail in the
// gds.nvidia.com domain followed by details.
func gdsError(code codes.Code, reason, msg string, metadata map[string]string, details ...protoadapt.MessageV1) error {
	st := status.New(code, msg)
	all := append([]protoadapt.MessageV1{&errdetails.ErrorInfo{Reason: reason, Domain: gds.ErrorDomain, Metadata: metadata}}, details...)
	withDetails, err := st.WithDetails(all...)
	if err != nil {
		return st.Err()
	}
	return withDetails.Err()
}

// invalidArgument returns INVALID_ARGUMENT with a BadRequest naming the
// fields.
func invalidArgument(violations []*errdetails.BadRequest_FieldViolation) error {
	msg := fmt.Sprintf("invalid request: %s: %s", violations[0].GetField(), violations[0].GetDescription())
	if n := len(violations) - 1; n > 0 {
		msg += fmt.Sprintf(" (and %d more)", n)
	}
	return gdsError(codes.InvalidArgument, gds.ReasonInvalidArgument, msg, nil, &errdetails.BadRequest{FieldViolations: violations})
}

func tooManyEndpoints(n int) error {
	msg := fmt.Sprintf("endpoint set has %d endpoints; at most %d are allowed", n, gds.MaxEndpointsPerExport)
	return gdsError(codes.InvalidArgument, gds.ReasonTooManyEndpoints, msg, nil, &errdetails.BadRequest{
		FieldViolations: []*errdetails.BadRequest_FieldViolation{{Field: "endpoints", Description: msg}},
	})
}

// staleGeneration returns FAILED_PRECONDITION STALE_GENERATION with the
// stored generation in the ErrorInfo metadata and a PreconditionFailure.
func staleGeneration(what string, attempted, current uint64) error {
	msg := fmt.Sprintf("%s generation %d does not exceed stored generation %d", what, attempted, current)
	return gdsError(codes.FailedPrecondition, gds.ReasonStaleGeneration, msg,
		map[string]string{gds.MetadataCurrentGeneration: strconv.FormatUint(current, 10)},
		&errdetails.PreconditionFailure{Violations: []*errdetails.PreconditionFailure_Violation{{
			Type:        gds.ReasonStaleGeneration,
			Subject:     "generation",
			Description: fmt.Sprintf("current generation is %d", current),
		}}})
}

func clusterNotRegistered(code codes.Code, key store.ClusterKey) error {
	return gdsError(code, gds.ReasonClusterNotRegistered,
		fmt.Sprintf("cluster %q is not registered in clusterset %q", key.Cluster, key.ClusterSet), nil)
}

func notFound(format string, args ...any) error {
	return gdsError(codes.NotFound, gds.ReasonNotFound, fmt.Sprintf(format, args...), nil)
}

// storeError maps a store error that the caller did not handle itself.
func storeError(err error, op string) error {
	var stale store.ErrStaleGeneration
	switch {
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, op+": "+err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, op+": "+err.Error())
	case errors.Is(err, store.ErrUnavailable):
		return gdsError(codes.Unavailable, gds.ReasonStoreUnavailable, op+": store unavailable", nil)
	case errors.Is(err, store.ErrLeaseEpochMismatch):
		return gdsError(codes.Aborted, gds.ReasonLeaseEpochMismatch, op+": lease epoch is not current; register again", nil)
	case errors.Is(err, store.ErrPoolNameTaken):
		return gdsError(codes.AlreadyExists, gds.ReasonPoolNameTaken, op+": another pool of the cluster has the name", nil)
	case errors.Is(err, store.ErrTooLarge):
		return gdsError(codes.InvalidArgument, gds.ReasonTooManyEndpoints, op+": record too large", nil)
	case errors.As(err, &stale):
		return staleGeneration(op, 0, stale.Current)
	}
	return gdsError(codes.Internal, gds.ReasonInternal, op+": "+err.Error(), nil)
}

// watchError maps the error that ended a watch.
func watchError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, view.ErrTooManyWatchers):
		return gdsError(codes.ResourceExhausted, gds.ReasonTooManyWatchers, "the replica serves the maximum number of watches; retry later or elsewhere", nil)
	case errors.Is(err, view.ErrShuttingDown):
		return gdsError(codes.Unavailable, gds.ReasonShuttingDown, "the replica is shutting down; reconnect", nil)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return status.FromContextError(err).Err()
	}
	if _, ok := status.FromError(err); ok {
		return err
	}
	return gdsError(codes.Internal, gds.ReasonInternal, "watch: "+err.Error(), nil)
}
