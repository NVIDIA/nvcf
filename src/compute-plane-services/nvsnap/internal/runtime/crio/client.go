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

// Package crio implements runtime.Runtime for CRI-O via the standardized
// CRI gRPC protocol (k8s.io/cri-api).
//
// CRI-O does not ship a native Go client library, so we use CRI — the same
// protocol the kubelet speaks to CRI-O. This gives us container listing,
// inspection, and status without depending on CRI-O's internal packages.
//
// For rootfs resolution, CRI's ContainerStatus only exposes the container's
// own view. We resolve the host-accessible rootfs by reading the symlink at
// /proc/<pid>/root, which dereferences to the real path in the host mount
// namespace. This works uniformly for CRI-O, containerd, and any other
// runtime that mounts container rootfs on the host.
package crio

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/runtime"
)

// Client implements runtime.Runtime against a CRI-O socket.
type Client struct {
	conn    *grpc.ClientConn
	runtime runtimeapi.RuntimeServiceClient
	log     *logrus.Logger
}

func init() {
	runtime.RegisterFactory(runtime.TypeCRIO, func(socket string, cfg runtime.Config) (runtime.Runtime, error) {
		return New(socket, cfg.Logger)
	})
}

// New creates a new CRI-O runtime client.
// address is the path to the CRI-O socket (e.g., /run/crio/crio.sock).
func New(address string, log *logrus.Logger) (*Client, error) {
	if address == "" {
		address = "/run/crio/crio.sock"
	}

	// CRI is always over a Unix socket.
	conn, err := grpc.NewClient("unix://"+address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to dial CRI-O socket %s: %w", address, err)
	}

	rtClient := runtimeapi.NewRuntimeServiceClient(conn)

	// Verify the runtime is responsive.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	vresp, err := rtClient.Version(ctx, &runtimeapi.VersionRequest{})
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("CRI-O version check failed: %w", err)
	}
	log.WithFields(logrus.Fields{
		"runtime":    vresp.RuntimeName,
		"version":    vresp.RuntimeVersion,
		"apiVersion": vresp.RuntimeApiVersion,
	}).Info("connected to CRI-O")

	return &Client{conn: conn, runtime: rtClient, log: log}, nil
}

// Type returns the runtime type.
func (c *Client) Type() runtime.Type { return runtime.TypeCRIO }

// Close releases the gRPC connection.
func (c *Client) Close() error {
	return c.conn.Close()
}

// FindContainerByPod finds a container by K8s namespace + pod + container name.
func (c *Client) FindContainerByPod(ctx context.Context, namespace, podName, containerName string) (*runtime.ContainerInfo, error) {
	filter := &runtimeapi.ContainerFilter{
		State: &runtimeapi.ContainerStateValue{State: runtimeapi.ContainerState_CONTAINER_RUNNING},
		LabelSelector: map[string]string{
			"io.kubernetes.pod.namespace": namespace,
			"io.kubernetes.pod.name":      podName,
		},
	}
	resp, err := c.runtime.ListContainers(ctx, &runtimeapi.ListContainersRequest{Filter: filter})
	if err != nil {
		return nil, fmt.Errorf("ListContainers: %w", err)
	}

	for _, ctr := range resp.Containers {
		ctrName := ctr.Labels["io.kubernetes.container.name"]
		if ctrName == "" || ctrName == "POD" {
			continue
		}
		if containerName != "" && ctrName != containerName {
			continue
		}
		return c.inspect(ctx, ctr.Id)
	}

	return nil, fmt.Errorf("%w for pod %s/%s", runtime.ErrNotFound, namespace, podName)
}

// FindContainerByID looks up a container by its full runtime ID.
func (c *Client) FindContainerByID(ctx context.Context, id string) (*runtime.ContainerInfo, error) {
	return c.inspect(ctx, id)
}

// FindContainerByName resolves a container by K8s container name label.
func (c *Client) FindContainerByName(ctx context.Context, name string) (*runtime.ContainerInfo, error) {
	filter := &runtimeapi.ContainerFilter{
		State: &runtimeapi.ContainerStateValue{State: runtimeapi.ContainerState_CONTAINER_RUNNING},
	}
	resp, err := c.runtime.ListContainers(ctx, &runtimeapi.ListContainersRequest{Filter: filter})
	if err != nil {
		return nil, fmt.Errorf("ListContainers: %w", err)
	}
	for _, ctr := range resp.Containers {
		if ctr.Id == name || ctr.Metadata != nil && ctr.Metadata.Name == name {
			return c.inspect(ctx, ctr.Id)
		}
		if ctr.Labels["io.kubernetes.container.name"] == name {
			return c.inspect(ctx, ctr.Id)
		}
	}
	return nil, fmt.Errorf("%w for name %s", runtime.ErrNotFound, name)
}

