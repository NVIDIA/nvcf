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

// Package containerd implements the runtime.Runtime interface against
// containerd via its native Go SDK.
package containerd

import (
	"context"
	"fmt"
	"strings"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/namespaces"
	"github.com/sirupsen/logrus"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/runtime"
)

const defaultNamespace = "k8s.io"

func init() {
	runtime.RegisterFactory(runtime.TypeContainerd, func(socket string, cfg runtime.Config) (runtime.Runtime, error) {
		return New(socket, cfg.Namespace, cfg.Logger)
	})
}

// Client wraps the containerd client and implements runtime.Runtime.
type Client struct {
	client *containerd.Client
	log    *logrus.Logger
	ns     string
}

// ContainerInfo is retained as an alias for backwards compatibility with
// existing call sites. New code should use runtime.ContainerInfo directly.
type ContainerInfo = runtime.ContainerInfo

// New creates a new containerd-backed runtime client.
func New(address, namespace string, log *logrus.Logger) (*Client, error) {
	if namespace == "" {
		namespace = defaultNamespace
	}
	client, err := containerd.New(address)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to containerd: %w", err)
	}

	// Verify connection
	ctx := namespaces.WithNamespace(context.Background(), namespace)
	_, err = client.Version(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get containerd version: %w", err)
	}

	return &Client{
		client: client,
		log:    log,
		ns:     namespace,
	}, nil
}

// Type returns the runtime type.
func (c *Client) Type() runtime.Type { return runtime.TypeContainerd }

// Close closes the containerd client.
func (c *Client) Close() error {
	return c.client.Close()
}

// FindContainerByPod finds a container by K8s pod name and namespace.
func (c *Client) FindContainerByPod(ctx context.Context, namespace, podName, containerName string) (*runtime.ContainerInfo, error) {
	ctx = namespaces.WithNamespace(ctx, c.ns)

	containers, err := c.client.Containers(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	for _, container := range containers {
		labels, err := container.Labels(ctx)
		if err != nil {
			continue
		}

		if labels["io.kubernetes.pod.namespace"] != namespace ||
			labels["io.kubernetes.pod.name"] != podName {
			continue
		}

		ctrName := labels["io.kubernetes.container.name"]
		if ctrName == "" || ctrName == "POD" {
			continue
		}
		if containerName != "" && ctrName != containerName {
			continue
		}

		info, err := c.containerInfo(ctx, container)
		if err != nil {
			c.log.WithError(err).Debug("skipping container")
			continue
		}
		return info, nil
	}

	return nil, fmt.Errorf("%w for pod %s/%s", runtime.ErrNotFound, namespace, podName)
}

// FindContainerByID finds a container by container ID.
func (c *Client) FindContainerByID(ctx context.Context, containerID string) (*runtime.ContainerInfo, error) {
	ctx = namespaces.WithNamespace(ctx, c.ns)

	container, err := c.client.LoadContainer(ctx, containerID)
	if err != nil {
		return nil, fmt.Errorf("%w: load container %s: %w", runtime.ErrNotFound, containerID, err)
	}
	return c.containerInfo(ctx, container)
}

// FindContainerByName resolves a container by name or ID.
func (c *Client) FindContainerByName(ctx context.Context, name string) (*runtime.ContainerInfo, error) {
	ctx = namespaces.WithNamespace(ctx, c.ns)

	containers, err := c.client.Containers(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}
	for _, container := range containers {
		if container.ID() == name {
			return c.containerInfo(ctx, container)
		}
		labels, err := container.Labels(ctx)
		if err != nil {
			continue
		}
		if labels["io.containerd.container.name"] == name || labels["name"] == name {
			return c.containerInfo(ctx, container)
		}
	}
	return nil, fmt.Errorf("%w for name %s", runtime.ErrNotFound, name)
}

// ListContainers lists all K8s containers.
func (c *Client) ListContainers(ctx context.Context) ([]runtime.ContainerInfo, error) {
	ctx = namespaces.WithNamespace(ctx, c.ns)

	containers, err := c.client.Containers(ctx)
	if err != nil {
		return nil, err
	}

	result := make([]runtime.ContainerInfo, 0, len(containers))
	for _, container := range containers {
		labels, _ := container.Labels(ctx)
		// Only return K8s containers
		if !strings.HasPrefix(labels["io.kubernetes.pod.name"], "") {
			continue
		}

		task, err := container.Task(ctx, nil)
		var pid uint32
		if err == nil {
			pid = task.Pid()
		}

		image, _ := container.Image(ctx)
		imageName := ""
		if image != nil {
			imageName = image.Name()
		}

		result = append(result, runtime.ContainerInfo{
			ID:     container.ID(),
			Name:   labels["io.kubernetes.container.name"],
			Image:  imageName,
			PID:    pid,
			Labels: labels,
		})
	}

	return result, nil
}

// GetImageSize returns the size of an image. Not part of the runtime.Runtime
// interface — exposed for callers that specifically need containerd data.
func (c *Client) GetImageSize(ctx context.Context, imageRef string) (int64, error) {
	ctx = namespaces.WithNamespace(ctx, c.ns)

	image, err := c.client.GetImage(ctx, imageRef)
	if err != nil {
		return 0, err
	}

	return image.Size(ctx)
}

func (c *Client) containerInfo(ctx context.Context, container containerd.Container) (*runtime.ContainerInfo, error) {
	task, err := container.Task(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get task: %w", err)
	}

	status, err := task.Status(ctx)
	if err != nil || status.Status != containerd.Running {
		return nil, fmt.Errorf("container task not running")
	}

	labels, err := container.Labels(ctx)
	if err != nil {
		labels = map[string]string{}
	}

	image, _ := container.Image(ctx)
	imageName := ""
	if image != nil {
		imageName = image.Name()
	}

	// Containerd's task bundle path: CRIU's --root needs this absolute path.
	// /proc/<pid>/root is a symlink CRIU can dereference, but the literal
	// bundle path is more portable across namespaces.
	rootfs := fmt.Sprintf("/run/containerd/io.containerd.runtime.v2.task/%s/%s/rootfs", c.ns, container.ID())

	return &runtime.ContainerInfo{
		ID:     container.ID(),
		Name:   labels["io.kubernetes.container.name"],
		Image:  imageName,
		PID:    task.Pid(),
		RootFS: rootfs,
		Labels: labels,
	}, nil
}
