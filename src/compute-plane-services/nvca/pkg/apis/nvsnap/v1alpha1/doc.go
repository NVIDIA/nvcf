/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package v1alpha1 contains the per-cluster NvSnap state CRDs read and
// written by NVCA's checkpoint/restore reconciler (Hook B) and stamped
// onto restoring pods by NVCA's pod-creation hook (Hook A).
//
// The canonical authority for "which function-version has a checkpoint
// hash" is NGC (the global function-version object). This CRD tracks
// per-cluster operational state — local cache status, retry counters,
// last-error, opt-outs — that NGC shouldn't carry. See
// docs/users/nvsnap/NVSNAP-INTEGRATION-DESIGN.md §"Where exactly does the
// hash live" for the split-storage rationale.
//
// +k8s:deepcopy-gen=package
// +k8s:defaulter-gen=TypeMeta
// +groupName=nvsnap.nvcf.nvidia.io
package v1alpha1
