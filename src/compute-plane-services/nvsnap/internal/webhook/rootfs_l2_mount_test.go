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

package webhook

import (
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
)

func TestManifestIsRootfs(t *testing.T) {
	cases := []struct {
		name string
		m    checkpointstore.Manifest
		want bool
	}{
		{"extract-paths", checkpointstore.Manifest{
			RootfsExtractPaths: []checkpointstore.ExtractPath{{Path: "/opt/nim/.cache"}}}, true},
		{"rootfs-volume", checkpointstore.Manifest{
			Volumes: []checkpointstore.VolumeMeta{{Name: "rootfs", Type: "rootfs"}}}, true},
		{"criu-empty", checkpointstore.Manifest{}, false},
		{"criu-volumes", checkpointstore.Manifest{
			Volumes: []checkpointstore.VolumeMeta{{Name: "dump", Type: "emptyDir"}}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := manifestIsRootfs(tc.m); got != tc.want {
				t.Errorf("manifestIsRootfs = %v, want %v", got, tc.want)
			}
		})
	}
}
