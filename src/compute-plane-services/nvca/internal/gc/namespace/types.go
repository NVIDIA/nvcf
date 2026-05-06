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

package namespace

import (
	"context"

	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
)

// ICMSRequestGetter defines the interface for getting ICMSRequest CRs.
// Keeping this local avoids a cyclic dependency with the storageclass package.
type ICMSRequestGetter interface {
	GetICMSRequest(ctx context.Context, name string) (*nvcav2beta1.ICMSRequest, error)
}

const (
	// MaxCleanupWorkers defines the maximum number of parallel cleanup operations.
	// This mirrors the value used by the storageclass cleaner to stay consistent.
	MaxCleanupWorkers = 5
)
