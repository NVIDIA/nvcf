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
	"fmt"
	"math/rand"
	"path/filepath"
	"time"

	"github.com/cenkalti/backoff/v4"
	"go.uber.org/zap"
	"golang.org/x/oauth2"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/auth"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvcf"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/token"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/utils"
)

func (c *Client) ConnectIndefinitely(ctx context.Context) (context.Context, error) {
	ctx, cancelFunc := context.WithCancel(ctx)
	context.AfterFunc(ctx, func() {
		zap.L().Warn("connection loop exiting", zap.Error(ctx.Err()))
	})
	err := backoff.Retry(func() error {
		return c.connect(ctx)
	}, backoff.WithContext(backoff.NewExponentialBackOff(backoff.WithMaxElapsedTime(5*time.Minute)), ctx))
	if err != nil {
		cancelFunc()
		return ctx, err
	}

	// keep trying to reconnect indefinitely
	go func() {
		defer cancelFunc() // indicate to the caller that the background connection loop has exited
		for ctx.Err() == nil {

			// sleep until it's time to reconnect
			token, err := c.NvcfTokenProvider.Token()
			if err == nil && token.Valid() {
				// somewhere between 20-30 minutes, but shorter if expiry is earlier
				// in practice the expiry is 3 hours
				sleepDuration := time.Duration(rand.Intn(10*60)+20*60) * time.Second
				if expiresIn := time.Until(token.Expiry); expiresIn < sleepDuration {
					sleepDuration = time.Duration(expiresIn.Seconds()*0.9) * time.Second
				}
				utils.SleepWithContext(ctx, sleepDuration)
			}
		maxElapsed := time.Until(token.Expiry)
		if maxElapsed <= 0 {
			maxElapsed = 5 * time.Minute
		}
		err = backoff.Retry(func() error {
			return c.connect(ctx)
		}, backoff.WithContext(backoff.NewExponentialBackOff(backoff.WithMaxElapsedTime(maxElapsed)), ctx))
			if err != nil {
				zap.L().Error("failed to reconnect to NVCF", zap.Error(err))
				return
			}
		}
	}()

	return ctx, nil
}

func (c *Client) connect(ctx context.Context) error {
	zap.L().Info("connecting to NVCF")
	if ctx.Err() != nil {
		// Don't bother
		return ctx.Err()
	}
	connected, err := c.Client.ConnectOnce(ctx, &pb.WorkerConnect{
		InstanceId:        c.instanceId,
		FunctionId:        c.functionId,
		FunctionVersionId: c.functionVersionId,
	}, auth.GrpcTokenFromSource(c.NvcfTokenProvider))
	if err != nil {
		return fmt.Errorf("failed to send connect request to NVCF: %w", err)
	}
	if connected.ConnectedRegion == "" {
		return fmt.Errorf("nvcf did not respond with a connected region")
	}
	c.updateConnectedRegions(connected.ConnectedRegion, connected.OtherRegions)
	oauthToken := &oauth2.Token{
		AccessToken: connected.NvcfWorkerToken,
		Expiry:      connected.Expiration.AsTime(),
	}
	if !oauthToken.Valid() {
		return errors.New("invalid worker token response from nvcf api")
	}
	go func() {
		tokenFile := filepath.Join(c.sharedConfigDir, cachedNvcfTokenFilename)
		err = token.CacheToken(tokenFile, oauthToken)
		if err != nil {
			zap.L().Error("failed to cache new NVCF token", zap.Error(err))
		}
	}()
	c.NvcfTokenProvider.SetTokenSource(oauth2.StaticTokenSource(oauthToken))
	zap.L().Info("connected to NVCF", zap.String("region", connected.ConnectedRegion), zap.Strings("secondaryRegions", connected.OtherRegions))
	return nil
}
