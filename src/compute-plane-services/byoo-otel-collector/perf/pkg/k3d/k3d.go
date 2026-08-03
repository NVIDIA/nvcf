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

// Package k3d provisions and tears down a dedicated local k3d cluster for the
// performance suite's managed "k3d" mode. It shells out to the k3d CLI; the
// command runner is injectable so the argument construction is unit-testable
// without a real k3d binary or Docker.
package k3d

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Runner executes a k3d subcommand and returns its combined output. It is a
// package variable so tests can substitute a fake without invoking k3d.
var Runner = execRunner

func execRunner(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "k3d", args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.Bytes(), err
}

// Options controls how the managed cluster is created.
type Options struct {
	// Name is the k3d cluster name. The kube context is "k3d-<Name>".
	Name string
	// Servers is the number of server (control-plane) nodes.
	Servers int
	// Agents is the number of agent (worker) nodes.
	Agents int
}

// DefaultOptions returns a single-node cluster suitable for the suite.
func DefaultOptions(name string) Options {
	return Options{Name: name, Servers: 1, Agents: 0}
}

// Cluster is a provisioned managed cluster.
type Cluster struct {
	Name    string
	Context string
}

// KubeContext returns the kube context name k3d assigns to a cluster.
func KubeContext(name string) string { return "k3d-" + name }

// EnsureInstalled verifies the k3d CLI is available.
func EnsureInstalled(ctx context.Context) error {
	if _, err := Runner(ctx, "version"); err != nil {
		return fmt.Errorf("k3d CLI not available (install k3d or use --mode remote): %w", err)
	}
	return nil
}

// Exists reports whether a cluster with the given name already exists.
func Exists(ctx context.Context, name string) (bool, error) {
	out, err := Runner(ctx, "cluster", "list", "--no-headers")
	if err != nil {
		return false, fmt.Errorf("list k3d clusters: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == name {
			return true, nil
		}
	}
	return false, nil
}

// createArgs builds the "k3d cluster create" arguments for the given options.
func createArgs(opts Options) []string {
	args := []string{
		"cluster", "create", opts.Name,
		"--servers", fmt.Sprintf("%d", maxInt(opts.Servers, 1)),
		"--agents", fmt.Sprintf("%d", maxInt(opts.Agents, 0)),
		"--wait",
	}
	return args
}

// Create provisions the cluster. If a cluster with the same name already exists
// it is reused rather than recreated, so reruns are cheap.
func Create(ctx context.Context, opts Options) (*Cluster, error) {
	if opts.Name == "" {
		return nil, fmt.Errorf("k3d cluster name is required")
	}
	exists, err := Exists(ctx, opts.Name)
	if err != nil {
		return nil, err
	}
	if !exists {
		if out, err := Runner(ctx, createArgs(opts)...); err != nil {
			return nil, fmt.Errorf("create k3d cluster %q: %w (%s)", opts.Name, err, strings.TrimSpace(string(out)))
		}
	}
	return &Cluster{Name: opts.Name, Context: KubeContext(opts.Name)}, nil
}

// Delete removes the cluster. Deleting a non-existent cluster is not an error.
func Delete(ctx context.Context, name string) error {
	exists, err := Exists(ctx, name)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if out, err := Runner(ctx, "cluster", "delete", name); err != nil {
		return fmt.Errorf("delete k3d cluster %q: %w (%s)", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ImportImages loads local Docker images into the cluster's nodes, so images
// that are not pullable from the cluster (e.g. a locally built collector) are
// available. It is a no-op when no images are given.
func ImportImages(ctx context.Context, name string, images ...string) error {
	if len(images) == 0 {
		return nil
	}
	args := append([]string{"image", "import", "--cluster", name}, images...)
	if out, err := Runner(ctx, args...); err != nil {
		return fmt.Errorf("import images into k3d cluster %q: %w (%s)", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
