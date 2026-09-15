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

package nvct

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/clients"
	"go.uber.org/zap"
	"golang.org/x/oauth2"
	"google.golang.org/grpc"

	nvauth "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/auth"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/auth"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvct"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/token"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/utils"
)

const cachedNvctTokenFilename = "nvct-worker-token.json"

// By default, grpc-go doesn't have a timeout.
const DefaultNvctClientTimeout = 20 * time.Second

type Client struct {
	Client                  pb.WorkerClient
	regionalNvctClientsLock sync.RWMutex
	regionalNvctClients     map[string]pb.WorkerClient
	NvctTokenProvider       *auth.SettableTokenSource
	instanceId              string
	taskId                  string
	assertionTokenPath      string
	instanceType            string
	clientTimeout           time.Duration
	sharedConfigDir         string
}

func CreateClient(nvctFqdn string, nvctWorkerToken string, instanceId string, taskId string, instanceType string, nvctClientTimeout time.Duration, sharedConfigDir string) (*Client, error) {
	workerClient, err := createWorkerClient(nvctFqdn, nvctClientTimeout)
	if err != nil {
		return nil, err
	}

	tokenExpiry, err := calculateAssertionTokenExpiry(nvctWorkerToken)
	if err != nil {
		return nil, fmt.Errorf("failed to get token expiry: %w", err)
	}

	nvctToken := &oauth2.Token{
		AccessToken: nvctWorkerToken,
		Expiry:      tokenExpiry,
	}

	// By design, NVCA and newt will always mount sharedConfigDir in the worker spec
	_, err = os.Stat(sharedConfigDir)
	if errors.Is(err, os.ErrNotExist) {
		// Support local dev and Slurm cluster environments where the dirs might need to be created
		zap.L().Warn("couldn't find directory, assuming it is not mounted via cluster agent. Attempting to create", zap.String("sharedConfigDir", sharedConfigDir))
		err = utils.CreateDirectory(sharedConfigDir, os.FileMode(0755))
		if err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}

	zap.L().Info("checking for cached NVCT token")
	tokenFile := filepath.Join(sharedConfigDir, cachedNvctTokenFilename)
	cachedNvctToken, err := token.LoadCachedTokenIfExists(tokenFile)
	if err != nil {
		zap.L().Error("error while loading cached NVCT token", zap.Error(err))
		zap.L().Warn("cached token found, but unloadable - using environment token")
	} else if cachedNvctToken != nil {
		zap.L().Info("cached token found")
		nvctToken = cachedNvctToken
	} else {
		zap.L().Info("no cached token found - using environment token")
	}

	return &Client{
		Client: workerClient,
		regionalNvctClients: map[string]pb.WorkerClient{
			nvctFqdn: workerClient,
		},
		NvctTokenProvider: auth.NewSettableTokenSource(oauth2.StaticTokenSource(nvctToken)),
		instanceId:        instanceId,
		taskId:            taskId,
		instanceType:      instanceType,
		clientTimeout:     nvctClientTimeout,
		sharedConfigDir:   sharedConfigDir,
	}, nil
}

// Get a regional nvct client based on FQDN
func (c *Client) GetRegionalNvctClient(nvctFqdn string) (pb.WorkerClient, error) {
	if nvctFqdn == "" {
		return c.Client, nil
	}
	c.regionalNvctClientsLock.RLock()
	if client, ok := c.regionalNvctClients[nvctFqdn]; ok {
		c.regionalNvctClientsLock.RUnlock()
		return client, nil
	}
	c.regionalNvctClientsLock.RUnlock()
	c.regionalNvctClientsLock.Lock()
	defer c.regionalNvctClientsLock.Unlock()
	if client, ok := c.regionalNvctClients[nvctFqdn]; ok {
		return client, nil
	}
	client, err := createWorkerClient(nvctFqdn, c.clientTimeout)
	if err != nil {
		return nil, err
	}
	c.regionalNvctClients[nvctFqdn] = client
	return client, nil
}

// Setup nvct client
func createWorkerClient(nvctFqdn string, timeout time.Duration) (pb.WorkerClient, error) {
	nvctUrl, err := utils.PortSafeUrl(nvctFqdn)
	if err != nil {
		return nil, err
	}

	grpcClientConfig := clients.GRPCClientConfig{BaseClientConfig: &clients.BaseClientConfig{
		Addr: nvctUrl.Host,
		TLS: nvauth.TLSConfigOptions{
			Enabled: nvctUrl.Scheme == "https",
		},
	}}
	dialOptions, err := grpcClientConfig.DialOptions()
	if err != nil {
		return nil, err
	}
	// larger than our max expected message size
	const tenMB = utils.OneMB * 10
	dialOptions = append(dialOptions,
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(tenMB)),
		grpc.WithInitialWindowSize(tenMB),
		grpc.WithInitialConnWindowSize(tenMB),
		grpc.WithSharedWriteBuffer(true),
		grpc.WithWriteBufferSize(tenMB),
		grpc.WithTimeout(timeout), //nolint:staticcheck // TODO: migrate to NewClient with context timeout
		grpc.WithContextDialer(func(ctx context.Context, s string) (net.Conn, error) {
			conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", s)
			if err != nil {
				return nil, err
			}
			initialBufSize := utils.GetWriteBufferSize(conn)
			type WriteBufferSetter interface {
				SetWriteBuffer(bytes int) error
			}
			if setter, ok := conn.(WriteBufferSetter); ok && tenMB > initialBufSize {
				var err error
				for size := ((initialBufSize / utils.OneMB) + 1) * utils.OneMB; err == nil && size <= tenMB; size += utils.OneMB {
					err = setter.SetWriteBuffer(size)
				}
				syscallError := &os.SyscallError{}
				if errors.As(err, &syscallError) {
					if syscallError.Unwrap().Error() == "no buffer space available" {
						err = nil
					}
				}
				zap.L().Info("setting write buffer up to 10MB", zap.Error(err))
				utils.GetWriteBufferSize(conn)
			}
			return conn, nil
		}),
	)
	grpcClientConfig.DialOptOverrides = dialOptions
	conn, err := grpcClientConfig.Dial()
	if err != nil {
		return nil, err
	}
	workerClient := pb.NewWorkerClient(conn)
	return workerClient, nil
}
