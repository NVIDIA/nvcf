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

package openbao

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"nvcf-cli/internal/k8s"
	"nvcf-cli/internal/logging"
)

// Client provides OpenBao authentication and JWT token generation
type Client struct {
	httpClient *http.Client
	config     *Config
	k8sClient  *k8s.Client
}

// Config holds OpenBao client configuration
type Config struct {
	OpenBaoURL            string
	OpenBaoJWTAuthRole    string
	OpenBaoJWTSigningRole string
	OpenBaoNamespace      string
	OpenBaoStatefulSet    string
	OpenBaoContainer      string
	OpenBaoSecretName     string
	KubeconfigPath        string
	ClusterNamespace      string
	UtilityImage          string
	Debug                 bool
}

// JWTAuthRequest represents the JWT authentication request to OpenBao
type JWTAuthRequest struct {
	Role string `json:"role"`
	JWT  string `json:"jwt"`
}

// JWTAuthResponse represents the JWT authentication response from OpenBao
type JWTAuthResponse struct {
	Auth struct {
		ClientToken string `json:"client_token"`
	} `json:"auth"`
}

// JWTSignResponse represents the JWT signing response from OpenBao
type JWTSignResponse struct {
	Data struct {
		Token string `json:"token"`
	} `json:"data"`
}

// NewClient creates a new OpenBao client
func NewClient(config *Config, k8sClient *k8s.Client) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		config:     config,
		k8sClient:  k8sClient,
	}
}

// GenerateAdminToken generates an admin JWT token from OpenBao
func (c *Client) GenerateAdminToken(ctx context.Context) (string, time.Time, error) {
	if c.config.Debug {
		logging.Debug("Starting admin token generation from OpenBao")
	}

	// Step 1: Try to get service account token (JWT), fallback to root token (Vault format)
	saToken, err := c.getServiceAccountToken()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to get service account token: %w", err)
	}

	var vaultToken string

	// Check if we have a Vault root token (starts with "s.") - skip authentication
	if strings.HasPrefix(saToken, "s.") {
		if c.config.Debug {
			logging.Debug("Using OpenBao root token directly, skipping JWT authentication")
		}
		vaultToken = saToken
	} else {
		if c.config.Debug {
			logging.Debug("Using service account JWT token, authenticating with OpenBao")
		}
		// Step 2: Authenticate with OpenBao using service account JWT token
		vaultToken, err = c.authenticateWithOpenBao(ctx, saToken)
		if err != nil {
			return "", time.Time{}, fmt.Errorf("failed to authenticate with OpenBao: %w", err)
		}
	}

	// Step 3: Generate JWT token using the vault token
	jwtToken, err := c.generateJWTToken(ctx, vaultToken)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to generate JWT token: %w", err)
	}

	// Set expiration to 24 hours from now (could be parsed from JWT in future)
	expiration := time.Now().Add(24 * time.Hour)

	if c.config.Debug {
		logging.Debug("Successfully generated admin token, expires: %s", expiration.Format("2006-01-02 15:04:05"))
	}

	return jwtToken, expiration, nil
}

// GenerateUserToken generates a user JWT token with custom subject
func (c *Client) GenerateUserToken(ctx context.Context, userSubject string) (string, time.Time, error) {
	if c.config.Debug {
		logging.Debug("Starting user token generation for subject: %s", userSubject)
	}

	// Step 1: Get service account token
	saToken, err := c.getServiceAccountToken()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to get service account token: %w", err)
	}

	// Step 2: Authenticate with OpenBao
	vaultToken, err := c.authenticateWithOpenBao(ctx, saToken)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to authenticate with OpenBao: %w", err)
	}

	// Step 3: Generate JWT token with custom subject
	jwtToken, err := c.generateUserJWTTokenWithSubject(ctx, vaultToken, userSubject)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to generate user JWT token: %w", err)
	}

	// Set expiration to 24 hours from now
	expiration := time.Now().Add(24 * time.Hour)

	if c.config.Debug {
		logging.Debug("Successfully generated user token for %s, expires: %s", userSubject, expiration.Format("2006-01-02 15:04:05"))
	}

	return jwtToken, expiration, nil
}

