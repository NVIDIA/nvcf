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

package upload

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/samber/lo"
	"github.com/valyala/bytebufferpool"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-task/internal/metrics"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-task/internal/secrets"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-task/pkg/ngc"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/utils"
)

const (
	maxFileUploadConcurrency  = 4 // Max number of files to upload in parallel
	maxChunkUploadConcurrency = 8 // Max number of chunks to upload in parallel for a single file
)

type Uploader interface {
	Submit(ctx context.Context, resultPath string, modelVersion string)
	Wait() error
}

type NgcUploader struct {
	Uploader
	model          string
	secrets        *secrets.Secrets
	ngcClient      *ngc.NgcClient
	workers        errgroup.Group
	abortedUploads bool
}

func NewUploader(
	ctx context.Context,
	resultsLocation, ngcBaseUrl string,
	clientSecrets *secrets.Secrets,
) (*NgcUploader, error) {
	// The valid resultsLocation would be one of:
	// <org>/<model> or
	// <org>/<team>/<model>
	var org, team, model string
	ngcData := strings.Split(resultsLocation, "/")
	if len(ngcData) == 2 {
		org = ngcData[0]
		model = ngcData[1]
	} else if len(ngcData) == 3 {
		org = ngcData[0]
		team = ngcData[1]
		model = ngcData[2]
	} else {
		return nil, fmt.Errorf("invalid results upload location")
	}

	ngcClient, err := ngc.NewClient(org, team, ngcBaseUrl)
	if err != nil {
		return nil, err
	}

	uploader := &NgcUploader{
		model:          model,
		ngcClient:      ngcClient,
		workers:        errgroup.Group{},
		secrets:        clientSecrets,
		abortedUploads: false,
	}

	return uploader, nil
}

// Zip result folder and upload it to NGC model registry.
func (u *NgcUploader) UploadResult(ctx context.Context, resultPath, version string) error {
	logger := zap.L().With(
		zap.String("model name", u.model),
		zap.String("model version", version),
	)

	logger.Info("Uploading result to NGC")

	if err := u.ngcClient.CreateModel(ctx, u.secrets.NgcApiKey(), u.model); err != nil {
		return err
	}

	if err := u.ngcClient.CreateModelVersion(ctx, u.secrets.NgcApiKey(), u.model, version); err != nil {
		return err
	}

	eg, egCtx := errgroup.WithContext(ctx)
	eg.SetLimit(maxFileUploadConcurrency)

	// Walk through the result folder and upload each file to NGC while maintaining the dir structure
	if err := filepath.Walk(resultPath, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if egCtx.Err() != nil {
			return egCtx.Err()
		}

		if info.IsDir() {
			return nil
		}

		relPath, err := filepath.Rel(resultPath, path)
		if err != nil {
			return err
		}

		eg.Go(func() error {
			return u.uploadResultFile(egCtx, version, path, relPath, info.Size())
		})

		return nil
	}); err != nil {
		return err
	}

	if err := eg.Wait(); err != nil {
		return err
	}

	// Update model status to complete
	return u.ngcClient.UpdateModel(ctx, u.secrets.NgcApiKey(), u.model, version)
}

// Submit an async upload job to worker.
func (u *NgcUploader) Submit(ctx context.Context, resultPath string, modelVersion string) {
	logger := zap.L().With(zap.String("result name", modelVersion))
	logger.Info("Uploading result")
	u.workers.Go(func() error {
		if err := u.UploadResult(ctx, resultPath, modelVersion); err != nil {
			logger.Error("failed to upload result", zap.Error(err))
			metrics.ResultUploadFailureCounter.Inc()
			return err
		}
		return nil
	})
}

// Wait for all upload worker to complete and returns error of failed uploads.
func (u *NgcUploader) Wait() error {
	zap.L().Info("Waiting for all upload jobs to complete")
	if err := u.workers.Wait(); err != nil {
		zap.L().Error("Some uploads failed", zap.Error(err))
		return err
	}
	zap.L().Info("All upload jobs complete")
	return nil
}

