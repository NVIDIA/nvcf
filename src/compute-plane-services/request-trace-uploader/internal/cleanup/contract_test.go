// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cleanup_test

import (
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/internal/cleanup"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/internal/journalfs"
)

// The journal store is wired into cleanup without an adapter.
var _ cleanup.Journal = (*journalfs.Store)(nil)