// getServiceAccountToken retrieves the service account token
func (c *Client) getServiceAccountToken() (string, error) {
	// First try to get from Kubernetes mounted service account token
	saToken, err := k8s.GetServiceAccountToken()
	if err == nil {
		if c.config.Debug {
			logging.Debug("Using mounted service account token")
		}
		return saToken, nil
	}

	if c.config.Debug {
		logging.Debug("Failed to get mounted service account token, trying from secret: %v", err)
	}

	// Fallback: try to get from OpenBao root token secret (for development/testing)
	return c.getOpenBaoRootToken()
}

// getOpenBaoRootToken retrieves the OpenBao root token from Kubernetes secret via kubectl
func (c *Client) getOpenBaoRootToken() (string, error) {
	// Use kubectl to get the secret directly
	kubectlArgs := []string{"kubectl", "get", "secret", c.config.OpenBaoSecretName,
		"-n", c.config.OpenBaoNamespace,
		"-o", "jsonpath={.data.root_token}"}

	// Add kubeconfig if specified
	if c.config.KubeconfigPath != "" {
		kubectlArgs = []string{"kubectl", "--kubeconfig", c.config.KubeconfigPath}
		kubectlArgs = append(kubectlArgs, "get", "secret", c.config.OpenBaoSecretName,
			"-n", c.config.OpenBaoNamespace,
			"-o", "jsonpath={.data.root_token}")
	}

	if c.config.Debug {
		logging.Debug("Retrieving OpenBao root token from secret %s/%s via kubectl", c.config.OpenBaoNamespace, c.config.OpenBaoSecretName)
	}

	// Execute kubectl directly
	cmd := exec.Command(kubectlArgs[0], kubectlArgs[1:]...)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to retrieve OpenBao root token via kubectl: %w", err)
	}

	encodedToken := strings.TrimSpace(string(output))
	if encodedToken == "" {
		return "", fmt.Errorf("empty root token retrieved from OpenBao secret")
	}

	// The token is base64 encoded in the secret, decode it
	decodedToken, err := base64.StdEncoding.DecodeString(encodedToken)
	if err != nil {
		return "", fmt.Errorf("failed to decode root token: %w", err)
	}

	rootToken := strings.TrimSpace(string(decodedToken))
	if rootToken == "" {
		return "", fmt.Errorf("empty root token after decoding")
	}

	if c.config.Debug {
		logging.Debug("Retrieved OpenBao root token from secret %s/%s via kubectl", c.config.OpenBaoNamespace, c.config.OpenBaoSecretName)
	}

	return rootToken, nil
}

// authenticateWithOpenBao authenticates with OpenBao using JWT via kubectl run
func (c *Client) authenticateWithOpenBao(ctx context.Context, saToken string) (string, error) {
	loginURL := c.config.OpenBaoURL + "/v1/auth/jwt/login"

	loginRequest := JWTAuthRequest{
		Role: c.config.OpenBaoJWTAuthRole,
		JWT:  saToken,
	}

	requestBody, err := json.Marshal(loginRequest)
	if err != nil {
		return "", fmt.Errorf("failed to marshal login request: %w", err)
	}

	if c.config.Debug {
		logging.Debug("Authenticating with OpenBao at %s, role: %s via kubectl run", loginURL, c.config.OpenBaoJWTAuthRole)
	}

	// Build curl command for OpenBao authentication
	curlArgs := []string{
		"curl", "-s", "-X", "POST", loginURL,
		"-H", "Content-Type: application/json",
		"-d", string(requestBody),
	}

	// Execute via kubectl run
	output, err := c.executeKubectlRun("openbao-auth", curlArgs)
	if err != nil {
		return "", fmt.Errorf("failed to authenticate with OpenBao via kubectl: %w", err)
	}

	if strings.TrimSpace(output) == "" {
		return "", fmt.Errorf("empty response from OpenBao authentication")
	}

	var loginResponse JWTAuthResponse
	if err := json.Unmarshal([]byte(output), &loginResponse); err != nil {
		return "", fmt.Errorf("failed to unmarshal auth response: %w", err)
	}

	if loginResponse.Auth.ClientToken == "" {
		return "", fmt.Errorf("no client token in OpenBao response")
	}

	if c.config.Debug {
		logging.Debug("Successfully authenticated with OpenBao via kubectl run")
	}

	return loginResponse.Auth.ClientToken, nil
}

