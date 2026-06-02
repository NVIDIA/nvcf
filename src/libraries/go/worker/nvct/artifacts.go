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

	"github.com/cenkalti/backoff/v4"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/auth"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvct"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/types"
)

// ------------------------------------------------------------------------

// Get artifacts list from NVCT
func (c *Client) GetArtifacts(ctx context.Context) (*types.ArtifactsList, error) {
	zap.L().Info("Fetching artifacts list from NVCT")

	var response *pb.ArtifactsResponse

	retryErr := backoff.Retry(func() error {
		var err error
		response, err = c.Client.GetArtifacts(ctx, &pb.ArtifactsRequest{
			TaskId: c.taskId,
		}, auth.GrpcTokenFromSource(c.NvctTokenProvider))
		if err != nil {
			zap.L().Warn("failed to get artifacts from NVCT", zap.Error(err))
		}
		return err
	}, backoff.WithContext(backoff.WithMaxRetries(backoff.NewExponentialBackOff(), 10), ctx))

	if retryErr != nil {
		return nil, retryErr
	}

	artifactsList := types.ArtifactsList{
		Models:    []types.Artifact{},
		Resources: []types.Artifact{},
	}

	for _, a := range response.GetArtifacts() {
		for _, f := range a.GetFiles() {
			artifact := types.Artifact{
				Name:    a.GetName(),
				Version: a.GetVersion(),
				Path:    f.GetPath(),
				Url:     f.GetUrl(),
			}

			switch a.GetKind() {
			case pb.ArtifactsResponse_ArtifactResponse_MODEL:
				artifactsList.Models = append(artifactsList.Models, artifact)
			case pb.ArtifactsResponse_ArtifactResponse_RESOURCE:
				artifactsList.Resources = append(artifactsList.Resources, artifact)
			default:
				zap.L().Warn(
					"invalid artifact kind",
					zap.String("name", a.GetName()),
					zap.String("kind", a.GetKind().String()),
				)
			}
		}
	}

	return &artifactsList, nil
}
