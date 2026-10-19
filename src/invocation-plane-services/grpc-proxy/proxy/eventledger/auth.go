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
package eventledger

import (
	"net/http"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/auth"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/clients"
	"go.uber.org/zap"

	"nvcf-grpc-proxy/proxy/credentials"
)

const (
	tokenKey = "eventLedgerToken"
	scope    = "fnds:createEvent"
	spanName = "grpc-proxy.event_ledger.publish"
)

// newTransport picks the auth mode the same way the NVCF API client does: an
// eventLedgerToken entry in the secrets file selects a fixed bearer token
// that is reloaded when the file changes, otherwise OAuth2 client
// credentials against the OAuth2 host.
func newTransport(secretsPath, oauth2Fqdn string) (*transport, error) {
	cfg := &clients.HTTPClientConfig{BaseClientConfig: &clients.BaseClientConfig{}}
	var bearer *credentials.BearerTokenCredentials
	if _, err := credentials.ReadTokenFromFile(secretsPath, tokenKey); err == nil {
		zap.L().Info("Using fixed bearer token authentication for event ledger client")
		if bearer, err = credentials.NewBearerTokenCredentials(secretsPath, tokenKey, false); err != nil {
			return nil, err
		}
	} else {
		zap.L().Info("Event ledger bearer token not found, falling back to OAuth2")
		cfg.AuthnCfg = &auth.AuthnConfig{
			OIDCConfig: &auth.ProviderConfig{
				Host:            oauth2Fqdn,
				CredentialsFile: secretsPath,
				Scopes:          []string{scope},
			},
			RefreshConfig: &auth.RefreshConfig{Interval: int64((5 * time.Minute).Seconds())},
		}
	}
	client, err := clients.DefaultHTTPClient(cfg, func(string, *http.Request) string { return spanName })
	if err != nil {
		if bearer != nil {
			_ = bearer.Close()
		}
		return nil, err
	}
	return &transport{client: client, bearer: bearer}, nil
}

// transport resolves the client per request because nvkit swaps it when
// OAuth2 credentials rotate. bearer is nil in OAuth2 mode.
type transport struct {
	client *clients.HTTPClient
	bearer *credentials.BearerTokenCredentials
}

func (t *transport) RoundTrip(r *http.Request) (*http.Response, error) {
	if t.bearer != nil {
		md, err := t.bearer.GetRequestMetadata(r.Context())
		if err != nil {
			return nil, err
		}
		// A RoundTripper must not modify the caller's request.
		r = r.Clone(r.Context())
		r.Header.Set("Authorization", md["authorization"])
	}
	return t.client.Client(r.Context()).Do(r)
}

// Close stops the bearer token file watcher.
func (t *transport) Close() {
	if t.bearer != nil {
		_ = t.bearer.Close()
	}
}