// generateJWTToken generates a JWT token using the admin role via kubectl run
func (c *Client) generateJWTToken(ctx context.Context, vaultToken string) (string, error) {
	signURL := fmt.Sprintf("%s/v1/services/nvcf-api/jwt/sign/%s",
		c.config.OpenBaoURL, c.config.OpenBaoJWTSigningRole)

	if c.config.Debug {
		logging.Debug("Generating JWT token at %s via kubectl run", signURL)
	}

	// Build curl command for JWT token generation
	curlArgs := []string{
		"curl", "-s", "-X", "PUT", signURL,
		"-H", "X-Vault-Token: " + vaultToken,
		"-H", "Content-Type: application/json",
		"-d", "{}",
	}

	// Execute via kubectl run
	output, err := c.executeKubectlRun("openbao-jwt-sign", curlArgs)
	if err != nil {
		return "", fmt.Errorf("failed to sign JWT token via kubectl: %w", err)
	}

	if strings.TrimSpace(output) == "" {
		return "", fmt.Errorf("empty response from JWT signing")
	}

	if c.config.Debug {
		logging.Debug("JWT signing response: %s", output)
	}

	var signResponse JWTSignResponse
	if err := json.Unmarshal([]byte(output), &signResponse); err != nil {
		return "", fmt.Errorf("failed to unmarshal JWT response: %w", err)
	}

	if signResponse.Data.Token == "" {
		return "", fmt.Errorf("no token in JWT signing response")
	}

	if c.config.Debug {
		logging.Debug("Successfully generated JWT token via kubectl run (length: %d)", len(signResponse.Data.Token))
	}

	return signResponse.Data.Token, nil
}

// generateUserJWTTokenWithSubject generates a JWT token with custom subject via kubectl run
func (c *Client) generateUserJWTTokenWithSubject(ctx context.Context, vaultToken, userSubject string) (string, error) {
	signURL := fmt.Sprintf("%s/v1/services/nvcf-api/jwt/sign/%s",
		c.config.OpenBaoURL, c.config.OpenBaoJWTSigningRole)

	if c.config.Debug {
		logging.Debug("Generating user JWT token with subject %s at %s via kubectl run", userSubject, signURL)
	}

	// Create payload with custom subject
	payload := map[string]interface{}{
		"sub": userSubject,
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal JWT payload: %w", err)
	}

	// Build curl command for user JWT token generation
	curlArgs := []string{
		"curl", "-s", "-X", "PUT", signURL,
		"-H", "X-Vault-Token: " + vaultToken,
		"-H", "Content-Type: application/json",
		"-d", string(payloadBytes),
	}

	// Execute via kubectl run
	output, err := c.executeKubectlRun("openbao-user-jwt-sign", curlArgs)
	if err != nil {
		return "", fmt.Errorf("failed to sign user JWT token via kubectl: %w", err)
	}

	if strings.TrimSpace(output) == "" {
		return "", fmt.Errorf("empty response from user JWT signing")
	}

	if c.config.Debug {
		logging.Debug("User JWT signing response: %s", output)
	}

	var signResponse JWTSignResponse
	if err := json.Unmarshal([]byte(output), &signResponse); err != nil {
		return "", fmt.Errorf("failed to unmarshal user JWT response: %w", err)
	}

	if signResponse.Data.Token == "" {
		return "", fmt.Errorf("no token in user JWT signing response")
	}

	if c.config.Debug {
		logging.Debug("Successfully generated user JWT token for %s via kubectl run (length: %d)", userSubject, len(signResponse.Data.Token))
	}

	return signResponse.Data.Token, nil
}

