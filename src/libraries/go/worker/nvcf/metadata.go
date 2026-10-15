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

package nvcf

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/cenkalti/backoff/v4"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/auth"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvcf"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/token"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/utils"
)

var MetadataCredsTokenFile = "/var/run/nvcf/info/self"

// ------------------------------------------------------------------------

func (c *Client) StartMetadataCredentialsRefresher(ctx context.Context) {
	token.StartTokenRefresher(ctx, "metadata credentials", true,
		c.getMetadataCredentials, c.metadataCredentialsCallback)
}

// ------------------------------------------------------------------------

func (c *Client) getMetadataCredentials(ctx context.Context) (token.Token, error) {
	if ctx.Err() != nil {
		return token.Token{}, ctx.Err()
	}

	var mc *pb.FunctionMetadataCredentialsResponse
	err := backoff.Retry(func() error {
		var err error
		mc, err = c.Client.RequestFunctionMetadataCredentials(ctx,
			&pb.FunctionMetadataCredentialsRequest{
				NcaId:             c.ncaId,
				FunctionId:        c.functionId,
				FunctionVersionId: c.functionVersionId,
			}, auth.GrpcTokenFromSource(c.NvcfTokenProvider))
		return err
	}, backoff.WithContext(backoff.WithMaxRetries(backoff.NewExponentialBackOff(), 10), ctx))

	if err != nil {
		return token.Token{}, err
	}

	return token.Token{
		Token:      mc.FunctionMetadataCredentialsToken,
		Expiration: mc.Expiration.AsTime(),
	}, nil
}

// ------------------------------------------------------------------------

func (c *Client) metadataCredentialsCallback(newToken token.Token) error {
	zap.L().Info("writing metadata credentials token to disk",
		zap.String("path", MetadataCredsTokenFile))
	metadataCredsTokenDir := filepath.Dir(MetadataCredsTokenFile)
	_, err := os.Stat(metadataCredsTokenDir)
	if errors.Is(err, os.ErrNotExist) {
		err = utils.CreateDirectory(metadataCredsTokenDir, os.FileMode(0755))
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	tmp := MetadataCredsTokenFile + ".tmp"
	err = os.WriteFile(tmp, []byte(newToken.Token), 0644)
	if err != nil {
		return err
	}
	err = os.Rename(tmp, MetadataCredsTokenFile)
	if err != nil {
		return err
	}

	return nil
}
