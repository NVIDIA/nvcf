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
	"path/filepath"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"
	"golang.org/x/oauth2"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/auth"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvct"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/token"
)

const (
	expiryIntervalInMins = 90
)

// ------------------------------------------------------------------------

// Schedule a periodical token refresher.
func (c *Client) StartWorkerTokenRefresher(ctx context.Context, enableJitter bool) {
	token.StartTokenRefresher(
		ctx,
		"worker token",
		enableJitter,
		c.getWorkerToken,
		c.workerTokenCallback,
	)
}

// ------------------------------------------------------------------------

// Get worker token from nvct.
func (c *Client) getWorkerToken(ctx context.Context) (token.Token, error) {
	var workerToken token.Token

	if ctx.Err() != nil {
		return workerToken, ctx.Err()
	}

	var resp *pb.RefreshTokenResponse
	backoffErr := backoff.Retry(func() error {
		var err error
		resp, err = c.Client.RefreshToken(ctx, &pb.RefreshTokenRequest{
			TaskId: c.taskId,
		}, auth.GrpcTokenFromSource(c.NvctTokenProvider))
		return err
	}, backoff.WithContext(backoff.WithMaxRetries(backoff.NewExponentialBackOff(), 10), ctx))

	if backoffErr != nil {
		return workerToken, backoffErr
	}

	tokenString := resp.GetToken()
	expiry, err := calculateAssertionTokenExpiry(tokenString)
	if err != nil {
		return workerToken, err
	}

	workerToken = token.Token{
		Token:      resp.GetToken(),
		Expiration: expiry,
	}

	return workerToken, nil
}

// ------------------------------------------------------------------------

// Set new worker client token.
func (c *Client) workerTokenCallback(newToken token.Token) error {

	nvctToken := &oauth2.Token{
		AccessToken: newToken.Token,
		Expiry:      newToken.Expiration,
	}

	c.NvctTokenProvider.SetTokenSource(oauth2.StaticTokenSource(nvctToken))

	zap.L().Info("Caching NVCT token...")
	tokenFile := filepath.Join(c.sharedConfigDir, cachedNvctTokenFilename)
	err := token.CacheToken(tokenFile, nvctToken)
	if err != nil {
		zap.L().Error("failed to cache NVCT token", zap.Error(err))
		return err
	}
	zap.L().Info("Caching NVCT token successful")

	return nil
}

// ------------------------------------------------------------------------

// Set assertion token expiry for NVCT
func calculateAssertionTokenExpiry(tokenString string) (time.Time, error) {
	claims := jwt.MapClaims{}
	_, _, err := jwt.NewParser().ParseUnverified(tokenString, claims)
	if err != nil {
		zap.L().Error("failed to parse token", zap.Error(err))
		return time.Time{}, err
	}

	// Get the issuedAt date from assertion token
	iat, err := claims.GetIssuedAt()
	if err != nil {
		zap.L().Error("failed to get issued at claim in token", zap.Error(err))
		return time.Time{}, err
	}
	// Set expiry time
	expiryTime := iat.Add(expiryIntervalInMins * time.Minute)
	return expiryTime, nil
}
