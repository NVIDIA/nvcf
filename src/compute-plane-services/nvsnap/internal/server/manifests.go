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

// Package server provides the workload registry — it loads the demo
// workload catalog from a filesystem directory (default
// /etc/nvsnap/workloads). Each
// workload is a pair of files:
//
//	<id>.yaml          source Pod (has nvsnap.io/* annotations on metadata)
//	<id>-restore.yaml  restore Pod (CRIU path: same image, /nvsnap/restore-entrypoint;
//	                                rootfs path: customer-shape with
//	                                nvsnap.io/restore-from annotation placeholder)
//
// The nvsnap-server image's Dockerfile copies deploy/k8s/workloads/ into
// /etc/nvsnap/workloads/ so the image bundles the canonical demo set.
// Operators can mount a different directory at /etc/nvsnap/workloads (or
// set NVSNAP_WORKLOADS_DIR) to swap in a custom catalog without rebuilding.
//
// The metadata that drives the UI (name, model, gpu count, capture
// path, expected checkpoint size) lives in annotations on the source
// Pod's metadata, so it travels with the manifest and survives kubectl
// apply -f workloads/<id>.yaml. Required annotations:
//
//	nvsnap.io/demo-name  Human display name shown on the workload tile
//	nvsnap.io/model      Inference API model id (OpenAI-compatible)
//	nvsnap.io/port       Inference port (8000 for vLLM/TRT-LLM/NIM, 30000 for SGLang)
//	nvsnap.io/gpus       GPU count (1, 2, 4, ...)
//	nvsnap.io/path       "criu" or "rootfs" — selects the capture flow
//
// Optional:
//
//	nvsnap.io/desc       Subtitle on the tile
//	nvsnap.io/ckpt-size  Display-only ("30 GB"), no semantic meaning
package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	sigsyaml "sigs.k8s.io/yaml"
)

// DefaultWorkloadsDir is the path the nvsnap-server reads the workload
// catalog from when NVSNAP_WORKLOADS_DIR is unset. Matches the location
// the nvsnap-server Dockerfile copies into.
const DefaultWorkloadsDir = "/etc/nvsnap/workloads"

// Annotation keys driving the WorkloadConfig parse. Kept as constants
// so the writer (deploy/k8s/workloads/*.yaml authors) and the reader
// (here) can't drift.
const (
	annDemoName = "nvsnap.io/demo-name"
	annDesc     = "nvsnap.io/desc"
	annModel    = "nvsnap.io/model"
	annPort     = "nvsnap.io/port"
	annGPUs     = "nvsnap.io/gpus"
	annPath     = "nvsnap.io/path"
	annCkptSize = "nvsnap.io/ckpt-size"
)

// CapturePath identifies which checkpoint/restore mechanism a workload
// uses. Single-GPU demos use CRIU + cuda-checkpoint; multi-GPU demos
// use the rootfs-only path with webhook-injected restore mounts.
type CapturePath string

// Capture path identifiers.
const (
	CapturePathCRIU   CapturePath = "criu"
	CapturePathRootfs CapturePath = "rootfs"
)

// WorkloadConfig is the runtime view of one demo workload. Sourced
// from a Pod's metadata annotations + its file contents. Same shape
// as before so callers in internal/server/demo.go are unchanged.
type WorkloadConfig struct {
	ID              string      // filename stem (e.g., "vllm-small")
	DemoName        string      // "vLLM", "NIM", "SGLang", "TensorRT-LLM"
	Desc            string      // tile subtitle
	Port            int         // inference port
	PodName         string      // .metadata.name from the source Pod
	RestorePodName  string      // <PodName>-restored
	Model           string      // inference model id
	GPUs            int         // GPU count requested
	Path            CapturePath // criu | rootfs
	CkptSize        string      // tile display ("30 GB")
	DeployManifest  string      // raw source Pod YAML
	RestoreManifest string      // raw restore Pod YAML
}

// workloadConfigs is the parsed catalog. Populated by loadWorkloads at
// package init time. demo.go indexes by workload id (the filename stem).
var workloadConfigs map[string]WorkloadConfig

// initLog is a package-scoped logger so init can log via the structured
// path. Set in init via the global logrus default — the nvsnap-server
// main wires logrus output before the first request, so init-time logs
// land where everything else does.
var initLog = logrus.WithField("subsys", "server.workloads")

