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

package ngc

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/clients"
	"github.com/cenkalti/backoff/v4"
	"github.com/goccy/go-json"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-task/internal/metrics"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/utils"
)

type NgcClient struct {
	ngcUrl string
	client *clients.HTTPClient
}

func NewClient(org, team, baseUrl string) (*NgcClient, error) {
	ngcUrl, err := url.JoinPath(baseUrl, "v2/org", org)
	if err != nil {
		return nil, err
	}

	if team != "" {
		ngcUrl, err = url.JoinPath(baseUrl, "v2/org", org, "team", team)
		if err != nil {
			return nil, err
		}
	}

	client, err := clients.DefaultHTTPClient(
		&clients.HTTPClientConfig{
			BaseClientConfig: &clients.BaseClientConfig{
				Addr: ngcUrl,
			},
			NumRetries: 3,
		},
		func(_ string, r *http.Request) string {
			return r.URL.Path
		},
	)
	if err != nil {
		return nil, err
	}

	return &NgcClient{
		ngcUrl: ngcUrl,
		client: client,
	}, nil
}

func (c *NgcClient) CreateModel(ctx context.Context, apiKey, modelName string) error {
	logger := zap.L().With(zap.String("model name", modelName))
	logger.Info("creating model")

	modelUrl, err := url.JoinPath(c.ngcUrl, "models")
	if err != nil {
		return err
	}

	reqData := ModelCreateRequest{
		Name: modelName,
	}

	if err := backoff.Retry(func() error {
		resp, err := c.sendJsonRequest(ctx, reqData, modelUrl, apiKey, http.MethodPost)
		if err != nil {
			return err
		}
		defer utils.Close(resp.Body.Close)

		recordResponseCode("create_model", resp.StatusCode)

		if resp.StatusCode == http.StatusConflict {
			logger.Warn("model already exists")
			return nil
		}

		if resp.StatusCode != http.StatusOK {
			resBody, err := io.ReadAll(resp.Body)
			if err != nil {
				return err
			}
			return fmt.Errorf("failed to create model - status: %d, response: %s", resp.StatusCode, string(resBody))
		}

		return nil
	}, backoff.WithContext(
		backoff.WithMaxRetries(backoff.NewExponentialBackOff(), 5), ctx,
	)); err != nil {
		return err
	}

	logger.Info("successfully created model")

	return nil
}

func (c *NgcClient) CreateModelVersion(ctx context.Context, apiKey, modelName, versionId string) error {
	logger := zap.L().With(zap.String("model name", modelName), zap.String("model version", versionId))
	logger.Info("creating model version")

	modelUrl, err := url.JoinPath(c.ngcUrl, "models", modelName, "versions")
	if err != nil {
		return err
	}

	reqData := ModelVersionCreateRequest{
		VersionId: versionId,
	}

	if err := backoff.Retry(func() error {
		resp, err := c.sendJsonRequest(ctx, reqData, modelUrl, apiKey, http.MethodPost)
		if err != nil {
			return err
		}
		defer utils.Close(resp.Body.Close)

		recordResponseCode("create_model_version", resp.StatusCode)

		if resp.StatusCode == http.StatusConflict {
			logger.Warn("model version already exists")
			return nil
		}

		if resp.StatusCode != http.StatusOK {
			resBody, err := io.ReadAll(resp.Body)
			if err != nil {
				return err
			}
			return fmt.Errorf("failed to create version - status: %d, response: %s", resp.StatusCode, string(resBody))
		}

		return nil
	}, backoff.WithContext(
		backoff.WithMaxRetries(backoff.NewExponentialBackOff(), 5), ctx,
	)); err != nil {
		return err
	}

	logger.Info("successfully created version")

	return nil
}

func (c *NgcClient) StartModelUpload(
	ctx context.Context,
	apiKey string,
	modelName string,
	versionId string,
	filePath string,
	fileSize int64,
	customPartSize int64,
) (*ModelUploadResponse, error) {
	logger := zap.L().With(
		zap.String("model name", modelName),
		zap.String("model version", versionId),
		zap.String("model file", filePath),
	)
	logger.Info("initiating model file upload")

	modelUrl, err := url.JoinPath(c.ngcUrl, "files/multipart")
	if err != nil {
		return nil, err
	}

	reqData := ModelUploadRequest{
		Name:           modelName,
		Version:        versionId,
		ArtifactType:   "model",
		FilePath:       filePath,
		Size:           fileSize,
		CustomPartSize: customPartSize,
	}

	resData := new(ModelUploadResponse)
	if err := backoff.Retry(func() error {
		resp, err := c.sendJsonRequest(ctx, reqData, modelUrl, apiKey, http.MethodPost)
		if err != nil {
			return err
		}
		defer utils.Close(resp.Body.Close)

		recordResponseCode("initiate_model_upload", resp.StatusCode)

		resBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return err
		}

		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("failed to initiate model upload - status: %d, response: %s", resp.StatusCode, string(resBody))
		}

		if err = json.Unmarshal(resBody, resData); err != nil {
			return err
		}

		return nil
	}, backoff.WithContext(
		backoff.WithMaxRetries(backoff.NewExponentialBackOff(), 5), ctx,
	)); err != nil {
		return nil, err
	}

	logger.Info("successfully initiate model file upload")

	return resData, nil
}

