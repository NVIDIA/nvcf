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

package testutils

import (
	"context"
	"embed"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/nats-io/nats-server/v2/logger"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.uber.org/zap"
)

//go:embed nats-config
var natsConfigDir embed.FS

type SuperCluster struct {
	Clusters []*Cluster
	// Single server that backs all regions
	singleServer *server.Server
}

type Cluster struct {
	Region  string
	Servers []*server.Server
}

func (sc *SuperCluster) Shutdown() {
	if sc.singleServer != nil {
		sc.singleServer.Shutdown()
		sc.singleServer.WaitForShutdown()
	}
}

func NewNatsSuperCluster(t *testing.T) (*SuperCluster, error) {
	const natsLogging = false
	confFilesDir := t.TempDir()

	// Create single server configuration
	confFileLocation := filepath.Join(confFilesDir, "single-server.conf")
	fileContents, err := natsConfigDir.ReadFile("nats-config/single-server.conf")
	if err != nil {
		return nil, err
	}

	err = os.WriteFile(confFileLocation, fileContents, 0644)
	if err != nil {
		return nil, err
	}

	// Create server options
	opts := &server.Options{}
	if err := opts.ProcessConfigFile(confFileLocation); err != nil {
		return nil, err
	}
	opts.StoreDir = t.TempDir()
	opts.DisableJetStreamBanner = true

	// Create single server
	s, err := server.NewServer(opts)
	if err != nil {
		return nil, err
	}
	if natsLogging {
		s.SetLoggerV2(logger.NewTestLogger(s.Name()+" ", true), false, false, false)
	}

	// Create SuperCluster structure with the single server appearing in each region
	// This maintains backward compatibility with existing code
	superCluster := &SuperCluster{
		singleServer: s,
		Clusters: []*Cluster{
			{Region: "region-1", Servers: []*server.Server{s}},
			{Region: "region-2", Servers: []*server.Server{s}},
			{Region: "region-3", Servers: []*server.Server{s}},
		},
	}

	// Start the single server
	err = startSingleServer(s)
	if err != nil {
		return nil, err
	}

	// Validate JetStream functionality for all regions
	err = validateSingleServerForAllRegions(s)
	if err != nil {
		return nil, err
	}

	zap.L().Info("nats single server ready with multi-region support")
	return superCluster, nil
}

func startSingleServer(s *server.Server) error {
	zap.L().Info("starting nats server", zap.String("url", s.ClientURL()))
	s.Start()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := backoff.Retry(func() error {
		for s.MonitorAddr() == nil {
			time.Sleep(time.Millisecond * 10)
		}
		url := "http://" + s.MonitorAddr().String() + "/healthz"
		request, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return fmt.Errorf("could not create healthz request: %w", err)
		}
		ctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		request = request.WithContext(ctx)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			return fmt.Errorf("failed to send healthz request: %w", err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("bad nats healthz status code: %d", response.StatusCode)
		}
		return nil
	}, backoff.WithContext(backoff.NewExponentialBackOff(), ctx))

	if err != nil {
		zap.L().Error("nats server startup failed", zap.String("server", s.Name()), zap.Error(err))
		return err
	}
	return nil
}

func validateSingleServerForAllRegions(s *server.Server) error {
	regions := []string{"region-1", "region-2", "region-3"}

	for _, region := range regions {
		err := validateJetStreamForRegion(s, "aws-region:"+region)
		if err != nil {
			return fmt.Errorf("failed to validate JetStream for region %s: %w", region, err)
		}
	}
	return nil
}

func validateJetStreamForRegion(s *server.Server, tag string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := backoff.Retry(func() error {
		nc, err := nats.Connect(s.ClientURL())
		if err != nil {
			return err
		}
		defer nc.Close()
		js, err := jetstream.New(nc)
		if err != nil {
			return err
		}
		streamName := s.Name() + "_test-stream_" + tag
		ctx, cancel := context.WithTimeout(ctx, 1*time.Second)
		defer cancel()
		_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
			Name:      streamName,
			Subjects:  []string{s.Name() + ".test-stream." + tag + ".>"},
			Storage:   jetstream.MemoryStorage,
			Replicas:  1, // Single server, so replicas = 1
			Placement: &jetstream.Placement{Tags: []string{tag}},
		})
		if err != nil {
			return err
		}
		return js.DeleteStream(ctx, streamName)
	}, backoff.WithContext(backoff.NewExponentialBackOff(), ctx))

	if err != nil {
		zap.L().Error("failed to create test stream", zap.String("server", s.Name()), zap.String("tag", tag), zap.Error(err))
		return err
	}
	return nil
}
