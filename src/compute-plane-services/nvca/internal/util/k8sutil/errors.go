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

import "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8serr"

// IsTransientK8sError returns true for Kubernetes API errors that are expected
// to resolve on retry without human intervention: k8serr.IsTransient, which
// holds the classification so callers that must stay small need not import
// this package.
func IsTransientK8sError(err error) bool {
	return k8serr.IsTransient(err)
}

// AnyNonTransientK8sError returns the first non-transient error from the slice,
// or nil if all errors are transient. Useful for checking collected errors from
// batch operations (e.g., cleanup) where a single non-transient error should
// cause the entire batch to surface as a reconcile failure.
func AnyNonTransientK8sError(errs []error) error {
	for _, err := range errs {
		if err != nil && !IsTransientK8sError(err) {
			return err
		}
	}
	return nil
}
