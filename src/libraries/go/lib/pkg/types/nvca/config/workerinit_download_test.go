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

package nvcaconfig

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkerInitDownloadConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *WorkerInitDownloadConfig
		wantErr string
	}{
		{name: "nil is unset", cfg: nil},
		{name: "zero is unset", cfg: &WorkerInitDownloadConfig{}},
		{name: "ngc cli profile", cfg: &WorkerInitDownloadConfig{ConcurrentDownloads: 4, ConcurrentChunks: 16, ChunkSizeBytes: 512 << 20}},
		{name: "only chunk size", cfg: &WorkerInitDownloadConfig{ChunkSizeBytes: 1 << 20}},
		{name: "negative downloads", cfg: &WorkerInitDownloadConfig{ConcurrentDownloads: -1}, wantErr: "concurrentDownloads"},
		{name: "too many chunks", cfg: &WorkerInitDownloadConfig{ConcurrentChunks: 257}, wantErr: "concurrentChunks"},
		{name: "chunk below 1 MiB", cfg: &WorkerInitDownloadConfig{ChunkSizeBytes: 65536}, wantErr: "chunkSizeBytes"},
		{name: "chunk above 4 GiB", cfg: &WorkerInitDownloadConfig{ChunkSizeBytes: (4 << 30) + 1}, wantErr: "chunkSizeBytes"},
		{name: "negative chunk", cfg: &WorkerInitDownloadConfig{ChunkSizeBytes: -1}, wantErr: "chunkSizeBytes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestWorkerInitDownloadConfig_EnvOverrides(t *testing.T) {
	var unset *WorkerInitDownloadConfig
	assert.Nil(t, unset.EnvOverrides(), "nil config emits nothing")
	assert.Nil(t, (&WorkerInitDownloadConfig{}).EnvOverrides(), "zero config emits nothing, so worker-init keeps its defaults")

	assert.Equal(t, map[string]string{
		WorkerInitConcurrentDownloadsEnv: "4",
		WorkerInitConcurrentChunksEnv:    "16",
		WorkerInitChunkSizeEnv:           "536870912",
	}, (&WorkerInitDownloadConfig{ConcurrentDownloads: 4, ConcurrentChunks: 16, ChunkSizeBytes: 512 << 20}).EnvOverrides())

	assert.Equal(t, map[string]string{WorkerInitChunkSizeEnv: "1048576"},
		(&WorkerInitDownloadConfig{ChunkSizeBytes: 1 << 20}).EnvOverrides(),
		"only the fields that are set are emitted")
}

func TestWorkloadConfig_EffectiveEnvOverrides(t *testing.T) {
	t.Run("no download config leaves overrides untouched", func(t *testing.T) {
		explicit := map[string]string{"INIT_CONTAINER": "img"}
		cfg := WorkloadConfig{FunctionEnvOverrides: explicit}
		assert.Equal(t, explicit, cfg.EffectiveFunctionEnvOverrides())
		assert.Nil(t, cfg.EffectiveTaskEnvOverrides(), "nil stays nil so NVCA skips the override path")
	})

	t.Run("download config reaches function and task overrides", func(t *testing.T) {
		cfg := WorkloadConfig{
			WorkerInitDownload:   &WorkerInitDownloadConfig{ConcurrentDownloads: 4, ConcurrentChunks: 16, ChunkSizeBytes: 512 << 20},
			FunctionEnvOverrides: map[string]string{"INIT_CONTAINER": "img"},
		}
		fn := cfg.EffectiveFunctionEnvOverrides()
		assert.Equal(t, "img", fn["INIT_CONTAINER"])
		assert.Equal(t, "4", fn[WorkerInitConcurrentDownloadsEnv])
		assert.Equal(t, "16", fn[WorkerInitConcurrentChunksEnv])
		assert.Equal(t, "536870912", fn[WorkerInitChunkSizeEnv])
		assert.Len(t, fn, 4)
		assert.Equal(t, map[string]string{
			WorkerInitConcurrentDownloadsEnv: "4",
			WorkerInitConcurrentChunksEnv:    "16",
			WorkerInitChunkSizeEnv:           "536870912",
		}, cfg.EffectiveTaskEnvOverrides(), "tasks get the same tuning with no explicit task overrides")
		assert.Equal(t, map[string]string{"INIT_CONTAINER": "img"}, cfg.FunctionEnvOverrides, "the configured map is not mutated")
	})

	t.Run("explicit override wins, case-insensitively", func(t *testing.T) {
		cfg := WorkloadConfig{
			WorkerInitDownload:   &WorkerInitDownloadConfig{ChunkSizeBytes: 512 << 20, ConcurrentChunks: 16},
			FunctionEnvOverrides: map[string]string{"worker_chunk_size": "1048576"},
		}
		fn := cfg.EffectiveFunctionEnvOverrides()
		assert.Equal(t, "1048576", fn["worker_chunk_size"])
		_, derivedPresent := fn[WorkerInitChunkSizeEnv]
		assert.False(t, derivedPresent, "the derived upper-case key is dropped so the explicit value is the only one NVCA normalizes")
		assert.Equal(t, "16", fn[WorkerInitConcurrentChunksEnv])
	})
}

func TestConfig_Validate_WorkerInitDownload(t *testing.T) {
	cfg := Config{Workload: WorkloadConfig{WorkerInitDownload: &WorkerInitDownloadConfig{ChunkSizeBytes: 4096}}}
	require.ErrorContains(t, cfg.Validate(), "workload.workerInitDownload.chunkSizeBytes")
	cfg.Workload.WorkerInitDownload.ChunkSizeBytes = 512 << 20
	assert.NoError(t, cfg.Validate())
}

func TestWorkloadConfig_Complete_PreservesWorkerInitDownload(t *testing.T) {
	cfg := WorkloadConfig{WorkerInitDownload: &WorkerInitDownloadConfig{ConcurrentDownloads: 2}}
	assert.Equal(t, cfg.WorkerInitDownload, cfg.Complete().WorkerInitDownload)
}

// TestDecodeConfig_WorkerInitDownload feeds the exact document the chart's
// agent-config-merge ConfigMap carries through the real decoder.
func TestDecodeConfig_WorkerInitDownload(t *testing.T) {
	doc := []byte(`
workload:
  workerInitDownload:
    concurrentDownloads: 4
    concurrentChunks: 16
    chunkSizeBytes: 536870912
`)
	cfg, err := DecodeConfig(doc)
	require.NoError(t, err)
	require.NotNil(t, cfg.Workload.WorkerInitDownload)
	assert.Equal(t, 4, cfg.Workload.WorkerInitDownload.ConcurrentDownloads)
	assert.Equal(t, 16, cfg.Workload.WorkerInitDownload.ConcurrentChunks)
	assert.EqualValues(t, 536870912, cfg.Workload.WorkerInitDownload.ChunkSizeBytes)
	assert.Equal(t, "536870912", cfg.Workload.EffectiveFunctionEnvOverrides()[WorkerInitChunkSizeEnv])

	plain, err := DecodeConfig([]byte("workload: {}\n"))
	require.NoError(t, err)
	assert.Nil(t, plain.Workload.WorkerInitDownload, "absent block decodes to nil, so nothing is injected")
}

// TestEncodeConfig_WorkerInitDownload covers the operator path: the merge
// document is layered over the base config and re-decoded by the agent.
func TestEncodeConfig_WorkerInitDownload(t *testing.T) {
	// FunctionEnvOverrides are not part of this round trip: EncodeConfig
	// lower-cases map keys, which is why the operator ships them base64
	// encoded instead. Only the typed block is exercised here.
	base := Config{Workload: WorkloadConfig{StargateQUICInsecure: true}}
	merge := Config{Workload: WorkloadConfig{WorkerInitDownload: &WorkerInitDownloadConfig{ConcurrentChunks: 16, ChunkSizeBytes: 512 << 20}}}
	encoded, err := EncodeConfig(base, merge)
	require.NoError(t, err)
	decoded, err := DecodeConfig(encoded)
	require.NoError(t, err)
	require.NotNil(t, decoded.Workload.WorkerInitDownload)
	assert.Equal(t, 16, decoded.Workload.WorkerInitDownload.ConcurrentChunks)
	assert.EqualValues(t, 512<<20, decoded.Workload.WorkerInitDownload.ChunkSizeBytes)
	assert.Equal(t, 0, decoded.Workload.WorkerInitDownload.ConcurrentDownloads, "unset stays unset through encode and decode")
	assert.True(t, decoded.Workload.StargateQUICInsecure, "the base document survives the merge")
	fn := decoded.Workload.EffectiveFunctionEnvOverrides()
	assert.Equal(t, "16", fn[WorkerInitConcurrentChunksEnv])
	_, hasDownloads := fn[WorkerInitConcurrentDownloadsEnv]
	assert.False(t, hasDownloads)
}
