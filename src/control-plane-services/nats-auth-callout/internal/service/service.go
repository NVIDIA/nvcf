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

package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/NVIDIA/nvcf/src/control-plane-services/nats-auth-callout/internal/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/nats-auth-callout/internal/plugins"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
	"github.com/synadia-io/callout.go"
	"go.uber.org/zap"
)

type Service struct {
	config         *config.ServiceConfig
	nc             *nats.Conn
	logger         *zap.Logger
	calloutService *callout.AuthorizationService
	closed         chan struct{}
}

func NewFromConfig(ctx context.Context, config *config.ServiceConfig, logger *zap.Logger) (*Service, error) {
	// Create NKey keypair from embedded seed
	nkeyPair, err := nkeys.FromSeed([]byte(config.NkeySeed))
	if err != nil {
		return nil, fmt.Errorf("failed to create NKEY keypair: %w", err)
	}

	// Get public key for NKey option
	publicKey, err := nkeyPair.PublicKey()
	if err != nil {
		return nil, fmt.Errorf("failed to get public key: %w", err)
	}

	// Create NKey option with signature callback
	nkeyOpt := nats.Nkey(publicKey, func(nonce []byte) ([]byte, error) {
		return nkeyPair.Sign(nonce)
	})

	// Key for signing JWTs, must be an account or operator key
	signingKeySeed := []byte(config.NkeySignature)
	if len(signingKeySeed) == 0 {
		return nil, fmt.Errorf("signing key seed cannot be empty")
	}

	signingKey, err := nkeys.FromSeed(signingKeySeed)
	if err != nil {
		return nil, fmt.Errorf("failed to create signing key: %w", err)
	}

	// Verify signing key is appropriate type
	keyPrefix, err := signingKey.PublicKey()
	// untested section
	if err != nil {
		return nil, fmt.Errorf("failed to get signing key public key: %w", err)
	}
	// untested section
	if !strings.HasPrefix(keyPrefix, "A") && !strings.HasPrefix(keyPrefix, "O") {
		return nil, fmt.Errorf("signing key must be an account or operator key: %w", err)
	}

	// nats.go's default is to give up after 60 reconnect attempts (~2 min) and then
	// stay closed while the process keeps running and reporting healthy. A NATS
	// outage longer than that left the callout permanently disconnected and every
	// login timing out until someone restarted it by hand. Reconnect without limit,
	// and expose a connection that closes anyway so the process can exit.
	closed := make(chan struct{})
	var closeOnce sync.Once
	nc, err := nats.Connect(config.NatsURL, nkeyOpt,
		nats.MaxReconnects(-1),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			logger.Warn("Disconnected from NATS server", zap.Error(err))
		}),
		nats.ReconnectHandler(func(c *nats.Conn) {
			logger.Info("Reconnected to NATS server", zap.String("url", c.ConnectedUrl()))
		}),
		nats.ClosedHandler(func(_ *nats.Conn) {
			logger.Info("NATS connection closed")
			closeOnce.Do(func() { close(closed) })
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to NATS: %w", err)
	}
	logger.Info("Connected to NATS server", zap.String("url", config.NatsURL))

	pm := plugins.NewManager(config, logger)
	ah := NewAuthHandler(pm, logger, signingKey)
	authorizerFn := func(req *jwt.AuthorizationRequest) (string, error) {
		start := time.Now()
		logger.Info("Callout authorizer function called",
			zap.String("user_nkey", req.UserNkey),
			zap.String("client_info", req.ClientInformation.Name))

		result, err := ah.HandleAuthRequest(ctx, req)
		duration := time.Since(start)

		if err != nil {
			logger.Error("Callout authorizer function failed",
				zap.String("user_nkey", req.UserNkey),
				zap.Duration("duration", duration),
				zap.Error(err))
		} else {
			logger.Info("Callout authorizer function succeeded",
				zap.String("user_nkey", req.UserNkey),
				zap.Duration("duration", duration))
		}

		return result, err
	}

	options := []callout.Option{
		callout.Authorizer(authorizerFn),
		callout.ResponseSignerKey(signingKey),
		callout.Logger(calloutLogAdapter{logger.Sugar()}),
		callout.AsyncWorkers(512), // without this, all requests are done sync
	}

	calloutService, err := callout.NewAuthorizationService(nc, options...)
	if err != nil {
		return nil, fmt.Errorf("failed to create authorization service: %w", err)
	}

	logger.Info("Svc info:", zap.Any("info", calloutService.Service.Info()))

	return &Service{
		nc:             nc,
		config:         config,
		logger:         logger,
		calloutService: calloutService,
		closed:         closed,
	}, nil
}

func (s *Service) Stop() {
	_ = s.calloutService.Stop()
	s.nc.Close()
}

// Closed is closed once the NATS connection is closed for good: on Stop, or on an
// error the client does not retry. While it stays open, the client keeps reconnecting.
func (s *Service) Closed() <-chan struct{} {
	return s.closed
}

// CheckReady returns an error while the service cannot answer auth requests,
// i.e. while its NATS connection is not up.
func (s *Service) CheckReady() error {
	if !s.nc.IsConnected() {
		return fmt.Errorf("NATS connection is %s", s.nc.Status())
	}
	return nil
}

// calloutLogAdapter adapts our zap.Logger to what callout expects.
type calloutLogAdapter struct {
	logger *zap.SugaredLogger
}

// untested section
func (l calloutLogAdapter) Tracef(format string, args ...any) {
	l.logger.Debugf(format, args...)
}

// untested section
func (l calloutLogAdapter) Debugf(format string, args ...any) {
	l.logger.Debugf(format, args...)
}

// untested section
func (l calloutLogAdapter) Noticef(format string, args ...any) {
	l.logger.Infof(format, args...)
}

// untested section
func (l calloutLogAdapter) Warnf(format string, args ...any) {
	l.logger.Warnf(format, args...)
}

// untested section
func (l calloutLogAdapter) Errorf(format string, args ...any) {
	l.logger.Errorf(format, args...)
}

// untested section
func (l calloutLogAdapter) Fatalf(format string, args ...any) {
	l.logger.Fatalf(format, args...)
}
