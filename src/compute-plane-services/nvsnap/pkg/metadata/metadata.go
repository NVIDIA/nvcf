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

// Package metadata handles checkpoint metadata for cross-node restore operations.
package metadata

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	// MetadataFilename is the name of the metadata file in checkpoint directories
	MetadataFilename = "metadata.json"
	// DescriptorsFilename is the name of the file descriptors file
	DescriptorsFilename = "descriptors.json"
)

// CheckpointMetadata stores information needed for cross-node restore
type CheckpointMetadata struct {
	// Checkpoint identification
	CheckpointID string    `json:"checkpoint_id"`
	CreatedAt    time.Time `json:"created_at"`

	// Source information
	SourceNode   string `json:"source_node"`
	SourcePodIP  string `json:"source_pod_ip,omitempty"`
	ContainerID  string `json:"container_id"`
	PodName      string `json:"pod_name"`
	PodNamespace string `json:"pod_namespace"`
	Image        string `json:"image"`

	// Process information
	PID int `json:"pid"`

	// Filesystem information
	RootfsDiffPath  string `json:"rootfs_diff_path,omitempty"`
	UpperDir        string `json:"upper_dir,omitempty"`
	HasRootfsDiff   bool   `json:"has_rootfs_diff"`
	HasDeletedFiles bool   `json:"has_deleted_files"`

	// Mount mappings from original container
	Mounts []MountMetadata `json:"mounts"`

	// OCI spec derived paths
	MaskedPaths    []string `json:"masked_paths,omitempty"`
	ReadonlyPaths  []string `json:"readonly_paths,omitempty"`
	BindMountDests []string `json:"bind_mount_dests,omitempty"`

	// Namespace information
	Namespaces []NamespaceMetadata `json:"namespaces"`

	// CRIU options used during dump
	CRIUOptions CRIUOptionsMetadata `json:"criu_options"`
}

// MountMetadata stores information about a mount for remapping during restore
type MountMetadata struct {
	ContainerPath string   `json:"container_path"`
	HostPath      string   `json:"host_path"`
	OCISource     string   `json:"oci_source,omitempty"`
	OCIType       string   `json:"oci_type,omitempty"`
	OCIOptions    []string `json:"oci_options,omitempty"`
	VolumeType    string   `json:"volume_type"`
	VolumeName    string   `json:"volume_name"`
	FSType        string   `json:"fs_type"`
	ReadOnly      bool     `json:"read_only"`
}

// NamespaceMetadata stores namespace information
type NamespaceMetadata struct {
	Type       string `json:"type"`
	Inode      uint64 `json:"inode"`
	IsExternal bool   `json:"is_external"`
}

// CRIUOptionsMetadata stores CRIU options used during checkpoint
type CRIUOptionsMetadata struct {
	TcpEstablished bool `json:"tcp_established"` //nolint:revive // exported name kept for API stability
	TcpClose       bool `json:"tcp_close"`       //nolint:revive // exported name kept for API stability
	ShellJob       bool `json:"shell_job"`
	FileLocks      bool `json:"file_locks"`
	LeaveRunning   bool `json:"leave_running"`
}

// NewCheckpointMetadata creates a new metadata instance
func NewCheckpointMetadata(checkpointID string) *CheckpointMetadata {
	return &CheckpointMetadata{
		CheckpointID: checkpointID,
		CreatedAt:    time.Now().UTC(),
		Mounts:       make([]MountMetadata, 0),
		Namespaces:   make([]NamespaceMetadata, 0),
	}
}

// SaveMetadata writes metadata to a JSON file in the checkpoint directory
func SaveMetadata(checkpointDir string, meta *CheckpointMetadata) error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	metadataPath := filepath.Join(checkpointDir, MetadataFilename)
	if err := os.WriteFile(metadataPath, data, 0o644); err != nil {
		return fmt.Errorf("failed to write metadata file: %w", err)
	}

	return nil
}

// LoadMetadata reads metadata from a checkpoint directory
func LoadMetadata(checkpointDir string) (*CheckpointMetadata, error) {
	metadataPath := filepath.Join(checkpointDir, MetadataFilename)

	data, err := os.ReadFile(metadataPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read metadata file: %w", err)
	}

	var meta CheckpointMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("failed to unmarshal metadata: %w", err)
	}

	return &meta, nil
}

// GetCheckpointDir returns the path to a checkpoint directory
func GetCheckpointDir(baseDir, checkpointID string) string {
	return filepath.Join(baseDir, checkpointID)
}

// ListCheckpoints returns all checkpoint IDs in the base directory
func ListCheckpoints(baseDir string) ([]string, error) {
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read checkpoint directory: %w", err)
	}

	var checkpoints []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		metadataPath := filepath.Join(baseDir, entry.Name(), MetadataFilename)
		if _, err := os.Stat(metadataPath); err == nil {
			checkpoints = append(checkpoints, entry.Name())
		}
	}

	return checkpoints, nil
}

// DeleteCheckpoint removes a checkpoint directory
func DeleteCheckpoint(baseDir, checkpointID string) error {
	checkpointDir := GetCheckpointDir(baseDir, checkpointID)
	return os.RemoveAll(checkpointDir)
}
