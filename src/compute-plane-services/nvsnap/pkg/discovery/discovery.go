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

// Package discovery provides container information resolution via containerd.
package discovery

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/namespaces"
	specs "github.com/opencontainers/runtime-spec/specs-go"
)

const (
	// K8sNamespace is the containerd namespace used by Kubernetes
	K8sNamespace = "k8s.io"
	// DefaultSocket is the default containerd socket path
	DefaultSocket = "/run/containerd/containerd.sock"
)

// ContainerInfo holds resolved container information from containerd.
type ContainerInfo struct {
	ContainerID string
	PID         uint32
	RootFS      string
	BundlePath  string
	Image       string
	Spec        *specs.Spec
	Labels      map[string]string
}

// MountInfo represents a mount from the OCI spec.
type MountInfo struct {
	Destination string
	Source      string
	Type        string
	Options     []string
}

// Client wraps the containerd client for container discovery
type Client struct {
	client *containerd.Client
	socket string
}

// NewClient creates a new discovery client
func NewClient(socket string) (*Client, error) {
	if socket == "" {
		socket = DefaultSocket
	}

	client, err := containerd.New(socket)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to containerd at %s: %w", socket, err)
	}

	return &Client{
		client: client,
		socket: socket,
	}, nil
}

// Close closes the containerd client connection
func (c *Client) Close() error {
	if c.client != nil {
		return c.client.Close()
	}
	return nil
}

// ResolveContainer resolves a container ID to its process information.
func (c *Client) ResolveContainer(ctx context.Context, containerID string) (*ContainerInfo, error) {
	ctx = namespaces.WithNamespace(ctx, K8sNamespace)

	container, err := c.client.LoadContainer(ctx, containerID)
	if err != nil {
		return nil, fmt.Errorf("failed to load container %s: %w", containerID, err)
	}

	task, err := container.Task(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get task for container %s: %w", containerID, err)
	}

	pid := task.Pid()

	image, err := container.Image(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get image for container %s: %w", containerID, err)
	}

	spec, err := container.Spec(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get spec for container %s: %w", containerID, err)
	}

	labels, err := container.Labels(ctx)
	if err != nil {
		labels = make(map[string]string)
	}

	containerdRunRoot := os.Getenv("CONTAINERD_RUN_ROOT")
	if containerdRunRoot == "" {
		containerdRunRoot = "/run/containerd"
	}
	bundlePath := filepath.Join(containerdRunRoot, "io.containerd.runtime.v2.task", K8sNamespace, containerID)

	rootfsRelPath := "rootfs"
	if spec.Root != nil && spec.Root.Path != "" {
		rootfsRelPath = spec.Root.Path
	}

	var rootFS string
	if filepath.IsAbs(rootfsRelPath) {
		rootFS = rootfsRelPath
	} else {
		rootFS = filepath.Join(bundlePath, rootfsRelPath)
	}

	return &ContainerInfo{
		ContainerID: containerID,
		PID:         pid,
		RootFS:      rootFS,
		BundlePath:  bundlePath,
		Image:       image.Name(),
		Spec:        spec,
		Labels:      labels,
	}, nil
}

// GetMounts returns the mount configuration from the OCI spec.
func (info *ContainerInfo) GetMounts() []MountInfo {
	if info.Spec == nil || info.Spec.Mounts == nil {
		return nil
	}

	mounts := make([]MountInfo, len(info.Spec.Mounts))
	for i, m := range info.Spec.Mounts {
		mounts[i] = MountInfo{
			Destination: m.Destination,
			Source:      m.Source,
			Type:        m.Type,
			Options:     m.Options,
		}
	}
	return mounts
}

// GetMaskedPaths returns the masked paths from the OCI spec.
func (info *ContainerInfo) GetMaskedPaths() []string {
	if info.Spec == nil || info.Spec.Linux == nil {
		return nil
	}
	return info.Spec.Linux.MaskedPaths
}

// GetReadonlyPaths returns the readonly paths from the OCI spec.
func (info *ContainerInfo) GetReadonlyPaths() []string {
	if info.Spec == nil || info.Spec.Linux == nil {
		return nil
	}
	return info.Spec.Linux.ReadonlyPaths
}

// ListContainers lists all containers in the K8s namespace
func (c *Client) ListContainers(ctx context.Context) ([]string, error) {
	ctx = namespaces.WithNamespace(ctx, K8sNamespace)

	containers, err := c.client.Containers(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	ids := make([]string, len(containers))
	for i, container := range containers {
		ids[i] = container.ID()
	}

	return ids, nil
}
