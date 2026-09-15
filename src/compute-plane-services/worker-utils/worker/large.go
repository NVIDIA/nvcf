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

package worker

import (
	"bytes"
	"context"
	"fmt"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-utils/worker/metrics"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/auth"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/metering"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvcf"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/utils"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/cenkalti/backoff/v4"
	"github.com/klauspost/compress/zip"
	"github.com/samber/lo"
	"github.com/valyala/bytebufferpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/errgroup"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const (
	maxPartSize                = 20 * utils.OneMB // must be 10-100mb inclusive.
	multipartUploadConcurrency = 8
)

// largeResponse copies data from buf first then reads the rest of the data from res into a zip
// file constructed on the fly as it's being read by the http client
func (w *NVCFWorker) largeResponse(ctx context.Context, work *pb.WorkerInvokeFunctionRequest, body io.Reader, responseDir string, artifacts []string, meteringEvent *metering.EventDetails) (*http.Response, error) {
	ctx, span := otel.GetTracerProvider().Tracer("nvcf-worker-utils").
		Start(ctx, "Large Response", trace.WithSpanKind(trace.SpanKindInternal))
	defer span.End()

	// fire this off early so we introduce as little latency as possible by fetching the download url while we upload the response
	downloadUrl := lo.Async2(func() (string, error) {
		// fetch the large response download url so we can construct a 302 response
		response, err := w.nvcfClient.Client.RequestLargeResponseDownloadCredentials(ctx, &pb.LargeResponseDownloadCredentialsRequest{
			FunctionId:        w.config.FunctionId,
			FunctionVersionId: w.config.FunctionVersionId,
			RequestId:         work.RequestId,
			NcaId:             work.NcaId,
		}, auth.GrpcTokenFromSource(w.nvcfClient.NvcfTokenProvider))
		if err != nil {
			return "", fmt.Errorf("failed to fetch large response download url: %w", err)
		}
		return response.LargeResponseDownloadUrl, nil
	})

	client := w.workerRestClient.Client(ctx)
	// pipe is used so we can write to an http request body rather than the http request reading from a buffer
	pipeReader, pipeWriter := io.Pipe()
	defer pipeReader.Close()
	zipper := lo.Async(func() (err error) {
		defer func() {
			if closeErr := pipeWriter.Close(); err == nil && closeErr != nil {
				err = closeErr
			}
		}()
		writer := zip.NewWriter(pipeWriter)

		// write response to zip
		var responseFile io.Writer
		responseFile, err = writer.Create(work.RequestId + ".response")
		if err != nil {
			return
		}
		_, err = io.Copy(responseFile, body)
		if err != nil {
			_ = writer.Close()
			return
		}

		// add additional files to zip
		for _, artifact := range artifacts {
			var artifactFile *os.File
			artifactFile, err = os.Open(artifact)
			if err != nil {
				_ = writer.Close()
				return
			}

			var artifactRelPath string
			artifactRelPath, err = filepath.Rel(responseDir, artifact)
			if err != nil {
				_ = artifactFile.Close()
				_ = writer.Close()
				return
			}

			var artifactWriter io.Writer
			artifactWriter, err = writer.Create(artifactRelPath)
			if err != nil {
				_ = artifactFile.Close()
				_ = writer.Close()
				return
			}

			_, err = io.Copy(artifactWriter, artifactFile)
			_ = artifactFile.Close()
			if err != nil {
				_ = writer.Close()
				return
			}
		}

		err = writer.Close()
		return
	})

	bufferedZip := bytebufferpool.Get()
	defer bytebufferpool.Put(bufferedZip)

	_, err := io.CopyN(bufferedZip, pipeReader, maxPartSize)

	// no error means we copied maxPartSize. we'll use multipart upload for sizes maxPartSize and up.
	if err == nil {
		// re-combine the already read buffer back with the rest of the body
		body := io.MultiReader(bytes.NewReader(bufferedZip.Bytes()), pipeReader)
		err := w.multipartUpload(ctx, work, body, meteringEvent)
		if err != nil {
			return nil, err
		}
		err = <-zipper
		if err != nil {
			return nil, fmt.Errorf("failed to fully read and zip large file: %w", err)
		}
		return finishedLargeResponse(downloadUrl)
	}

	// check for actual error aside from EOF.
	// EOF means we consumed the body without reading more than maxPartSize.
	if err != io.EOF {
		return nil, err
	}

	err = <-zipper
	if err != nil {
		return nil, fmt.Errorf("failed to zip large file: %w", err)
	}

	// we have less than maxPartSize to upload, so we'll use a PUT to a pre-signed url
	largeResponseUrl, err := w.getValidLargeResponseUrl(ctx, work, false)
	if err != nil {
		return nil, err
	}

	zipLen := int64(bufferedZip.Len())
	uploadStart := time.Now()
	if err = backoff.Retry(func() error {
		request, err := http.NewRequestWithContext(ctx, http.MethodPut, largeResponseUrl, bytes.NewReader(bufferedZip.Bytes()))
		if err != nil {
			return err
		}
		request.Header.Set("Content-Type", "application/zip")

		response, err := client.Do(request)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			if response.StatusCode == http.StatusBadRequest {
				// Refresh presigned url when getting 400 from AWS
				newUrl, err := w.getValidLargeResponseUrl(ctx, work, true)
				if err != nil {
					return fmt.Errorf("failed to get new presigned url: %w", err)
				}
				largeResponseUrl = newUrl
			}
			errBody, _ := io.ReadAll(response.Body)
			return fmt.Errorf("bad response from large response upload %d: %s", response.StatusCode, string(errBody))
		}
		return nil
	}, backoff.WithContext(backoff.WithMaxRetries(
		backoff.NewExponentialBackOff(
			backoff.WithInitialInterval(100*time.Millisecond)), 3), ctx,
	)); err != nil {
		return nil, err
	}

	recordUploadStats(uploadStart, meteringEvent, zipLen)

	return finishedLargeResponse(downloadUrl)
}