// Upload a single result file to NGC model registry
func (u *NgcUploader) uploadResultFile(ctx context.Context, modelVersion, localPath, remotePath string, size int64) error {
	logger := zap.L().With(
		zap.String("model name", u.model),
		zap.String("model version", modelVersion),
		zap.String("model file path", remotePath),
	)

	logger.Info("Uploading file")

	chunkSize, chunkCount := getChunk(size)
	logger.Debug("Requesting multi-part upload", zap.Int64("customPartSize", chunkSize))
	modelUploadData, err := u.ngcClient.StartModelUpload(ctx, u.secrets.NgcApiKey(), u.model, modelVersion, remotePath, size, chunkSize)
	if err != nil {
		return err
	}

	uploadId := modelUploadData.UploadId
	presignedUrls := modelUploadData.Urls
	abortUpload := false
	defer func() {
		if !abortUpload {
			return
		}
		if abortErr := u.ngcClient.AbortModelUpload(
			context.Background(), u.secrets.NgcApiKey(), u.model, modelVersion, remotePath, uploadId,
		); abortErr != nil {
			zap.L().Error("failed to abort model upload", zap.Error(abortErr))
		}
		u.abortedUploads = true
	}()

	// Check if the count of presigned urls matches the count of chunks
	if len(presignedUrls) != chunkCount {
		abortUpload = true
		return fmt.Errorf("got %d presigned urls from NGC, expect %d", len(presignedUrls), chunkCount)
	}

	// Start async multi part upload to given presigned urls from NGC
	bufferChan := make(chan *bytebufferpool.ByteBuffer, maxChunkUploadConcurrency)
	asyncUpload := lo.Async(func() error {
		eg, ctx := errgroup.WithContext(ctx)
		eg.SetLimit(maxChunkUploadConcurrency)
		partNumberIter := 0
		for buffer := range bufferChan {
			buffer := buffer
			partNumber := partNumberIter
			presignedUrl := presignedUrls[partNumber]

			eg.Go(func() error {
				logger.Debug("Uploading chunk", zap.Int("part number", partNumber))
				defer bytebufferpool.Put(buffer)

				if err := u.ngcClient.UploadModel(ctx, presignedUrl, buffer.Bytes()); err != nil {
					return err
				}

				logger.Debug("Complete uploading chunk", zap.Int("part number", partNumber))
				return nil
			})

			partNumberIter += 1
		}
		return eg.Wait()
	})

	f, err := os.Open(localPath)
	if err != nil {
		abortUpload = true
		return err
	}
	defer f.Close()

	// Read chunks from file for uploads
	h := sha256.New()
	content := io.TeeReader(f, h)
	_, err = utils.MultipartReadToBuffer(content, bufferChan, chunkSize)
	close(bufferChan)
	if err != nil {
		abortUpload = true
		return err
	}

	if err = <-asyncUpload; err != nil {
		abortUpload = true
		return err
	}

	// Calculate sha256 digest and complete the upload
	sha256Digest := h.Sum(nil)
	encodedDigest := base64.StdEncoding.EncodeToString(sha256Digest)

	if err := u.ngcClient.CompleteModelUpload(
		ctx, u.secrets.NgcApiKey(), u.model, modelVersion, remotePath, encodedDigest, uploadId,
	); err != nil {
		abortUpload = true
		return err
	}

	logger.Info("Complete uploading file")
	metrics.ResultUploadBytesCounter.Add(float64(size))

	return nil
}

// Decide on upload chunk size and count based on input file size.
func getChunk(fileSizeInBytes int64) (chunkSize int64, chunkCount int) {
	// Requirement from S3: total file size / chunk size <= 10,000
	switch {
	case fileSizeInBytes <= 50*utils.OneGB:
		chunkSize = 5 * utils.OneMB
	case fileSizeInBytes <= 100*utils.OneGB:
		chunkSize = 10 * utils.OneMB
	case fileSizeInBytes <= 150*utils.OneGB:
		chunkSize = 20 * utils.OneMB
	case fileSizeInBytes <= 350*utils.OneGB:
		chunkSize = 40 * utils.OneMB
	default:
		// Very large file, assuming it is running in a large cluster, use 250MB chunks
		chunkSize = 250 * utils.OneMB
	}

	chunkCount = int(math.Ceil(float64(fileSizeInBytes) / float64(chunkSize)))

	return chunkSize, chunkCount
}