// executeKubectlRun executes a kubectl run command with the utility image
func (c *Client) executeKubectlRun(name string, args []string) (string, error) {
	// Build kubectl run command
	cmdArgs := []string{"kubectl", "run", name,
		"--image=" + c.config.UtilityImage,
		"--rm", "-i", "--restart=Never", "--timeout=60s"}

	// Add kubeconfig if specified
	if c.config.KubeconfigPath != "" {
		cmdArgs = []string{"kubectl", "--kubeconfig", c.config.KubeconfigPath}
		cmdArgs = append(cmdArgs, "run", name, "--image="+c.config.UtilityImage, "--rm", "-i", "--restart=Never", "--timeout=60s")
	}

	// Add namespace
	cmdArgs = append(cmdArgs, "-n", c.config.ClusterNamespace)

	// Add the -- separator and the actual command
	cmdArgs = append(cmdArgs, "--")
	cmdArgs = append(cmdArgs, args...)

	if c.config.Debug {
		// Mask any sensitive tokens in debug output
		maskedArgs := make([]string, len(cmdArgs))
		copy(maskedArgs, cmdArgs)
		for i, arg := range maskedArgs {
			if strings.Contains(arg, "X-Vault-Token:") {
				maskedArgs[i] = "X-Vault-Token: <MASKED>"
			}
		}
		logging.Debug("Executing kubectl run for OpenBao: %s", strings.Join(maskedArgs, " "))
	}

	// Execute the command
	cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if c.config.Debug {
			logging.Debug("kubectl run failed with error: %s", err)
			logging.Debug("kubectl raw output:\n%s", string(output))
		}
		return "", fmt.Errorf("kubectl run failed: %s\nOutput: %s", err, string(output))
	}

	if c.config.Debug {
		logging.Debug("kubectl raw output (length: %d bytes):\n%s", len(output), string(output))
	}

	// Filter out kubectl deletion messages and return only the actual command output
	cleanOutput := c.filterKubectlOutput(string(output))

	if c.config.Debug {
		logging.Debug("kubectl filtered output (length: %d bytes):\n%s", len(cleanOutput), cleanOutput)
	}

	return strings.TrimSpace(cleanOutput), nil
}

// filterKubectlOutput filters out kubectl messages and returns only the actual command output
func (c *Client) filterKubectlOutput(output string) string {
	// Find the first line that looks like JSON (starts with { or [)
	lines := strings.Split(output, "\n")
	var jsonLines []string
	foundJSON := false

	for _, line := range lines {
		line = strings.TrimSpace(line)

		// Skip empty lines
		if line == "" {
			continue
		}

		// Handle kubectl deletion message that might be on the same line as JSON
		if strings.Contains(line, "pod \"") && strings.Contains(line, "deleted") {
			// Split at the kubectl deletion message
			if idx := strings.Index(line, "pod \""); idx > 0 {
				// Keep the part before "pod"
				line = strings.TrimSpace(line[:idx])
			} else {
				// "pod" is at the beginning, skip the entire line
				continue
			}
		}

		// Skip other kubectl warning messages
		if strings.Contains(line, "All commands and output from this session will be recorded") ||
			strings.Contains(line, "If you don't see a command prompt") ||
			strings.HasPrefix(line, "Warning:") ||
			strings.HasPrefix(line, "Error from server:") {
			continue
		}

		// Once we find JSON, start collecting lines
		if strings.HasPrefix(line, "{") || strings.HasPrefix(line, "[") {
			foundJSON = true
		}

		if foundJSON {
			jsonLines = append(jsonLines, line)
		}
	}

	// If no JSON found, try to return the last non-empty line
	if len(jsonLines) == 0 {
		for i := len(lines) - 1; i >= 0; i-- {
			line := strings.TrimSpace(lines[i])
			if line != "" && !strings.Contains(line, "pod \"") && !strings.Contains(line, "deleted") {
				return line
			}
		}
		return ""
	}

	return strings.Join(jsonLines, "\n")
}
