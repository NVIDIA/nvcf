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

package nvca

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	cmnhttp "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/http"
	"github.com/sirupsen/logrus"
	oteltrace "go.opentelemetry.io/otel/trace"

	nvcaauth "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/auth"
	nvcaotel "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/otel"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	nvcaerrors "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/errors"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

// ICMSClient provides access to ICMS endpoints
// with auth credential from a tokenFetcher
type ICMSClient struct {
	// endpoint is the base URL of ICMS service, e.g.,
	// ICMS service endpoint
	endpoint         string
	host             string
	clusterID        string
	maxResponseBytes int64
	tokenFetcher     nvcaauth.TokenFetcher

	// Path templates for URLs (excluding base), customizable via env at startup
	pathInstanceStatus string // e.g. "v1/si/clusters/%s/instances"
	pathNVCAPrefix     string // e.g. "v1/nvca" -> .../clusters/%s/register, etc.
	pathRegister       string // full path template for register (one %s = clusterID); empty = use pathNVCAPrefix + /clusters/%s/register
	pathCredentials    string // full path template for credentials; empty = use pathNVCAPrefix + /clusters/%s/credentials
	pathHeartbeat      string // full path template for heartbeat; empty = use pathNVCAPrefix + /clusters/%s/heartbeat
	pathSIRsPrefix     string // e.g. "v1/sirs" -> .../sirs/%s, .../sirs/%s/%s

	// OTel tracer initialized at agent startup
	tracer oteltrace.Tracer

	client *http.Client
}

// Legacy ICMS API path customization (paths are appended to the base URL from config).
// Set these environment variables before NVCA startup to override the default paths:
//
//   - NVCA_ICMS_PATH_INSTANCE_STATUS — instance status (one %s = clusterID), default "v1/si/clusters/%s/instances"
//   - NVCA_ICMS_PATH_NVCA_PREFIX     — prefix for register/credentials/heartbeat, default "v1/nvca"
//   - NVCA_ICMS_PATH_REGISTER        — register path (one %s = clusterID); empty = use prefix + "clusters/%s/register"
//   - NVCA_ICMS_PATH_CREDENTIALS     — credentials path (one %s = clusterID); empty = use prefix + "clusters/%s/credentials"
//   - NVCA_ICMS_PATH_HEARTBEAT       — heartbeat path (one %s = clusterID); empty = use prefix + "clusters/%s/heartbeat"
//   - NVCA_ICMS_PATH_SIRS_PREFIX     — prefix for SIR ack and instance status update, default "v1/sirs"
const (
	envICMSPathInstanceStatus = "NVCA_ICMS_PATH_INSTANCE_STATUS"
	envICMSPathNVCAPrefix     = "NVCA_ICMS_PATH_NVCA_PREFIX"
	envICMSPathRegister       = "NVCA_ICMS_PATH_REGISTER"
	envICMSPathCredentials    = "NVCA_ICMS_PATH_CREDENTIALS"
	envICMSPathHeartbeat      = "NVCA_ICMS_PATH_HEARTBEAT"
	envICMSPathSIRsPrefix     = "NVCA_ICMS_PATH_SIRS_PREFIX"
)

// icmsPathConfig returns the path config from env or default (no leading slash).
func icmsPathConfig(envKey, defaultVal string) string {
	if v := os.Getenv(envKey); v != "" {
		return strings.TrimPrefix(v, "/")
	}
	return defaultVal
}

// Default path templates (no leading slash; appended to base URL).
const (
	defaultPathInstanceStatus = "v1/si/clusters/%s/instances"
	defaultPathNVCAPrefix     = "v1/nvca"
	defaultPathSIRsPrefix     = "v1/sirs"
)

func NewICMSClient(ctx context.Context, clusterID, endpoint string, tokenFetcher nvcaauth.TokenFetcher, tracer oteltrace.Tracer) *ICMSClient {
	return NewICMSClientWithHostHeaderOverride(ctx, clusterID, endpoint, "", tokenFetcher, tracer)
}

