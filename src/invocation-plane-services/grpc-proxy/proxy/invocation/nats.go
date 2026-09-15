/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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
package invocation

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/rand"
	"net"
	"strings"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/auth"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
	"go.uber.org/zap"
	"google.golang.org/grpc/credentials"

	"nvcf-grpc-proxy/proxy/metrics"
)

func NewNatsConnection(natsFqdn, nKeySeed, serviceName, ssaFqdn, secretsPath string) (*nats.Conn, error) {
	var natsAuthOption nats.Option
	if nKeySeed != "" {
		var err error
		natsAuthOption, err = newNkeyAuthOption(nKeySeed)
		if err != nil {
			return nil, err
		}
	} else {
		var err error
		natsAuthOption, err = newAuthCalloutAuthOption(ssaFqdn, secretsPath)
		if err != nil {
			return nil, err
		}
	}

	nc, err := nats.Connect(natsFqdn, nats.PingInterval(10*time.Second), natsAuthOption,
		nats.LameDuckModeHandler(func(conn *nats.Conn) {
			metrics.NatsLameDuckCounter.Inc()
			go func() {
				// TODO remove when the SDK natively handles reconnects on lame duck
				time.Sleep(time.Duration(rand.Int63n(int64(time.Second * 10))))
				zap.L().Info("got lame duck message, force reconnecting")
				_ = conn.ForceReconnect()
			}()
		}), nats.ReconnectHandler(func(conn *nats.Conn) {
			// TODO maybe we want to lock the connection until we are reconnected to prevent errors
			zap.L().Info("reconnected to nats", zap.String("server", conn.ConnectedServerName()), zap.String("cluster", conn.ConnectedClusterName()))
			metrics.NatsReconnectCounter.Inc()
		}), nats.Name(serviceName),
		nats.ErrorHandler(func(nc *nats.Conn, sub *nats.Subscription, err error) {
			recordNatsAsyncError(err)
		}), nats.ReconnectErrHandler(func(conn *nats.Conn, err error) {
			recordNatsFailure("nats reconnect failed", err)
		}), nats.DisconnectErrHandler(func(conn *nats.Conn, err error) {
			recordNatsDisconnect(err)
		}), nats.ConnectHandler(func(conn *nats.Conn) {
			zap.L().Info("connected to nats", zap.String("server", conn.ConnectedServerName()), zap.String("cluster", conn.ConnectedClusterName()))
		}))
	if err != nil {
		return nil, err
	}
	metrics.SetNatsStatsConnection(nc)
	return nc, nil
}

func recordNatsDisconnect(err error) {
	if err == nil {
		zap.L().Info("disconnected from nats")
	} else {
		zap.L().Warn("disconnected from nats", zap.Error(err))
	}
	metrics.NatsDisconnectCounter.Inc()
}

func recordNatsAsyncError(err error) {
	metrics.NatsErrorCounter.Inc()
	recordNatsFailure("nats connection error", err)
}

func recordNatsFailure(message string, err error) {
	reason := natsErrorReason(err)
	zap.L().Warn(message, zap.String("reason", reason), zap.Error(err))
	metrics.NatsFailureCounter.WithLabelValues(reason).Inc()
}

