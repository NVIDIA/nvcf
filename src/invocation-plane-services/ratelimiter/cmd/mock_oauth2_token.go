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

package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/google/uuid"
	"github.com/lestrrat-go/jwx/jwk"
	"google.golang.org/grpc"
)

func generateECDSAKey() (*ecdsa.PrivateKey, error) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	return privateKey, nil
}

// CreateMockOAuth2Token builds test gRPC per-RPC credentials and a JWKS-backed key id for local tests.
func CreateMockOAuth2Token(iss string, aud []string, scopes []string) (grpc.DialOption, *ecdsa.PublicKey, *uuid.UUID, error) {
	claims := jwt.MapClaims{
		"sub":    "sub",                            // invoking service subject in real deployments
		"iss":    iss,                              // OAuth2 issuer
		"exp":    time.Now().Add(time.Hour).Unix(), // Expiration time (1 hour from now)
		"iat":    time.Now().Unix(),                // Issued at time
		"aud":    aud,
		"scopes": scopes,
	}

	// Create a new token object with claims
	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	kid, _ := uuid.NewRandom()
	token.Header["kid"] = kid

	// Sign the token with a private privateKey
	privateKey, err := generateECDSAKey()
	if err != nil {
		return nil, nil, nil, err
	}

	tokenString, err := token.SignedString(privateKey)
	if err != nil {
		return nil, nil, nil, err
	}
	creds := grpc.WithPerRPCCredentials(newTokenCredentials(tokenString))

	return creds, &privateKey.PublicKey, &kid, nil
}

func StartPublicKeyServer(publicKey ecdsa.PublicKey, kid uuid.UUID) error {
	jwkKey, err := jwk.New(publicKey)
	if err != nil {
		log.Fatal(err)
		return err
	}
	err = jwkKey.Set(jwk.KeyIDKey, kid.String())
	if err != nil {
		log.Fatal(err)
		return err
	}
	err = jwkKey.Set(jwk.AlgorithmKey, "ES256")
	if err != nil {
		log.Fatal(err)
		return err
	}
	go func() {
		http.HandleFunc("/.well-known/jwks.json", func(w http.ResponseWriter, request *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			keys := map[string]interface{}{
				"keys": []interface{}{jwkKey},
			}
			json.NewEncoder(w).Encode(keys)
		})
		fmt.Println("Server is running at http://localhost:8081")
		log.Fatal(http.ListenAndServe(":8081", nil))
	}()
	return nil
}

type tokenCredentials struct {
	token string
}

func newTokenCredentials(token string) *tokenCredentials {
	return &tokenCredentials{token}
}

func (c *tokenCredentials) GetRequestMetadata(ctx context.Context, uri ...string) (map[string]string, error) {
	return map[string]string{
		"authorization": "Bearer " + c.token,
	}, nil
}

func (c *tokenCredentials) RequireTransportSecurity() bool {
	return false
}
