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

// Package runtime abstracts container runtime operations so NVSNAP works
// with containerd, CRI-O, or any future CRI-compliant runtime.
//
// The Runtime interface exposes only the operations the agent actually needs:
// finding containers by K8s labels / ID / name, listing containers, and
// resolving a container's rootfs path for CRIU --root.
//
// Two implementations exist:
//   - containerd: uses the containerd Go SDK against /run/containerd/containerd.sock
//   - crio:       uses the CRI gRPC protocol against /run/crio/crio.sock
//
// Runtime is auto-detected at startup by probing well-known socket paths.
package runtime

import (
	"context"
	"errors"
)

// ErrNotFound is returned when a container cannot be found by the given selector.
var ErrNotFound = errors.New("container not found")

// Type identifies the container runtime.
type Type string

// Recognized container runtime types.
const (
	TypeContainerd Type = "containerd"
	TypeCRIO       Type = "cri-o"
)

// ContainerInfo is the subset of container metadata NVSNAP needs.
// Field semantics are identical across runtimes.
type ContainerInfo struct {
	// ID is the runtime-assigned container ID (full, not truncated).
	ID string

	// Name is the K8s container name (from io.kubernetes.container.name label).
	Name string

	// Image is the image reference the container was started from.
	Image string

	// PID is the host PID of the container's main process (task init).
	PID uint32

	// RootFS is the absolute host path to the container's root filesystem.
	// Used for CRIU --root. Runtime-specific — containerd uses the task
	// bundle path, CRI-O uses containers/storage overlay merged path.
	RootFS string

	// Labels includes all container labels (K8s + runtime).
	Labels map[string]string
}

// Runtime is the container runtime abstraction.
type Runtime interface {
	// FindContainerByPod finds a container by K8s namespace+pod and optional
	// container name. If containerName is empty, the first non-pause container
	// in the pod is returned. Returns ErrNotFound if no match.
	FindContainerByPod(ctx context.Context, namespace, podName, containerName string) (*ContainerInfo, error)

	// FindContainerByID looks up a container by its full runtime ID.
	FindContainerByID(ctx context.Context, id string) (*ContainerInfo, error)

	// FindContainerByName looks up a container by its K8s container name or
	// runtime label. Returns the first match (undefined if multiple match).
	FindContainerByName(ctx context.Context, name string) (*ContainerInfo, error)

	// ListContainers returns all K8s containers known to the runtime.
	ListContainers(ctx context.Context) ([]ContainerInfo, error)

	// Type returns the runtime type (for logging / telemetry).
	Type() Type

	// Close releases runtime connections.
	Close() error
}
