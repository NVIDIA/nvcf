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
	"context"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/auth"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvcf"
	"io"
	"time"

	"golang.org/x/sync/errgroup"
)

const preSignedUrlExpiry = 30 * time.Minute

func (w *NVCFWorker) getValidLargeResponseUrl(ctx context.Context, work *pb.WorkerInvokeFunctionRequest, forceRefresh bool) (string, error) {
	// assume pre-signed urls have at least half an hour.
	// we can't try the upload and check for failure because then we'd have to potentially
	// buffer and replay the upload body for retries
	if !forceRefresh && work.LargeResponseUrl != "" && time.Since(work.RequestTime.AsTime()) <= preSignedUrlExpiry {
		return work.LargeResponseUrl, nil
	}

	// need regional client because the request id info is only in the originating region
	client, err := w.nvcfClient.GetRegionalNvcfClient(work.DirectResponseUrl)
	if err != nil {
		return "", err
	}
	credentials, err := client.RefreshLargeUploadCredentials(ctx, &pb.RefreshLargeUploadCredentialsRequest{
		RequestId:         work.RequestId,
		FunctionId:        w.config.FunctionId,
		FunctionVersionId: w.config.FunctionVersionId,
	}, auth.GrpcTokenFromSource(w.nvcfClient.NvcfTokenProvider))
	if err != nil {
		return "", err
	}
	return credentials.LargeResponseUrl, nil
}

func (w *NVCFWorker) getValidAssets(ctx context.Context, work *pb.WorkerInvokeFunctionRequest) ([]*pb.InputAssetReference, error) {
	if len(work.InputAssetReference) == 0 || time.Since(work.RequestTime.AsTime()) <= preSignedUrlExpiry {
		return work.InputAssetReference, nil
	}

	// need regional client because the request id info is only in the originating region
	client, err := w.nvcfClient.GetRegionalNvcfClient(work.DirectResponseUrl)
	if err != nil {
		return nil, err
	}

	group, ctx := errgroup.WithContext(ctx)

	refreshedInputAssets := make([]*pb.InputAssetReference, 0, len(work.InputAssetReference))
	requestStream, err := client.RefreshAssetDownloadCredentials(ctx, auth.GrpcTokenFromSource(w.nvcfClient.NvcfTokenProvider))
	if err != nil {
		return nil, err
	}
	group.Go(func() error {
		for _, inputAsset := range work.InputAssetReference {
			err := requestStream.Send(&pb.RefreshAssetDownloadCredentialsRequest{
				RequestId:         work.RequestId,
				FunctionId:        w.config.FunctionId,
				FunctionVersionId: w.config.FunctionVersionId,
				AssetId:           inputAsset.AssetId,
			})
			if err != nil {
				return err
			}
		}
		return requestStream.CloseSend()
	})
	group.Go(func() error {
		for {
			inputAsset, err := requestStream.Recv()
			if err != nil {
				if err == io.EOF {
					return nil
				}
				return err
			}
			refreshedInputAssets = append(refreshedInputAssets, inputAsset.InputAssetReference)
		}
	})
	err = group.Wait()
	if err != nil {
		return nil, err
	}
	return refreshedInputAssets, nil
}
