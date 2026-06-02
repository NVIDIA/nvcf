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

package token

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"time"

	"go.uber.org/zap"
	"golang.org/x/oauth2"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/metrics"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/utils"
)

const maxJitterSeconds = 60

type Token struct {
	Token      string
	Expiration time.Time
}

// ------------------------------------------------------------------------

func StartTokenRefresher(ctx context.Context, name string, enableJitter bool, getTokenFunc func(ctx context.Context) (Token, error), tokenCallbackFunc func(Token) error) {
	zap.L().Info("starting periodic token refresher", zap.String("name", name))
	go func() {
		for {
			if ctx.Err() != nil {
				zap.L().Info("terminating token refresher",
					zap.String("name", name))
				return
			}

			zap.L().Info("refreshing token", zap.String("name", name))
			newToken, err := getTokenFunc(ctx)
			if err != nil {
				zap.L().Error("failed to refresh token",
					zap.String("name", name), zap.Error(err))
				recordTokenRefreshStatus(name, 1)
				utils.SleepWithContext(ctx, 1*time.Minute)
				continue
			}

			zap.L().Info("triggering token refresh callback",
				zap.String("name", name))
			err = tokenCallbackFunc(newToken)
			if err != nil {
				zap.L().Error("token refresh callback failed",
					zap.String("name", name), zap.Error(err))
				recordTokenRefreshStatus(name, 1)
				utils.SleepWithContext(ctx, 1*time.Minute)
				continue
			}

			// Reset failure gauge on success
			recordTokenRefreshStatus(name, 0)

			zap.L().Info("token refreshed",
				zap.String("name", name), zap.Time("expiration", newToken.Expiration))

			refreshDelay := time.Until(newToken.Expiration) * 80 / 100
			if enableJitter {
				refreshDelayWithJitter := refreshDelay + (time.Duration(rand.Intn(maxJitterSeconds)) * time.Second)
				if time.Now().Add(refreshDelayWithJitter).Before(newToken.Expiration) {
					refreshDelay = refreshDelayWithJitter
				}
			}

			utils.SleepWithContext(ctx, refreshDelay)
		}
	}()
}

func CacheToken(tokenFile string, token *oauth2.Token) error {
	zap.L().Info("Caching token")
	b, err := json.Marshal(token)
	if err != nil {
		return err
	}

	// Create directory if it doesn't exist
	tokenDir := filepath.Dir(tokenFile)
	_, err = os.Stat(tokenDir)
	if errors.Is(err, os.ErrNotExist) {
		zap.L().Warn("couldn't find directory, assuming it is not mounted via cluster agent. Attempting to create", zap.String("tokenDir", tokenDir))
		err = utils.CreateDirectory(tokenDir, os.FileMode(0755))
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	err = os.WriteFile(tokenFile, b, 0600)
	if err != nil {
		return err
	}

	return nil
}

func LoadCachedTokenIfExists(tokenFile string) (*oauth2.Token, error) {
	_, err := os.Stat(tokenFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}

	f, err := os.Open(tokenFile)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	decoder := json.NewDecoder(f)
	var token oauth2.Token
	err = decoder.Decode(&token)
	if err != nil {
		return nil, err
	}

	return &token, nil
}

// Record if token refreshment is successful or not as a prometheus gauge.
// Status 0 is success, 1 is failure.
func recordTokenRefreshStatus(tokenName string, status float64) {
	if metrics.TokenRefreshFailureGauge != nil {
		metrics.TokenRefreshFailureGauge.WithLabelValues(tokenName).Set(status)
	}
}