func init() {
	dir := os.Getenv("NVSNAP_WORKLOADS_DIR")
	if dir == "" {
		dir = DefaultWorkloadsDir
	}
	cfgs, err := loadWorkloads(dir)
	if err != nil {
		// Don't fail server startup if the workloads directory is missing.
		// In test environments and `go run` from a checkout the path may
		// not exist; the demo UI just won't list any workloads.
		initLog.WithError(err).WithField("dir", dir).
			Warn("workload catalog unavailable; demo workloads disabled")
		workloadConfigs = map[string]WorkloadConfig{}
		return
	}
	workloadConfigs = cfgs
	initLog.WithField("dir", dir).WithField("count", len(cfgs)).
		Info("workload catalog loaded")
}

// loadWorkloads scans dir for <id>.yaml + <id>-restore.yaml pairs and
// builds a WorkloadConfig per pair. Files missing the restore pair, or
// missing required annotations, are skipped with a warn log.
//
// Pure function — does not touch package state. Used by init and tests.
func loadWorkloads(dir string) (map[string]WorkloadConfig, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read workloads dir %s: %w", dir, err)
	}
	out := map[string]WorkloadConfig{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, "-restore.yaml") {
			continue
		}
		id := strings.TrimSuffix(name, ".yaml")
		srcBytes, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		rstPath := filepath.Join(dir, id+"-restore.yaml")
		rstBytes, err := os.ReadFile(rstPath)
		if err != nil {
			initLog.WithField("id", id).WithError(err).
				Warn("workload skipped: missing restore pair")
			continue
		}
		cfg, err := parseWorkloadAnnotations(id, srcBytes)
		if err != nil {
			initLog.WithField("id", id).WithError(err).
				Warn("workload skipped: annotation parse failed")
			continue
		}
		cfg.DeployManifest = string(srcBytes)
		cfg.RestoreManifest = string(rstBytes)
		out[id] = cfg
	}
	return out, nil
}

// parseWorkloadAnnotations extracts the WorkloadConfig fields from a
// Pod's metadata annotations. Required annotations missing/empty →
// error and the workload is skipped at the catalog level.
func parseWorkloadAnnotations(id string, podYAML []byte) (WorkloadConfig, error) {
	var pod corev1.Pod
	if err := sigsyaml.Unmarshal(podYAML, &pod); err != nil {
		return WorkloadConfig{}, fmt.Errorf("yaml unmarshal: %w", err)
	}
	a := pod.Annotations
	must := func(k string) (string, error) {
		v, ok := a[k]
		if !ok || v == "" {
			return "", fmt.Errorf("missing required annotation %q", k)
		}
		return v, nil
	}
	cfg := WorkloadConfig{ID: id, PodName: pod.Name}
	var err error
	if cfg.DemoName, err = must(annDemoName); err != nil {
		return cfg, err
	}
	if cfg.Model, err = must(annModel); err != nil {
		return cfg, err
	}
	portStr, err := must(annPort)
	if err != nil {
		return cfg, err
	}
	if cfg.Port, err = strconv.Atoi(portStr); err != nil {
		return cfg, fmt.Errorf("annotation %s=%q: %w", annPort, portStr, err)
	}
	gpusStr, err := must(annGPUs)
	if err != nil {
		return cfg, err
	}
	if cfg.GPUs, err = strconv.Atoi(gpusStr); err != nil {
		return cfg, fmt.Errorf("annotation %s=%q: %w", annGPUs, gpusStr, err)
	}
	pathStr, err := must(annPath)
	if err != nil {
		return cfg, err
	}
	cfg.Path = CapturePath(pathStr)
	if cfg.Path != CapturePathCRIU && cfg.Path != CapturePathRootfs {
		return cfg, fmt.Errorf("annotation %s=%q: want criu|rootfs", annPath, pathStr)
	}
	// Optional fields — never error on absence.
	cfg.Desc = a[annDesc]
	cfg.CkptSize = a[annCkptSize]
	if cfg.PodName == "" {
		cfg.PodName = id
	}
	cfg.RestorePodName = cfg.PodName + "-restored"
	return cfg, nil
}
