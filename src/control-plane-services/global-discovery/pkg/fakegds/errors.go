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

package fakegds

import (
	"fmt"
	"strconv"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/protoadapt"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api"
)

// gdsError returns a status error carrying an ErrorInfo with the GDS domain
// and reason, followed by extra details.
func gdsError(code codes.Code, reason, msg string, metadata map[string]string, extra ...protoadapt.MessageV1) error {
	st := status.New(code, msg)
	details := append([]protoadapt.MessageV1{&errdetails.ErrorInfo{
		Reason:   reason,
		Domain:   api.ErrorDomain,
		Metadata: metadata,
	}}, extra...)
	if withDetails, err := st.WithDetails(details...); err == nil {
		st = withDetails
	}
	return st.Err()
}

func errInvalid(field, description string) error {
	st := status.New(codes.InvalidArgument, fmt.Sprintf("invalid %s: %s", field, description))
	if withDetails, err := st.WithDetails(&errdetails.BadRequest{
		FieldViolations: []*errdetails.BadRequest_FieldViolation{{Field: field, Description: description}},
	}); err == nil {
		st = withDetails
	}
	return st.Err()
}

func errStaleGeneration(what string, got, current uint64) error {
	cur := strconv.FormatUint(current, 10)
	return gdsError(codes.FailedPrecondition, api.ReasonStaleGeneration,
		fmt.Sprintf("%s generation %d does not exceed stored generation %d", what, got, current),
		map[string]string{api.MetadataCurrentGeneration: cur},
		&errdetails.PreconditionFailure{Violations: []*errdetails.PreconditionFailure_Violation{{
			Type:        api.ReasonStaleGeneration,
			Subject:     "generation",
			Description: "current generation " + cur,
		}}},
	)
}

func errEpochMismatch(what string, got, current uint64) error {
	return gdsError(codes.Aborted, api.ReasonLeaseEpochMismatch,
		fmt.Sprintf("%s lease epoch %d is not the current epoch %d; register again", what, got, current), nil)
}

func errClusterNotRegistered(code codes.Code, clusterset, cluster string) error {
	return gdsError(code, api.ReasonClusterNotRegistered,
		fmt.Sprintf("cluster %q is not registered in clusterset %q", cluster, clusterset), nil)
}

func errPoolNameTaken(cluster, name, poolID string) error {
	return gdsError(codes.AlreadyExists, api.ReasonPoolNameTaken,
		fmt.Sprintf("pool name %q in cluster %q is taken by pool %s", name, cluster, poolID), nil)
}

func errTooManyEndpoints(n int) error {
	return gdsError(codes.InvalidArgument, api.ReasonTooManyEndpoints,
		fmt.Sprintf("%d endpoints exceed the limit of %d", n, api.MaxEndpointsPerExport), nil)
}

func errShuttingDown() error {
	return gdsError(codes.Unavailable, api.ReasonShuttingDown, "fakegds: watch closed", nil)
}

func errNotFound(format string, args ...any) error {
	return status.Errorf(codes.NotFound, format, args...)
}

// require returns INVALID_ARGUMENT for the first empty value, given as
// alternating field names and values.
func require(fieldsAndValues ...string) error {
	for i := 0; i+1 < len(fieldsAndValues); i += 2 {
		if fieldsAndValues[i+1] == "" {
			return errInvalid(fieldsAndValues[i], "required")
		}
	}
	return nil
}
