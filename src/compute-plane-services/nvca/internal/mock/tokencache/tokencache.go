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

package mocktokencache

import (
	"context"
	"crypto/rsa"
	"time"

	cmnoauth "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/oauth"
	"github.com/go-jose/go-jose/v3"
	"github.com/go-jose/go-jose/v3/jwt"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/mock/utils"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/health"
)

func New(issuer, ssaClientID, keyFile string) (*cmnoauth.JWTCache, *health.TokenFetcherHealthCheck, error) {
	priv, err := utils.DecodePrivateKeyRSA(keyFile)
	if err != nil {
		return nil, nil, err
	}
	return NewForKey(issuer, ssaClientID, priv)
}

func NewForKey(issuer, ssaClientID string, priv *rsa.PrivateKey) (*cmnoauth.JWTCache, *health.TokenFetcherHealthCheck, error) {
	sig, err := jose.NewSigner(jose.SigningKey{
		Algorithm: jose.RS256,
		Key:       priv,
	}, &jose.SignerOptions{})
	if err != nil {
		return nil, nil, err
	}

	jc := cmnoauth.NewJWTCache().
		WithFetcher(&tokenFetcher{
			sig: sig,
			iss: issuer,
			sub: ssaClientID,
		}).
		WithVerifier(&tokenVerifier{
			key: priv,
		})

	return jc, health.SuccessfulTokenFetcherHealthCheck("mock"), nil
}

type tokenFetcher struct {
	sig jose.Signer
	iss string
	sub string
}

func (f *tokenFetcher) RefreshClient() {}

func (f *tokenFetcher) FetchToken(context.Context) (string, error) {
	tok, err := jwt.Signed(f.sig).Claims(jwt.Claims{
		Issuer:  f.iss,
		Subject: f.sub,
		Expiry:  jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
	}).CompactSerialize()
	if err != nil {
		return "", err
	}
	return tok, nil
}

type tokenVerifier struct {
	key *rsa.PrivateKey
}

func (v *tokenVerifier) VerifyToken(ctx context.Context, token string) (bool, error) {
	tok, err := jwt.ParseSigned(token)
	if err != nil {
		return false, err
	}

	var cl jwt.Claims
	err = tok.Claims(&v.key.PublicKey, cl)
	return err == nil, nil
}