// NewICMSClientWithHostHeaderOverride creates an ICMS client with an optional HTTP Host header override.
func NewICMSClientWithHostHeaderOverride(
	ctx context.Context,
	clusterID, endpoint, host string,
	tokenFetcher nvcaauth.TokenFetcher,
	tracer oteltrace.Tracer,
) *ICMSClient {
	return newICMSClient(ctx, clusterID, strings.TrimSuffix(endpoint, "/"), host, tokenFetcher, tracer)
}

func newICMSClient(ctx context.Context, clusterID, endpoint, host string, tokenFetcher nvcaauth.TokenFetcher, tracer oteltrace.Tracer) *ICMSClient {
	// If the tracer is nil grab the default nvcaotel tracer
	if tracer == nil {
		tracer = nvcaotel.NewTracer()
	}

	return &ICMSClient{
		clusterID:          clusterID,
		endpoint:           endpoint,
		host:               host,
		maxResponseBytes:   250 * int64(1<<10), // Limit response payload to 250K
		pathInstanceStatus: icmsPathConfig(envICMSPathInstanceStatus, defaultPathInstanceStatus),
		pathNVCAPrefix:     icmsPathConfig(envICMSPathNVCAPrefix, defaultPathNVCAPrefix),
		pathRegister:       strings.TrimPrefix(os.Getenv(envICMSPathRegister), "/"),
		pathCredentials:    strings.TrimPrefix(os.Getenv(envICMSPathCredentials), "/"),
		pathHeartbeat:      strings.TrimPrefix(os.Getenv(envICMSPathHeartbeat), "/"),
		pathSIRsPrefix:     icmsPathConfig(envICMSPathSIRsPrefix, defaultPathSIRsPrefix),
		client: cmnhttp.NewRetryableClient(ctx,
			cmnhttp.WithAppVersionUserAgent(types.AppName),
			cmnhttp.WithRequestHeader(types.HeaderNVClusterID, clusterID),
			cmnhttp.WithClientOptions(cmnhttp.ClientOptions{
				RetryWaitMin: 3 * time.Second,
				RetryWaitMax: 1 * time.Minute,
			}),
		),
		tokenFetcher: tokenFetcher,
		tracer:       tracer,
	}
}

func (c *ICMSClient) newRequest(ctx context.Context, method, requestURL string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, requestURL, body)
	if err != nil {
		return nil, err
	}
	if c.host != "" {
		req.Host = c.host
	}
	return req, nil
}

// Endpoint returns the ICMS service endpoint URL
func (c *ICMSClient) Endpoint() string {
	return c.endpoint
}

// buildURL builds a full URL from the endpoint and a path template with optional args.
// If args are given, pathTpl is a format string (e.g. "v1/si/clusters/%s/instances"); otherwise pathTpl is used as-is.
func (c *ICMSClient) buildURL(pathTpl string, args ...string) (string, error) {
	var rel string
	if len(args) == 0 {
		rel = strings.TrimPrefix(pathTpl, "/")
	} else {
		argsAny := make([]any, len(args))
		for i, a := range args {
			argsAny[i] = a
		}
		rel = fmt.Sprintf(strings.TrimPrefix(pathTpl, "/"), argsAny...)
	}
	base := strings.TrimSuffix(c.endpoint, "/")
	if rel == "" {
		return base, nil
	}
	return base + "/" + rel, nil
}

// nvcaPath returns the path for register/credentials/heartbeat: custom if set, else prefix + suffix.
func (c *ICMSClient) nvcaPath(custom, suffix string) string {
	if custom != "" {
		return fmt.Sprintf(custom, c.clusterID)
	}
	return fmt.Sprintf("%s/clusters/%s/%s", c.pathNVCAPrefix, c.clusterID, suffix)
}

func (c *ICMSClient) checkResponse(log logrus.FieldLogger, resp *http.Response, respBody []byte) error {
	if resp.StatusCode == http.StatusOK {
		log.Debug("Received OK from ICMS")
		return nil
	}

	c.logResponseError(log, resp, respBody)

	return nvcaerrors.HTTPStatusError(resp.StatusCode, fmt.Errorf("error response from ICMS: %s", resp.Status))
}