func natsErrorReason(err error) string {
	var certificateInvalid x509.CertificateInvalidError
	if errors.As(err, &certificateInvalid) {
		if certificateInvalid.Reason == x509.Expired {
			return metrics.NatsErrorReasonCertificateExpired
		}
		return metrics.NatsErrorReasonTLSVerification
	}

	var unknownAuthority x509.UnknownAuthorityError
	var hostnameError x509.HostnameError
	var certificateVerification *tls.CertificateVerificationError
	if errors.As(err, &unknownAuthority) || errors.As(err, &hostnameError) || errors.As(err, &certificateVerification) {
		return metrics.NatsErrorReasonTLSVerification
	}

	errorText := strings.ToLower(errString(err))
	if strings.Contains(errorText, "certificate has expired") || strings.Contains(errorText, "expired certificate") {
		return metrics.NatsErrorReasonCertificateExpired
	}
	if errors.Is(err, nats.ErrSecureConnRequired) || errors.Is(err, nats.ErrSecureConnWanted) ||
		strings.Contains(errorText, "tls") || strings.Contains(errorText, "x509") {
		return metrics.NatsErrorReasonTLS
	}

	if errors.Is(err, nats.ErrAuthorization) || errors.Is(err, nats.ErrAuthExpired) ||
		errors.Is(err, nats.ErrAuthRevoked) || errors.Is(err, nats.ErrAccountAuthExpired) {
		return metrics.NatsErrorReasonAuthentication
	}

	var networkError net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, nats.ErrTimeout) ||
		(errors.As(err, &networkError) && networkError.Timeout()) {
		return metrics.NatsErrorReasonTimeout
	}

	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) ||
		errors.Is(err, nats.ErrConnectionClosed) || errors.Is(err, nats.ErrConnectionReconnecting) ||
		errors.Is(err, nats.ErrDisconnected) || errors.Is(err, nats.ErrNoServers) ||
		errors.Is(err, nats.ErrStaleConnection) || errors.As(err, &networkError) {
		return metrics.NatsErrorReasonConnection
	}
	return metrics.NatsErrorReasonOther
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func newNkeyAuthOption(nKeySeed string) (nats.Option, error) {
	kp, err := nkeys.FromSeed([]byte(nKeySeed))
	if err != nil {
		return nil, err
	}
	publicKey, err := kp.PublicKey()
	if err != nil {
		return nil, err
	}
	return nats.Nkey(publicKey, func(nonce []byte) ([]byte, error) {
		return kp.Sign(nonce)
	}), nil
}

func newAuthCalloutAuthOption(ssaFqdn, secretsPath string) (nats.Option, error) {
	oauthTokenProvider, err := newOAuthTokenProvider(ssaFqdn, secretsPath)
	if err != nil {
		return nil, err
	}
	authCalloutPluginTokenProvider := &authCalloutPluginTokenProvider{oauthTokenProvider}
	return nats.TokenHandler(authCalloutPluginTokenProvider.GetToken), nil
}

type oauthTokenProvider struct {
	tokenSource credentials.PerRPCCredentials
}

func newOAuthTokenProvider(ssaFqdn, secretsPath string) (*oauthTokenProvider, error) {
	authnConfig := &auth.AuthnConfig{
		OIDCConfig: &auth.ProviderConfig{
			Host:            ssaFqdn,
			CredentialsFile: secretsPath,
			Scopes:          []string{"admin:nats:Worker"},
		},
	}
	tokenSource, err := authnConfig.GRPCClientWithAuth()
	if err != nil {
		return nil, err
	}
	return &oauthTokenProvider{tokenSource: tokenSource}, nil
}

func (t *oauthTokenProvider) GetToken() string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	creds, err := t.tokenSource.GetRequestMetadata(ctx)
	if err != nil {
		zap.L().Warn("failed to get OAuth2 token for nats auth callout plugin", zap.Error(err))
		return ""
	}
	authorization := creds["authorization"]
	authorization = strings.TrimPrefix(authorization, "Bearer ")
	return authorization
}

type authCalloutPluginTokenProvider struct {
	oauthTokenProvider *oauthTokenProvider
}

// token=b64({"account":"$account","pluginName":"$pluginName","payload":"$payload"})
func (t *authCalloutPluginTokenProvider) GetToken() string {
	oauthToken := t.oauthTokenProvider.GetToken()
	tokenJson, err := json.Marshal(struct {
		Account    string `json:"account"`
		PluginName string `json:"pluginName"`
		Payload    string `json:"payload"`
	}{
		Account:    "Worker",
		PluginName: "ssa",
		Payload:    oauthToken,
	})
	if err != nil {
		zap.L().Warn("failed to marshal OAuth2 token for nats auth callout plugin", zap.Error(err))
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(tokenJson)
}
