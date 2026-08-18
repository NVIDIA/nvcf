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

package jwt

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// Header represents a JWT header
type Header struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
	Kid string `json:"kid,omitempty"`
}

// Payload represents a JWT payload
type Payload struct {
	Iss string `json:"iss"`
	Sub string `json:"sub"`
	Aud string `json:"aud"`
	Iat int64  `json:"iat"`
	Exp int64  `json:"exp"`
	Jti string `json:"jti"`
}

// Sign creates a signed JWT using the given private key
func Sign(privateKey *ecdsa.PrivateKey, kid string) (string, error) {
	// Create header
	header := Header{
		Alg: "ES256",
		Typ: "JWT",
		Kid: kid,
	}

	// Create payload
	now := time.Now().Unix()
	payload := Payload{
		Iss: "test-harness",
		Sub: "signing-key-test",
		Aud: "verification",
		Iat: now,
		Exp: now + 3600,
		Jti: fmt.Sprintf("test-%d", now),
	}

	// Encode header and payload
	headerJSON, _ := json.Marshal(header)
	payloadJSON, _ := json.Marshal(payload)

	headerB64 := base64URLEncode(headerJSON)
	payloadB64 := base64URLEncode(payloadJSON)

	// Create signing input
	signingInput := headerB64 + "." + payloadB64

	// Sign with ECDSA
	hash := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, privateKey, hash[:])
	if err != nil {
		return "", fmt.Errorf("signing failed: %w", err)
	}

	// Convert r, s to fixed-size byte arrays (32 bytes each for P-256)
	rBytes := padToLength(r.Bytes(), 32)
	sBytes := padToLength(s.Bytes(), 32)

	// Concatenate r || s
	signature := append(rBytes, sBytes...)
	signatureB64 := base64URLEncode(signature)

	return signingInput + "." + signatureB64, nil
}

// Verify validates a JWT signature using the given public key
func Verify(token string, publicKey *ecdsa.PublicKey) (bool, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false, fmt.Errorf("invalid JWT format: expected 3 parts, got %d", len(parts))
	}

	signingInput := parts[0] + "." + parts[1]
	signatureB64 := parts[2]

	// Decode signature
	signature, err := base64URLDecode(signatureB64)
	if err != nil {
		return false, fmt.Errorf("failed to decode signature: %w", err)
	}

	// For ES256, signature should be 64 bytes (32 + 32)
	if len(signature) != 64 {
		return false, fmt.Errorf("invalid signature length: expected 64, got %d", len(signature))
	}

	// Extract r and s
	r := new(big.Int).SetBytes(signature[:32])
	s := new(big.Int).SetBytes(signature[32:])

	// Verify
	hash := sha256.Sum256([]byte(signingInput))
	valid := ecdsa.Verify(publicKey, hash[:], r, s)

	return valid, nil
}

// base64URLEncode encodes bytes to base64url without padding
func base64URLEncode(data []byte) string {
	return strings.TrimRight(base64.URLEncoding.EncodeToString(data), "=")
}

// base64URLDecode decodes base64url string
func base64URLDecode(s string) ([]byte, error) {
	// Add padding if needed
	switch len(s) % 4 {
	case 2:
		s += "=="
	case 3:
		s += "="
	}
	return base64.URLEncoding.DecodeString(s)
}

// padToLength pads a byte slice to the specified length with leading zeros
func padToLength(b []byte, length int) []byte {
	if len(b) >= length {
		return b[len(b)-length:]
	}
	padded := make([]byte, length)
	copy(padded[length-len(b):], b)
	return padded
}

