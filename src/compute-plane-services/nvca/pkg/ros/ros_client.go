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

package ros

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	cmnhttp "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/http"
	"github.com/sirupsen/logrus"
	oteltrace "go.opentelemetry.io/otel/trace"

	nvcaauth "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/auth"
	nvcaotel "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/otel"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

const (
	http403ErrThreshold = 10
)

// ROSClient provides access to RolloverService endpoints
// with auth credential from a tokenFetcher
//
//nolint:revive
type ROSClient struct {
	// endpoint is the base URL of RolloverService Cluster, e.g.,
	// https://api.ros.nvidia.com
	endpoint         string
	clusterID        string
	ncaID            string
	maxResponseBytes int64
	tokenFetcher     nvcaauth.TokenFetcher
	http403ErrCount  uint64

	// OTel tracer initialized at agent startup
	tracer oteltrace.Tracer

	Client *http.Client
}

func NewROSClient(ctx context.Context, ncaID, clusterID, endpointURL string, tokenFetcher nvcaauth.TokenFetcher, tracer oteltrace.Tracer) *ROSClient {
	return newROSClient(ctx, ncaID, clusterID, strings.TrimSuffix(endpointURL, "/"), tokenFetcher, tracer)
}

func (c *ROSClient) GetConfig() (string, string) {
	return c.clusterID, c.endpoint
}

func newROSClient(ctx context.Context, ncaID, clusterID, endpoint string, tokenFetcher nvcaauth.TokenFetcher, tracer oteltrace.Tracer) *ROSClient {
	// If the tracer is nil grab the default nvcaotel tracer
	if tracer == nil {
		tracer = nvcaotel.NewTracer()
	}

	log := core.GetLogger(ctx)
	rosC := &ROSClient{
		clusterID:        clusterID,
		endpoint:         endpoint,
		ncaID:            ncaID,
		maxResponseBytes: 250 * int64(1<<10), // Limit response payload to 250K
		Client: cmnhttp.NewRetryableClient(ctx,
			cmnhttp.WithAppVersionUserAgent(types.AppName),
			cmnhttp.WithRequestHeader(types.HeaderNVClusterID, clusterID)),
		tokenFetcher: tokenFetcher,
		tracer:       tracer,
	}
	log.Debugf("ROS Client Parameters: %+v", rosC)
	return rosC
}

func (c *ROSClient) logResponseError(log logrus.FieldLogger, resp *http.Response, respBody []byte) {
	log = log.WithField("http_status", resp.StatusCode)
	if len(respBody) == 0 {
		var err error
		respBody, err = io.ReadAll(http.MaxBytesReader(nil, resp.Body, c.maxResponseBytes))
		if err != nil {
			log.WithError(err).Error("Internal error, failed to read response body")
		}
	}
	log.Errorf("Expected response to be %d, got %s (%s)", http.StatusOK, resp.Status, string(respBody))
}

func (c *ROSClient) PostFunctionInstanceStatusUpdate(ctx context.Context,
	reqID, instanceID string, isUpdates []types.InstanceUpdateStatusDTO) error {
	url := fmt.Sprintf("%s/v1/functions/status", c.endpoint)

	log := core.GetLogger(ctx).WithFields(logrus.Fields{
		"rpc":        "ros.FunctionInstanceStatusUpdateRequest",
		"endpoint":   url,
		"reqId":      reqID,
		"instanceId": instanceID,
	})

	if c.http403ErrCount > http403ErrThreshold {
		log.Trace("ROS integration temporarily disabled")
		// ignore silently once the service API key is Updated, NVCA will
		// will redeply and the count will be reset since its in-memory
		return nil
	}

	// fetch the service API key from the secret in the cluster instead
	jwt, err := c.tokenFetcher.FetchToken(ctx)
	if err != nil {
		return err
	}

	// affix clusterID
	for i := range isUpdates {
		isUpdates[i].ClusterID = c.clusterID
	}

	data, err := json.Marshal(isUpdates)
	if err != nil {
		return err
	}

	body := bytes.NewReader(data)
	req, _ := http.NewRequestWithContext(ctx, "POST", url, body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if jwt != "" {
		req.Header.Set("Authorization", "Bearer "+jwt)
	}

	log.WithFields(logrus.Fields{
		"body": string(data),
	}).Debug("Sending request to ROS")

	resp, err := c.Client.Do(req)
	if err != nil {
		log.WithError(err).Error("Do request")
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		c.logResponseError(log, resp, nil)
		if resp.StatusCode == http.StatusForbidden {
			c.http403ErrCount++
		}
		return fmt.Errorf("error response from ROS: %s", resp.Status)
	}

	// log response for debug logs
	log.WithFields(logrus.Fields{
		"response": resp,
	}).Debug("Received http.StatusOK from ROS")
	return nil
}
