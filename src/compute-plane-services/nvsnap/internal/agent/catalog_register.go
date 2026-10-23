// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

// Catalog registration and dump-directory walking, shared by the capture
// paths and the cross-cluster replication uploader. Formerly part of the
// retired blob-store uploader.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// fallbackString returns a if non-empty, else b.
func fallbackString(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// registerCheckpointInCatalog upserts a catalog row for this
// checkpoint via nvsnap-server. Idempotent — safe to call from
// retry paths. Best-effort: failures don't fail capture.
//
// Required so peer-add and /sources can find the
// row. Without this, agents capturing via the direct /v1/checkpoint
// API land orphan checkpoints (rows only exist when capture is
// initiated through nvsnap-server's CRD-driven flow).
//
// catalog (nvsnap#59) carries the content-addressed identity fields
// — image_ref, model_id, engine_flags, driver_version, etc. They
// land on the nvsnap-server row so NVCA's Hook A can find this
// checkpoint via POST /api/v1/checkpoints/lookup across fvIDs.
// Pass an empty CatalogInfo to skip (older agents, tests).
func (a *Agent) registerCheckpointInCatalog(ctx context.Context, checkpointID, namespace, podName, containerName, containerImage string, size int64, duration float64, hasGPU bool, catalog CatalogInfo) error {
	if a.config.CatalogURL == "" {
		return errors.New("CatalogURL not configured")
	}
	body, _ := json.Marshal(struct {
		CheckpointID      string   `json:"checkpoint_id"`
		Namespace         string   `json:"namespace"`
		PodName           string   `json:"pod_name"`
		ContainerName     string   `json:"container_name,omitempty"`
		ContainerImage    string   `json:"container_image,omitempty"`
		NodeName          string   `json:"node_name"`
		CheckpointSize    int64    `json:"checkpoint_size,omitempty"`
		Status            string   `json:"status"`
		HasGPU            bool     `json:"has_gpu"`
		DurationSecs      float64  `json:"duration_secs,omitempty"`
		Hash              string   `json:"hash,omitempty"`
		ImageRef          string   `json:"image_ref,omitempty"`
		ImageDigest       string   `json:"image_digest,omitempty"`
		ModelID           string   `json:"model_id,omitempty"`
		EngineFlags       []string `json:"engine_flags,omitempty"`
		GPUType           string   `json:"gpu_type,omitempty"`
		GPUCount          int      `json:"gpu_count,omitempty"`
		DriverVersion     string   `json:"driver_version,omitempty"`
		CUDAVersion       string   `json:"cuda_version,omitempty"`
		CPUArchitecture   string   `json:"cpu_architecture,omitempty"`
		FunctionName      string   `json:"function_name,omitempty"`
		FunctionVersionID string   `json:"function_version_id,omitempty"`
	}{
		CheckpointID:   checkpointID,
		Namespace:      namespace,
		PodName:        podName,
		ContainerName:  containerName,
		ContainerImage: containerImage,
		NodeName:       a.config.NodeName,
		CheckpointSize: size,
		Status:         "Completed",
		HasGPU:         hasGPU,
		DurationSecs:   duration,
		// CatalogInfo passthrough. Image ref defaults to the container
		// image when the catalog lookup couldn't resolve a digest yet
		// — server still needs ImageRef populated for indexed lookup.
		Hash:              catalog.Hash,
		ImageRef:          fallbackString(catalog.ImageRef, containerImage),
		ImageDigest:       catalog.ImageDigest,
		ModelID:           catalog.ModelID,
		EngineFlags:       catalog.EngineFlags,
		GPUType:           catalog.GPUType,
		GPUCount:          catalog.GPUCount,
		DriverVersion:     catalog.DriverVersion,
		CUDAVersion:       catalog.CUDAVersion,
		CPUArchitecture:   catalog.CPUArchitecture,
		FunctionName:      catalog.FunctionName,
		FunctionVersionID: catalog.FunctionVersionID,
	})
	u := fmt.Sprintf("%s/api/v1/checkpoints/register", a.config.CatalogURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytesReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("register: status %d: %s", resp.StatusCode, b)
	}
	return nil
}

// dumpFile is a per-file work item for the upload pool.
type dumpFile struct {
	relPath string
	size    int64
}

// walkDumpDir returns every regular file under dumpDir as a list
// of (relPath, size) entries with forward-slash paths. Directories
// and symlinks are skipped — CRIU dumps don't contain symlinks
// inside the images dir, and dirs are implicit from path prefixes.
func walkDumpDir(dumpDir string) ([]dumpFile, error) {
	var out []dumpFile
	err := filepath.Walk(dumpDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(dumpDir, path)
		if err != nil {
			return err
		}
		out = append(out, dumpFile{
			relPath: filepath.ToSlash(rel),
			size:    info.Size(),
		})
		return nil
	})
	return out, err
}