func finishedLargeResponse(downloadUrlChan <-chan lo.Tuple2[string, error]) (*http.Response, error) {
	downloadUrl, err := (<-downloadUrlChan).Unpack()
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: http.StatusFound,
		Header: http.Header{
			"Location": []string{downloadUrl},
		},
		Body: http.NoBody,
	}, nil
}

func recordUploadStats(uploadStart time.Time, meteringEvent *metering.EventDetails, totalSize int64) {
	uploadTimeSeconds := time.Since(uploadStart).Seconds()
	meteringEvent.InferenceSize += totalSize
	metrics.LargeResponseCounter.Inc()
	metrics.LargeResponseBytesCounter.Add(float64(totalSize))
	metrics.LargeResponseTimeCounter.Add(uploadTimeSeconds)
}

func (w *NVCFWorker) multipartUpload(ctx context.Context, work *pb.WorkerInvokeFunctionRequest, body io.Reader, meteringEvent *metering.EventDetails) error {
	// need regional client because the request id info is only in the originating region
	nvcfClient, err := w.nvcfClient.GetRegionalNvcfClient(work.DirectResponseUrl)
	if err != nil {
		return fmt.Errorf("failed to get regional nvcf client for multipart upload: %w", err)
	}
	mpCredsResponse, err := nvcfClient.MultipartLargeUploadCredentials(ctx, &pb.MultipartLargeUploadCredentialsRequest{
		RequestId:         work.RequestId,
		FunctionId:        w.config.FunctionId,
		FunctionVersionId: w.config.FunctionVersionId,
	}, auth.GrpcTokenFromSource(w.nvcfClient.NvcfTokenProvider))
	if err != nil {
		return fmt.Errorf("failed to fetch multipart credentials %w", err)
	}
	httpClient := w.workerRestClient.Client(ctx)
	client := s3.New(s3.Options{
		Credentials: aws.CredentialsProviderFunc(func(ctx context.Context) (aws.Credentials, error) {
			return aws.Credentials{
				AccessKeyID:     mpCredsResponse.Credentials.AccessKeyId,
				SecretAccessKey: mpCredsResponse.Credentials.SecretAccessKey,
				SessionToken:    mpCredsResponse.Credentials.SessionToken,
			}, nil
		}),
		Region:           mpCredsResponse.Region,
		RetryMaxAttempts: 3,
		UseAccelerate:    true,
		HTTPClient:       httpClient,
	})

	input := &s3.CreateMultipartUploadInput{
		Bucket:      &mpCredsResponse.Bucket,
		Key:         &mpCredsResponse.Key,
		ContentType: aws.String("application/zip"),
	}

	uploadStart := time.Now()
	resp, err := client.CreateMultipartUpload(ctx, input)
	if err != nil {
		return err
	}

	completedPartsLock := sync.Mutex{}
	var completedParts []types.CompletedPart

	bufferChan := make(chan *bytebufferpool.ByteBuffer, multipartUploadConcurrency)

	asyncUpload := lo.Async(func() error {
		group, ctx := errgroup.WithContext(ctx)
		partNumberIter := int32(1)
		for buffer := range bufferChan {
			buffer := buffer
			partNumber := partNumberIter
			group.Go(func() error {
				defer bytebufferpool.Put(buffer)
				partInput := &s3.UploadPartInput{
					Body:       bytes.NewReader(buffer.Bytes()),
					Bucket:     resp.Bucket,
					Key:        resp.Key,
					PartNumber: &partNumber,
					UploadId:   resp.UploadId,
				}

				var uploadResult *s3.UploadPartOutput
				_, _, err = lo.AttemptWithDelay(3, 100*time.Millisecond, func(index int, duration time.Duration) error {
					var err error
					uploadResult, err = client.UploadPart(ctx, partInput)
					return err
				})
				if err != nil {
					return err
				}

				completedPartsLock.Lock()
				completedParts = append(completedParts, types.CompletedPart{
					ETag:       uploadResult.ETag,
					PartNumber: &partNumber,
				})
				completedPartsLock.Unlock()
				return nil
			})
			partNumberIter++
		}
		return group.Wait()
	})

	// read zip body into buffers of up to maxPartSize and send them for parallel upload
	totalSize, err := utils.MultipartReadToBuffer(body, bufferChan, maxPartSize)
	close(bufferChan)
	if err != nil {
		abortErr := abortUpload(ctx, resp, client)
		if abortErr != nil {
			return abortErr
		}
		return err
	}

	err = <-asyncUpload
	if err != nil {
		abortErr := abortUpload(ctx, resp, client)
		if abortErr != nil {
			return abortErr
		}
		return err
	}
	// TODO grow the slice as needed and place the completed parts directly in their ordered location instead of sorting afterwards
	sort.Slice(completedParts, func(i, j int) bool {
		return *completedParts[i].PartNumber < *completedParts[j].PartNumber
	})
	_, err = client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:   resp.Bucket,
		Key:      resp.Key,
		UploadId: resp.UploadId,
		MultipartUpload: &types.CompletedMultipartUpload{
			Parts: completedParts,
		},
	})
	if err != nil {
		return err
	}
	recordUploadStats(uploadStart, meteringEvent, totalSize)
	return nil
}

func abortUpload(ctx context.Context, resp *s3.CreateMultipartUploadOutput, client *s3.Client) error {
	_, err := client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket:   resp.Bucket,
		Key:      resp.Key,
		UploadId: resp.UploadId,
	})
	return err
}
