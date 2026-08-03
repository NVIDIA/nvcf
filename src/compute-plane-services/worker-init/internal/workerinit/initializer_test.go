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

package workerinit

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-init/configs"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-init/internal/downloader"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/metering"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/types"
)

func validConfig() configs.InitConfig {
	c := configs.InitConfig{}
	c.OTELExporterOTLPEndpoint = "http://localhost:4317"
	return c
}

func TestIsNVCTInit(t *testing.T) {
	nvcf := configs.InitConfig{}
	nvcf.NvcfWorkerToken = "tok"
	require.False(t, isNVCTInit(nvcf), "an NVCF worker token means NVCF init")

	require.True(t, isNVCTInit(configs.InitConfig{}), "absence of an NVCF worker token means NVCT init")
}

func TestNewInitializerSelectsNvcfAndDefaults(t *testing.T) {
	c := validConfig()
	c.NvcfWorkerToken = "tok"

	init, err := NewInitializer(c)
	require.NoError(t, err)

	nvcf, ok := init.(*NvcfInitializer)
	require.True(t, ok, "NVCF token selects the NVCF initializer")
	require.Equal(t, configs.DefaultModelsRepo, nvcf.config.ModelRepo)
	require.Equal(t, configs.DefaultResourcesRepo, nvcf.config.ResourceRepo)
	require.Equal(t, configs.DefaultConcurrentDownloads, nvcf.config.ConcurrentDownloads)
	require.Equal(t, configs.DefaultSharedConfigDir, nvcf.config.SharedConfigDir)
}

func TestNewInitializerSelectsNvctAndAppliesOverrides(t *testing.T) {
	c := validConfig()
	c.InstanceTypeName = "typed-name"
	c.SpotEnvironment = "stage" // ICMSEnvironment empty -> filled from this
	c.CloudProvider = "NGN"
	c.NcaId = "nca-1"

	init, err := NewInitializer(c)
	require.NoError(t, err)

	nvct, ok := init.(*NvctInitializer)
	require.True(t, ok, "no NVCF token selects the NVCT initializer")
	require.Equal(t, "typed-name", nvct.config.InstanceType, "InstanceTypeName overrides InstanceType for NVCT")
	require.Equal(t, "stage", nvct.config.ICMSEnvironment, "ICMSEnvironment is filled from SpotEnvironment")
	require.Equal(t, "GFN", nvct.meteringConfg.Backend, "NGN is normalized to GFN in metering")
	require.Equal(t, "nca-1", nvct.meteringConfg.BillingNcaId, "BillingNcaId defaults to NcaId")
}

func TestNewInitializerPropagatesNspectId(t *testing.T) {
	// NspectId is sourced from the NVCF_NSPECT_ID env var and must propagate
	// into the metering config so NVCF_Infrastructure init events carry nspect_id.
	t.Setenv(metering.EnvNspectId, "NSPECT-INIT-1234")
	c := validConfig()
	c.NvcfWorkerToken = "tok"

	init, err := NewInitializer(c)
	require.NoError(t, err)

	nvcf, ok := init.(*NvcfInitializer)
	require.True(t, ok)
	require.Equal(t, "NSPECT-INIT-1234", nvcf.meteringConfg.NspectId)
}

func TestNewInitializerInvalidOTELEndpoint(t *testing.T) {
	c := configs.InitConfig{}
	c.OTELExporterOTLPEndpoint = "://bad"
	_, err := NewInitializer(c)
	require.Error(t, err)
}

func TestLogArtifactsForDebugRedactsURL(t *testing.T) {
	core, logs := observer.New(zap.DebugLevel)
	undo := zap.ReplaceGlobals(zap.New(core))
	defer undo()

	logArtifactsForDebug("models", "/config/models", []types.Artifact{
		{Name: "m", Version: "v1", Path: "p", Url: "https://user:pass@host/path?token=secret&X-Amz=sig"},
	})

	var loggedURL string
	for _, entry := range logs.All() {
		for _, f := range entry.Context {
			if f.Key == "url" {
				loggedURL = f.String
			}
		}
	}
	require.Equal(t, "https://host/path?REDACTED", loggedURL, "userinfo stripped and query redacted")
}

func TestDownloadArtifactsEmptyDoesNothing(t *testing.T) {
	base := &baseInitializer{config: configs.InitConfig{}}
	err := base.downloadArtifacts(context.Background(), func(context.Context) (*types.ArtifactsList, error) {
		return &types.ArtifactsList{}, nil
	})
	require.NoError(t, err)
}

func TestDownloadArtifactsListError(t *testing.T) {
	base := &baseInitializer{config: configs.InitConfig{}}
	err := base.downloadArtifacts(context.Background(), func(context.Context) (*types.ArtifactsList, error) {
		return nil, errors.New("boom")
	})
	require.EqualError(t, err, "boom")
}

func TestDownloadArtifactsModelsErrorPropagated(t *testing.T) {
	repo := t.TempDir()
	base := &baseInitializer{
		config: configs.InitConfig{},
		modelDownloader: downloader.NewArtifactDownloader(
			repo, 1, 1, downloader.DefaultChunkSize,
		),
		resourceDownloader: downloader.NewArtifactDownloader(
			repo, 1, 1, downloader.DefaultChunkSize,
		),
	}
	// Empty URL causes the cache-check to fail fast (no network calls).
	err := base.downloadArtifacts(context.Background(), func(context.Context) (*types.ArtifactsList, error) {
		return &types.ArtifactsList{
			Models: []types.Artifact{{Name: "m", Version: "1", Path: "test.bin", Url: ""}},
		}, nil
	})
	require.Error(t, err)
}

func TestDownloadArtifactsResourcesErrorPropagated(t *testing.T) {
	repo := t.TempDir()
	base := &baseInitializer{
		config: configs.InitConfig{},
		modelDownloader: downloader.NewArtifactDownloader(
			t.TempDir(), 1, 1, downloader.DefaultChunkSize,
		),
		resourceDownloader: downloader.NewArtifactDownloader(
			repo, 1, 1, downloader.DefaultChunkSize,
		),
	}
	// Empty URL causes the cache-check to fail fast (no network calls).
	err := base.downloadArtifacts(context.Background(), func(context.Context) (*types.ArtifactsList, error) {
		return &types.ArtifactsList{
			Resources: []types.Artifact{{Name: "r", Version: "1", Path: "test.bin", Url: ""}},
		}, nil
	})
	require.Error(t, err)
}

func TestStartAssertionTokenRefresherSkipsWhenNoToken(t *testing.T) {
	base := &baseInitializer{config: configs.InitConfig{}}
	called := false
	base.startAssertionTokenRefresher(context.Background(), func(_ context.Context, _ string, _ bool) {
		called = true
	})
	require.False(t, called, "refresher must not be called when SecretsAssertionToken is empty")
}

func TestStartAssertionTokenRefresherCallsWithToken(t *testing.T) {
	c := configs.InitConfig{}
	c.SecretsAssertionToken = "tok"
	c.EssAgentConfigDir = "/tmp/ess-test-dir"
	base := &baseInitializer{config: c}

	var gotPath string
	var gotRotate bool
	base.startAssertionTokenRefresher(context.Background(), func(_ context.Context, path string, rotate bool) {
		gotPath = path
		gotRotate = rotate
	})
	require.Equal(t, "/tmp/ess-test-dir/jwt.token", gotPath)
	require.True(t, gotRotate)
}
