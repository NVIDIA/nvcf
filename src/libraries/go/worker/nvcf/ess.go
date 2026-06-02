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

// ------------------------------------------------------------------------

// StartAssertionTokenRefresher Start to rotate assertion token for ESS agent periodically.
func (c *Client) StartAssertionTokenRefresher(ctx context.Context, tokenFilePath string, enableJitter bool) {
	c.assertionTokenPath = tokenFilePath
	token.StartTokenRefresher(ctx, "assertion", enableJitter,
		c.getAssertionToken, c.writeAssertionTokenToDisk)
}

// ------------------------------------------------------------------------

// Fetch assertion token from NVCF server.
func (c *Client) getAssertionToken(ctx context.Context) (token.Token, error) {
	if ctx.Err() != nil {
		return token.Token{}, ctx.Err()
	}

	var resp *pb.SecretCredentialsResponse

	backoffErr := backoff.Retry(func() error {
		var err error
		resp, err = c.Client.RequestSecretCredentials(ctx, &pb.SecretCredentialsRequest{
			FunctionId:        c.functionId,
			FunctionVersionId: c.functionVersionId,
		}, auth.GrpcTokenFromSource(c.NvcfTokenProvider))
		if err != nil {
			return err
		}
		return nil
	}, backoff.WithContext(backoff.WithMaxRetries(backoff.NewExponentialBackOff(), 10), ctx))

	if backoffErr != nil {
		return token.Token{}, backoffErr
	}

	return token.Token{
		Token:      resp.SecretCredentialsToken,
		Expiration: resp.Expiration.AsTime(),
	}, nil
}

// ------------------------------------------------------------------------

// Write assertion token to a file.
func (c *Client) writeAssertionTokenToDisk(token token.Token) error {
	zap.L().Info("Output assertion token to local file", zap.String("path", c.assertionTokenPath))

	basePath := filepath.Dir(c.assertionTokenPath)

	_, err := os.Stat(basePath)
	if errors.Is(err, os.ErrNotExist) {
		err = utils.CreateDirectory(basePath, os.FileMode(0755))
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	tmpPath := c.assertionTokenPath + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(token.Token), 0600); err != nil {
		return err
	}

	if err := os.Rename(tmpPath, c.assertionTokenPath); err != nil {
		return err
	}

	return nil
}