func (c *NgcClient) UploadModel(ctx context.Context, presignedUrl string, data []byte) error {
	if err := backoff.Retry(func() error {
		resp, err := c.client.Put(ctx, presignedUrl, bytes.NewReader(data))
		if err != nil {
			return err
		}
		defer utils.Close(resp.Body.Close)

		recordResponseCode("upload_model_chunks", resp.StatusCode)

		if resp.StatusCode != http.StatusOK {
			resBody, err := io.ReadAll(resp.Body)
			if err != nil {
				return err
			}
			return fmt.Errorf("failed to upload file - status: %d, response: %s", resp.StatusCode, string(resBody))
		}

		return nil
	}, backoff.WithContext(
		backoff.WithMaxRetries(backoff.NewExponentialBackOff(), 5), ctx,
	)); err != nil {
		return err
	}

	return nil
}

func (c *NgcClient) CompleteModelUpload(
	ctx context.Context,
	apiKey string,
	modelName string,
	versionId string,
	filePath string,
	fileHash string,
	uploadId string,
) error {
	logger := zap.L().With(
		zap.String("model name", modelName),
		zap.String("model version", versionId),
		zap.String("model file", filePath),
	)
	logger.Info("completing model upload")

	modelUrl, err := url.JoinPath(c.ngcUrl, "files/multipart")
	if err != nil {
		return err
	}

	reqData := CompleteModelUploadRequest{
		Name:         modelName,
		Version:      versionId,
		ArtifactType: "model",
		FilePath:     filePath,
		FileHash:     fileHash,
		UploadId:     uploadId,
	}

	if err := backoff.Retry(func() error {
		resp, err := c.sendJsonRequest(ctx, reqData, modelUrl, apiKey, http.MethodPut)
		if err != nil {
			return err
		}
		defer utils.Close(resp.Body.Close)

		recordResponseCode("complete_model_upload", resp.StatusCode)

		if resp.StatusCode != http.StatusOK {
			resBody, err := io.ReadAll(resp.Body)
			if err != nil {
				return err
			}
			return fmt.Errorf("failed to complete model upload - status: %d, response: %s", resp.StatusCode, string(resBody))
		}

		return nil
	}, backoff.WithContext(
		backoff.WithMaxRetries(backoff.NewExponentialBackOff(), 5), ctx,
	)); err != nil {
		return err
	}

	logger.Info("successfully completed model upload")

	return nil
}

func (c *NgcClient) AbortModelUpload(
	ctx context.Context,
	apiKey string,
	modelName string,
	versionId string,
	filePath string,
	uploadId string,
) error {
	logger := zap.L().With(
		zap.String("model name", modelName),
		zap.String("model version", versionId),
		zap.String("model file", filePath),
	)
	logger.Info("aborting model upload")

	modelUrl, err := url.JoinPath(c.ngcUrl, "/files/multipart")
	if err != nil {
		return err
	}

	reqData := AbortModelUploadRequest{
		Name:         modelName,
		Version:      versionId,
		ArtifactType: "model",
		FilePath:     filePath,
		UploadId:     uploadId,
	}

	if err := backoff.Retry(func() error {
		resp, err := c.sendJsonRequest(ctx, reqData, modelUrl, apiKey, http.MethodDelete)
		if err != nil {
			return err
		}
		defer utils.Close(resp.Body.Close)

		recordResponseCode("abort_model_upload", resp.StatusCode)

		if resp.StatusCode != http.StatusOK {
			resBody, err := io.ReadAll(resp.Body)
			if err != nil {
				return err
			}
			return fmt.Errorf("failed to abort model upload - status: %d, response: %s", resp.StatusCode, string(resBody))
		}

		return nil
	}, backoff.WithContext(
		backoff.WithMaxRetries(backoff.NewExponentialBackOff(), 5), ctx,
	)); err != nil {
		return err
	}

	logger.Info("successfully aborted model upload")

	return nil
}

func (c *NgcClient) UpdateModel(
	ctx context.Context,
	apiKey string,
	modelName string,
	versionId string,
) error {
	logger := zap.L().With(
		zap.String("model name", modelName),
		zap.String("model version", versionId),
	)
	logger.Info("marking model upload as completed")
	url, err := url.JoinPath(c.ngcUrl, "models", modelName, "versions", versionId)
	if err != nil {
		return err
	}

	reqData := UpdateModelRequest{
		Status: "UPLOAD_COMPLETE",
	}

	if err := backoff.Retry(func() error {
		resp, err := c.sendJsonRequest(ctx, reqData, url, apiKey, http.MethodPatch)
		if err != nil {
			return err
		}
		defer utils.Close(resp.Body.Close)

		recordResponseCode("update_model", resp.StatusCode)

		if resp.StatusCode != http.StatusOK {
			resBody, err := io.ReadAll(resp.Body)
			if err != nil {
				return err
			}
			return fmt.Errorf("failed to update model version - status: %d, response: %s", resp.StatusCode, string(resBody))
		}

		return nil
	}, backoff.WithContext(
		backoff.WithMaxRetries(backoff.NewExponentialBackOff(), 5), ctx,
	)); err != nil {
		return err
	}

	logger.Info("successfully updated model status to complete")

	return nil
}

func (c *NgcClient) sendJsonRequest(ctx context.Context, v any, url, apiKey, method string) (*http.Response, error) {
	reqBody, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", apiKey))

	return c.client.Client(ctx).Do(req)
}

func recordResponseCode(action string, statusCode int) {
	metrics.NgcClientResponseCodeCounter.WithLabelValues(strconv.Itoa(statusCode), action).Inc()
}
