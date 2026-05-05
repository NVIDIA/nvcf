// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Mock JWKS server for local dev and integration tests.
//
// Generates an ephemeral RSA keypair, serves the JWKS at /jwks.json, and
// writes a signed JWT (with the required scopes) to --token-file
// (default /tmp/reval-test-token).
//
//	go run ./test/mockjwks                       # :8888, /tmp/reval-test-token
//	go run ./test/mockjwks --port 9000
//	go run ./test/mockjwks --token-file /tmp/foo.jwt
package main

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func main() {
	port := flag.Int("port", 8888, "port to serve JWKS on")
	tokenFile := flag.String("token-file", "/tmp/reval-test-token", "path to write the signed test JWT")
	scopes := flag.String("scopes", "helmreval:validate helmreval:render", "space-separated scopes claim")
	subject := flag.String("subject", "dev", "subject claim")
	ttl := flag.Duration("ttl", 24*time.Hour, "token lifetime")
	flag.Parse()

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		log.Fatalf("generate key: %v", err)
	}
	const kid = "mockjwks-1"

	jwks, err := buildJWKS(&priv.PublicKey, kid)
	if err != nil {
		log.Fatalf("build jwks: %v", err)
	}

	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"sub":    *subject,
		"scopes": *scopes,
		"exp":    time.Now().Add(*ttl).Unix(),
		"iat":    time.Now().Unix(),
	})
	tok.Header["kid"] = kid
	signed, err := tok.SignedString(priv)
	if err != nil {
		log.Fatalf("sign token: %v", err)
	}
	if err := os.WriteFile(*tokenFile, []byte(signed), 0o600); err != nil {
		log.Fatalf("write token file %q: %v", *tokenFile, err)
	}
	log.Printf("[mockjwks] wrote signed JWT to %s", *tokenFile)

	mux := http.NewServeMux()
	mux.HandleFunc("/jwks.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(jwks)
	})

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("[mockjwks] serving JWKS on http://localhost%s/jwks.json", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("serve: %v", err)
	}
}

// buildJWKS marshals an RSA public key into a single-key JWK Set.
func buildJWKS(pub *rsa.PublicKey, kid string) ([]byte, error) {
	n := base64.RawURLEncoding.EncodeToString(pub.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(bigEndianBytes(pub.E))
	jwk := map[string]string{
		"kty": "RSA",
		"alg": "RS256",
		"use": "sig",
		"kid": kid,
		"n":   n,
		"e":   e,
	}
	return json.Marshal(map[string]any{"keys": []any{jwk}})
}

// bigEndianBytes encodes a non-negative int into the minimum big-endian byte slice.
func bigEndianBytes(v int) []byte {
	if v == 0 {
		return []byte{0}
	}
	var buf []byte
	for v > 0 {
		buf = append([]byte{byte(v & 0xff)}, buf...)
		v >>= 8
	}
	return buf
}