// ListContainers returns all running K8s containers.
func (c *Client) ListContainers(ctx context.Context) ([]runtime.ContainerInfo, error) {
	filter := &runtimeapi.ContainerFilter{
		State: &runtimeapi.ContainerStateValue{State: runtimeapi.ContainerState_CONTAINER_RUNNING},
	}
	resp, err := c.runtime.ListContainers(ctx, &runtimeapi.ListContainersRequest{Filter: filter})
	if err != nil {
		return nil, err
	}

	result := make([]runtime.ContainerInfo, 0, len(resp.Containers))
	for _, ctr := range resp.Containers {
		// Only K8s containers (have the pod labels).
		if ctr.Labels["io.kubernetes.pod.name"] == "" {
			continue
		}
		// Skip pause containers.
		if ctr.Labels["io.kubernetes.container.name"] == "POD" {
			continue
		}

		// Inspect for PID + rootfs — a ListContainers response lacks both.
		info, err := c.inspect(ctx, ctr.Id)
		if err != nil {
			c.log.WithError(err).WithField("id", ctr.Id).Debug("inspect failed, skipping")
			continue
		}
		result = append(result, *info)
	}
	return result, nil
}

// inspect fetches full ContainerStatus + runtime info for one container.
func (c *Client) inspect(ctx context.Context, id string) (*runtime.ContainerInfo, error) {
	resp, err := c.runtime.ContainerStatus(ctx, &runtimeapi.ContainerStatusRequest{
		ContainerId: id,
		Verbose:     true, // request runtime's "info" extension for PID
	})
	if err != nil {
		return nil, fmt.Errorf("%w: ContainerStatus %s: %w", runtime.ErrNotFound, id, err)
	}

	status := resp.Status
	if status == nil {
		return nil, fmt.Errorf("%w: empty ContainerStatus for %s", runtime.ErrNotFound, id)
	}

	pid, err := extractPID(resp.Info)
	if err != nil {
		return nil, fmt.Errorf("failed to extract PID for container %s: %w", id, err)
	}

	image := ""
	if status.Image != nil {
		image = status.Image.Image
	}
	if status.ImageRef != "" {
		image = status.ImageRef
	}

	// Host-accessible rootfs: dereference /proc/<pid>/root symlink.
	rootfs, err := resolveRootfs(pid)
	if err != nil {
		c.log.WithError(err).WithField("pid", pid).Warn("rootfs resolution failed, using /proc symlink")
		rootfs = fmt.Sprintf("/proc/%d/root", pid)
	}

	return &runtime.ContainerInfo{
		ID:     status.Id,
		Name:   status.Labels["io.kubernetes.container.name"],
		Image:  image,
		PID:    uint32(pid), //nolint:gosec // bounded: extractPID guarantees pid > 0 and a Linux PID fits in uint32
		RootFS: rootfs,
		Labels: status.Labels,
	}, nil
}

// extractPID parses the "info" blob from ContainerStatus(verbose=true).
// CRI-O returns a JSON object with a top-level "pid" field containing the
// container's init PID.
func extractPID(info map[string]string) (int, error) {
	raw, ok := info["info"]
	if !ok {
		return 0, fmt.Errorf("ContainerStatus info missing 'info' key (runtime may not support verbose)")
	}
	var parsed struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return 0, fmt.Errorf("parse info JSON: %w", err)
	}
	if parsed.PID <= 0 {
		return 0, fmt.Errorf("info PID is %d (container may have exited)", parsed.PID)
	}
	return parsed.PID, nil
}

// resolveRootfs returns /proc/<pid>/root so the caller can bind-mount it
// into a real directory before passing to CRIU.
//
// Using the /proc magic symlink (deref'd via bind-mount in the caller)
// rather than the raw overlay merged path has two advantages:
//
//  1. It captures the container's FULL mount namespace view, including
//     runtime-added bind mounts (nvidia-container-toolkit injects firmware
//     files, device bindings, NCCL topology files, etc. at pod-start time —
//     these aren't in the overlay merged/ directory).
//  2. It's runtime-agnostic: works for overlay, fuse-overlayfs, any storage
//     driver without needing the driver-specific path layout.
//
// The caller (internal/agent/restore.go) detects the /proc prefix and does
// the bind-mount via util-linux `mount --bind`.
func resolveRootfs(pid int) (string, error) {
	path := fmt.Sprintf("/proc/%d/root", pid)
	if _, err := os.Lstat(path); err != nil {
		return "", fmt.Errorf("stat %s: %w", path, err)
	}
	return path, nil
}

// Unused imports guard — strings.Fields was used by the old mountinfo parser.
var _ = strings.Fields
