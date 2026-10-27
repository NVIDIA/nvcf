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

package client

import (
	"strconv"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/status"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api"
)

// errorInfo returns the GDS ErrorInfo detail of err, or nil.
func errorInfo(err error) *errdetails.ErrorInfo {
	st, ok := status.FromError(err)
	if !ok {
		return nil
	}
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok && info.GetDomain() == api.ErrorDomain {
			return info
		}
	}
	return nil
}

// Reason returns the GDS error reason of err (one of the api.Reason*
// constants), or "" when err carries none.
func Reason(err error) string {
	return errorInfo(err).GetReason()
}

// StaleGeneration reports whether err is a STALE_GENERATION error and
// returns the stored generation. Retry with a generation above it, for
// example with Generation.Observe then Generation.Next.
func StaleGeneration(err error) (current uint64, ok bool) {
	info := errorInfo(err)
	if info.GetReason() != api.ReasonStaleGeneration {
		return 0, false
	}
	current, _ = strconv.ParseUint(info.GetMetadata()[api.MetadataCurrentGeneration], 10, 64)
	return current, true
}

// IsLeaseEpochMismatch reports whether a heartbeat failed because its epoch
// is not current. The writer registers again and resends its state.
func IsLeaseEpochMismatch(err error) bool {
	return Reason(err) == api.ReasonLeaseEpochMismatch
}

// IsClusterNotRegistered reports whether a call failed because the cluster
// has no registration.
func IsClusterNotRegistered(err error) bool {
	return Reason(err) == api.ReasonClusterNotRegistered
}
