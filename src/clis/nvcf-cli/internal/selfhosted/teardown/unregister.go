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

package teardown

import (
	"context"
	"strings"
)

// ClusterDeleter is the SIS API subset that Unregister needs. The interface
// lives in the teardown package; the orchestrator wires whichever production
// SIS client implements it. Tests use a fake.
type ClusterDeleter interface {
	DeleteCluster(ctx context.Context, sisURL, ncaID, clusterID string) error
}

// Unregister deletes the cluster's SIS row. HTTP 404 / "not found" errors are
// treated as success (idempotent: the row is already gone). 5xx errors
// propagate.
func Unregister(ctx context.Context, sis ClusterDeleter, sisURL, ncaID, clusterID string) error {
	if err := sis.DeleteCluster(ctx, sisURL, ncaID, clusterID); err != nil {
		if isNotFound(err) {
			return nil
		}
		return err
	}
	return nil
}

// isNotFound reports whether err looks like an HTTP 404 / not-found response.
// Checks are string-based because the production SIS client wraps errors as
// plain fmt.Errorf strings; if/when the client grows typed errors this can be
// replaced with errors.As.
func isNotFound(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "not found") || strings.Contains(msg, "404")
}
