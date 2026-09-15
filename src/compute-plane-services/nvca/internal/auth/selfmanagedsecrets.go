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

package auth

import (
	"context"
	"encoding/json"
	"time"

	cmnsecret "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/secret"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/health"
)

const (
	//nolint:gosec // this is a path to a file on the host
	DefaultSelfHostedVaultSecretsJSONPath = "/home/app/vault/secrets.json"
)

// Ensure this implements the TokenFetcher interface
var _ TokenFetcher = &SelfManagedSecretsFetcher{}
var _ NATSSecretsFetcher = &SelfManagedSecretsFetcher{}

type NATSSecretsFetcher interface {
	FetchNATSSecrets(ctx context.Context) (NATSSecrets, error)
}

type SelfManagedSecretsFetcher struct {
	secretJSONFetcher *cmnsecret.FileFetcher
}

type selfManagedSecrets struct {
	KV KVSecrets `json:"kv"`
}

type KVSecrets struct {
	NATS   NATSSecrets   `json:"nats"`
	Tokens TokensSecrets `json:"tokens"`
}

type NATSSecrets struct {
	APIAuth NATSAPIAuthSecrets `json:"api-auth"`
}

type NATSAPIAuthSecrets struct {
	User     string `json:"user"`
	UserSeed string `json:"user-seed"`
}

type TokensSecrets struct {
	ICMS  string `json:"icms"`
	ReVal string `json:"reval"`
}

func NewSelfManagedSecretsFetcher(
	ctx context.Context,
	name string,
	selfHostedVaultSecretsJSONPath string,
) (*SelfManagedSecretsFetcher, *health.TokenFetcherHealthCheck, error) {
	secretJSONFetcher, err := cmnsecret.NewFileFetcher(ctx, selfHostedVaultSecretsJSONPath,
		cmnsecret.WithMaxFileSize(int64(1024*1024)), // 1MB
		cmnsecret.WithForceFileRefreshInterval(time.Hour),
	)
	if err != nil {
		return nil, health.SuccessfulTokenFetcherHealthCheck(name), err
	}

	return &SelfManagedSecretsFetcher{
		secretJSONFetcher: secretJSONFetcher,
	}, health.SuccessfulTokenFetcherHealthCheck(name), nil
}

func (fetcher *SelfManagedSecretsFetcher) getSelfManagedSecrets(ctx context.Context) (*selfManagedSecrets, error) {
	// This fetch is cached in the fetcher, but we'll pay the penalty of parsing the JSON
	// on every fetch for now.
	secretJSON, err := fetcher.secretJSONFetcher.FetchData(ctx)
	if err != nil {
		return nil, err
	}

	var secrets selfManagedSecrets
	if err := json.Unmarshal(secretJSON, &secrets); err != nil {
		return nil, err
	}

	return &secrets, nil
}

func (fetcher *SelfManagedSecretsFetcher) FetchNATSSecrets(ctx context.Context) (NATSSecrets, error) {
	secrets, err := fetcher.getSelfManagedSecrets(ctx)
	if err != nil {
		return NATSSecrets{}, err
	}

	return secrets.KV.NATS, nil
}

// FetchToken fetches the ICMS API token from the self-managed secrets.
func (fetcher *SelfManagedSecretsFetcher) FetchToken(ctx context.Context) (string, error) {
	secrets, err := fetcher.getSelfManagedSecrets(ctx)
	if err != nil {
		return "", err
	}

	return secrets.KV.Tokens.ICMS, nil
}