func (c *ICMSClient) logResponseError(log logrus.FieldLogger, resp *http.Response, respBody []byte) {
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

func (c *ICMSClient) GetICMSServerInstanceStatuses(ctx context.Context) (types.ICMSInstanceStatusResponse, error) {
	requestURL, err := c.buildURL(c.pathInstanceStatus, c.clusterID)
	if err != nil {
		return types.ICMSInstanceStatusResponse{}, err
	}

	log := core.GetLogger(ctx).WithFields(logrus.Fields{
		"rpc":      "icms.GetInstanceStatus",
		"endpoint": requestURL,
	})

	// fetch the service API key from the secret in the cluster instead
	jwt, err := c.tokenFetcher.FetchToken(ctx)
	if err != nil {
		return types.ICMSInstanceStatusResponse{}, err
	}

	req, err := c.newRequest(ctx, "GET", requestURL, nil)
	if err != nil {
		return types.ICMSInstanceStatusResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if jwt != "" {
		req.Header.Set("Authorization", "Bearer "+jwt)
	}

	log.Debug("Sending request to ICMS")

	resp, err := c.client.Do(req)
	if err != nil {
		log.WithError(err).Error("Do request")
		return types.ICMSInstanceStatusResponse{}, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(http.MaxBytesReader(nil, resp.Body, c.maxResponseBytes))
	if err != nil {
		log.WithError(err).Errorf("Read response")
		return types.ICMSInstanceStatusResponse{}, err
	}

	if err := c.checkResponse(log, resp, respBody); err != nil {
		return types.ICMSInstanceStatusResponse{}, err
	}

	var inStateRes types.ICMSInstanceStatusResponse
	if err := json.Unmarshal(respBody, &inStateRes); err != nil {
		return types.ICMSInstanceStatusResponse{},
			fmt.Errorf("decode response body (%v) as ICMSInstanceStatusResponse: %w", respBody, err)
	}

	log.WithFields(logrus.Fields{
		"response":   string(respBody),
		"serialized": fmt.Sprintf("%+v", inStateRes),
	}).Debug("Received response from ICMS")

	return inStateRes, nil
}

func (c *ICMSClient) Register(ctx context.Context, rreq *types.ICMSRegistrationRequest) (*types.ICMSRegistrationResponse, error) {
	requestURL, err := c.buildURL(c.nvcaPath(c.pathRegister, "register"))
	if err != nil {
		return nil, err
	}
	log := core.GetLogger(ctx).WithFields(logrus.Fields{
		"rpc":      "icms.RegisterV2",
		"endpoint": requestURL,
	})

	// fetch the service API key from the secret in the cluster instead
	jwt, err := c.tokenFetcher.FetchToken(ctx)
	if err != nil {
		return nil, err
	}

	data, err := json.Marshal(rreq)
	if err != nil {
		return nil, err
	}

	body := bytes.NewReader(data)
	req, err := c.newRequest(ctx, "PUT", requestURL, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if jwt != "" {
		req.Header.Set("Authorization", "Bearer "+jwt)
	}

	log.WithFields(logrus.Fields{
		"body": string(data),
	}).Debug("Sending request to ICMS")

	resp, err := c.client.Do(req)
	if err != nil {
		log.WithError(err).Error("Do request")
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(http.MaxBytesReader(nil, resp.Body, c.maxResponseBytes))
	if err != nil {
		log.WithError(err).Errorf("Read request")
		return nil, err
	}

	if err := c.checkResponse(log, resp, respBody); err != nil {
		return nil, err
	}

	rres := &types.ICMSRegistrationResponse{}
	if err := json.Unmarshal(respBody, &rres); err != nil {
		return nil, fmt.Errorf("decode queue credentials")
	}
	return rres, nil
}

func (c *ICMSClient) GetCreds(ctx context.Context) (*types.ICMSCredentialResponse, error) {
	requestURL, err := c.buildURL(c.nvcaPath(c.pathCredentials, "credentials"))
	if err != nil {
		return nil, err
	}
	log := core.GetLogger(ctx).WithFields(logrus.Fields{
		"rpc":      "icms.GetCreds",
		"endpoint": requestURL,
	})

	// fetch the service API key from the secret in the cluster instead
	jwt, err := c.tokenFetcher.FetchToken(ctx)
	if err != nil {
		return nil, err
	}

	req, err := c.newRequest(ctx, "GET", requestURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if jwt != "" {
		req.Header.Set("Authorization", "Bearer "+jwt)
	}

	log.Debug("Sending request to ICMS")

	resp, err := c.client.Do(req)
	if err != nil {
		log.WithError(err).Error("Do request")
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(http.MaxBytesReader(nil, resp.Body, c.maxResponseBytes))
	if err != nil {
		log.WithError(err).Errorf("Read response")
		return nil, err
	}

	if err := c.checkResponse(log, resp, respBody); err != nil {
		return nil, err
	}

	icmsCreds := &types.ICMSCredentialResponse{}
	if err := json.Unmarshal(respBody, &icmsCreds.QueueCredentials); err != nil {
		return nil, fmt.Errorf("decode queue credentials")
	}
	return icmsCreds, nil
}

func (c *ICMSClient) PutHealthStatus(ctx context.Context, hsr *types.HealthStatusRequest) (*types.HealthStatusResponse, error) {
	requestURL, err := c.buildURL(c.nvcaPath(c.pathHeartbeat, "heartbeat"))
	if err != nil {
		return nil, err
	}
	log := core.GetLogger(ctx).WithFields(logrus.Fields{
		"rpc":      "icms.HealthStatus",
		"endpoint": requestURL,
	})

	// fetch the service API key from the secret in the cluster instead
	jwt, err := c.tokenFetcher.FetchToken(ctx)
	if err != nil {
		return nil, err
	}

	data, err := json.Marshal(&hsr)
	if err != nil {
		return nil, err
	}

	body := bytes.NewReader(data)
	req, err := c.newRequest(ctx, "POST", requestURL, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if jwt != "" {
		req.Header.Set("Authorization", "Bearer "+jwt)
	}

	log.WithFields(logrus.Fields{
		"body": string(data),
	}).Debug("Sending request to ICMS")

	resp, err := c.client.Do(req)
	if err != nil {
		log.WithError(err).Error("Do request")
		return nil, err
	}
	defer resp.Body.Close()

	// Read response body to check for action instructions
	respBody, err := io.ReadAll(http.MaxBytesReader(nil, resp.Body, c.maxResponseBytes))
	if err != nil {
		log.WithError(err).Error("Read response body")
		return nil, err
	}

	if err := c.checkResponse(log, resp, respBody); err != nil {
		return nil, err
	}

	// Parse response for health status instructions
	var healthResponse types.HealthStatusResponse
	if len(respBody) > 0 {
		if err := json.Unmarshal(respBody, &healthResponse); err != nil {
			log.WithError(err).WithField("response", string(respBody)).Warn("Failed to parse health status response, continuing with default action")
			// Default to ACCEPTED if we can't parse the response
			healthResponse.Action = types.HealthActionAccepted
		}
	} else {
		// Default to ACCEPTED if no response body
		healthResponse.Action = types.HealthActionAccepted
	}

	log.WithFields(logrus.Fields{
		"action":   healthResponse.Action,
		"response": string(respBody),
	}).Debug("Received health status response from ICMS")

	return &healthResponse, nil
}

func (c *ICMSClient) PutRequestAcknowledgement(
	ctx context.Context,
	icmsReqID string,
	messageBatchID string,
	instanceCount uint64,
	srTraceCtxCfg nvcav2beta1.ICMSRequestTraceContextConfig,
) error {
	return nvcaotel.InvokeWithSpan(
		nvcaotel.ContextWithParentSpanFromICMS(ctx, srTraceCtxCfg),
		c.tracer,
		"nvca.ICMSClient.PutRequestAcknowledgement",
		func(ctx context.Context) error {
			return c.putRequestAcknowledgement(ctx, icmsReqID, messageBatchID, instanceCount)
		})
}

func (c *ICMSClient) putRequestAcknowledgement(ctx context.Context, requestID string,
	messageBatchID string, instanceCount uint64) error {
	requestURL, err := c.buildURL("%s/%s", c.pathSIRsPrefix, requestID)
	if err != nil {
		return err
	}
	log := core.GetLogger(ctx).WithFields(logrus.Fields{
		"rpc":      "icms.AcknowledgeRequest",
		"endpoint": requestURL,
		"reqId":    requestID,
	})

	// fetch the service API key from the secret in the cluster instead
	jwt, err := c.tokenFetcher.FetchToken(ctx)
	if err != nil {
		return err
	}

	aReq := types.ICMSAcknowledgeRequest{
		InstanceCount:  instanceCount,
		MessageBatchID: messageBatchID,
		Status:         types.ICMSRequestPending,
	}

	data, err := json.Marshal(&aReq)
	if err != nil {
		return err
	}

	body := bytes.NewReader(data)
	req, err := c.newRequest(ctx, "PUT", requestURL, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if jwt != "" {
		req.Header.Set("Authorization", "Bearer "+jwt)
	}

	log.WithFields(logrus.Fields{
		"body": string(data),
	}).Debug("Sending request to ICMS")

	resp, err := c.client.Do(req)
	if err != nil {
		log.WithError(err).Error("Do request")
		return err
	}
	defer resp.Body.Close()

	if err := c.checkResponse(log, resp, nil); err != nil {
		return err
	}

	return nil
}

func shouldIgnoreReturn(ret int) bool {
	switch ret {
	case http.StatusNotFound, http.StatusInternalServerError, http.StatusPreconditionFailed, http.StatusConflict:
		return true
	}
	return false
}

// normalizeInstanceStatusUpdateRequestForLegacyAPI preserves ICMS naming internally
// while converting actions back to the legacy wire format required by the current API.
func normalizeInstanceStatusUpdateRequestForLegacyAPI(
	ireq *types.ICMSInstanceStatusUpdateRequest,
) *types.ICMSInstanceStatusUpdateRequest {
	if ireq == nil {
		return nil
	}

	normalized := *ireq
	normalized.Action = toLegacyAction(normalized.Action)

	return &normalized
}

func (c *ICMSClient) PostInstanceStatusUpdate(ctx context.Context, requestID, instanceID string, ireq *types.ICMSInstanceStatusUpdateRequest) error {
	requestURL, err := c.buildURL("%s/%s/%s", c.pathSIRsPrefix, requestID, instanceID)
	if err != nil {
		return err
	}
	log := core.GetLogger(ctx).WithFields(logrus.Fields{
		"rpc":        "icms.InstanceStatusUpdateRequest",
		"endpoint":   requestURL,
		"reqId":      requestID,
		"instanceId": instanceID,
	})

	// error internally to prevent ICMS SLI violations
	if ireq.InstanceState == "" {
		return fmt.Errorf("payload to ICMS is invalid, instanceState is empty")
	}

	// fetch the service API key from the secret in the cluster instead
	jwt, err := c.tokenFetcher.FetchToken(ctx)
	if err != nil {
		return err
	}

	data, err := json.Marshal(normalizeInstanceStatusUpdateRequestForLegacyAPI(ireq))
	if err != nil {
		return err
	}

	body := bytes.NewReader(data)
	req, err := c.newRequest(ctx, "POST", requestURL, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if jwt != "" {
		req.Header.Set("Authorization", "Bearer "+jwt)
	}

	log.WithFields(logrus.Fields{
		"body": string(data),
	}).Debug("Sending request to ICMS")

	resp, err := c.client.Do(req)
	if err != nil {
		log.WithError(err).Error("Do request")
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if ireq.InstanceState == types.ICMSInstanceTerminated && shouldIgnoreReturn(resp.StatusCode) {
			log.Debugf("ignoring %v error for instance %v already reported as terminated", resp.StatusCode, instanceID)
			return nil
		}

		c.logResponseError(log, resp, nil)
		return nvcaerrors.HTTPStatusError(resp.StatusCode, fmt.Errorf("error response from ICMS: %s", resp.Status))
	}

	return nil
}
