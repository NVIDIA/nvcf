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

package keygen

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"os/exec"
)

// JWK represents an EC JSON Web Key
type JWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	D   string `json:"d"`
	X   string `json:"x"`
	Y   string `json:"y"`
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	Use string `json:"use"`
	Iat int64  `json:"iat"`
}

// KeyGenResult contains the generated key and metadata
type KeyGenResult struct {
	JWK        JWK
	RawOutput  string
	DLength    int
	XLength    int
	YLength    int
	ParseError error
}

// Generator handles key generation via bash subprocess
type Generator struct {
	ScriptPath string
}

// NewGenerator creates a generator with the specified script
func NewGenerator(scriptPath string) *Generator {
	return &Generator{ScriptPath: scriptPath}
}

// Generate calls bash to generate a signing key and returns the result
func (g *Generator) Generate() (*KeyGenResult, error) {
	result := &KeyGenResult{}

	// Generate a UUID for the kid using Go
	kid := generateUUID()

	// Build bash command that sources the script and calls the function
	bashScript := fmt.Sprintf(`
		source "%s"
		generate_asymmetric_signing_key "%s"
	`, g.ScriptPath, kid)

	cmd := exec.Command("bash", "-c", bashScript)
	cmd.Env = os.Environ()

	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("bash execution failed: %w\nstderr: %s", err, string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("bash execution failed: %w", err)
	}

	result.RawOutput = string(output)

	// Parse the JWK JSON
	if err := json.Unmarshal(output, &result.JWK); err != nil {
		result.ParseError = err
		return result, nil
	}

	// Record lengths
	result.DLength = len(result.JWK.D)
	result.XLength = len(result.JWK.X)
	result.YLength = len(result.JWK.Y)

	return result, nil
}

// ToECDSAPrivateKey converts JWK to *ecdsa.PrivateKey
func (j *JWK) ToECDSAPrivateKey() (*ecdsa.PrivateKey, error) {
	// Decode base64url values
	dBytes, err := base64URLDecode(j.D)
	if err != nil {
		return nil, fmt.Errorf("failed to decode d: %w", err)
	}

	xBytes, err := base64URLDecode(j.X)
	if err != nil {
		return nil, fmt.Errorf("failed to decode x: %w", err)
	}

	yBytes, err := base64URLDecode(j.Y)
	if err != nil {
		return nil, fmt.Errorf("failed to decode y: %w", err)
	}

	// Construct the private key
	privateKey := &ecdsa.PrivateKey{
		PublicKey: ecdsa.PublicKey{
			Curve: elliptic.P256(),
			X:     new(big.Int).SetBytes(xBytes),
			Y:     new(big.Int).SetBytes(yBytes),
		},
		D: new(big.Int).SetBytes(dBytes),
	}

	return privateKey, nil
}

// base64URLDecode decodes a base64url string (no padding)
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

// generateUUID creates a UUID v4
func generateUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)

	// Set version (4) and variant bits
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80

	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

