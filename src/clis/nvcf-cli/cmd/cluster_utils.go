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

package cmd

import (
	"encoding/base64"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"nvcf-cli/internal/client"
	"nvcf-cli/internal/logging"
)

// buildKubectlCommand builds a kubectl command with proper kubeconfig handling
func buildKubectlCommand(config *client.Config, args []string) *exec.Cmd {
	// Start with kubectl
	cmdArgs := []string{"kubectl"}

	// Add kubeconfig if specified
	if config.KubeconfigPath != "" {
		cmdArgs = append(cmdArgs, "--kubeconfig", config.KubeconfigPath)
	}

	// Add the actual command arguments
	cmdArgs = append(cmdArgs, args...)

	// Create the command
	cmd := exec.Command("kubectl", args...)
	if config.KubeconfigPath != "" {
		cmd.Args = append(cmd.Args[:1], append([]string{"--kubeconfig", config.KubeconfigPath}, cmd.Args[1:]...)...)
	}

	if config.Debug {
		logging.Debug("Executing kubectl command: %s", strings.Join(cmd.Args, " "))
	}

	return cmd
}

// executeCommand executes a command and returns the output
func executeCommand(cmd *exec.Cmd) (string, error) {
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("command failed: %s\nOutput: %s", err, string(output))
	}

	return strings.TrimSpace(string(output)), nil
}

// decodeBase64 decodes a base64 string
func decodeBase64(encoded string) (string, error) {
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("failed to decode base64: %w", err)
	}
	return string(decoded), nil
}

// parseTokenFromOutput extracts the token from OpenBao output
func parseTokenFromOutput(output string) (string, error) {
	// OpenBao output typically looks like:
	// Key                Value
	// ---                -----
	// token              eyJhbGciOiJFUzI1NiIsImtpZCI6Ijl1U2V0R3U2VHdhZDdWaW9JUEdSOUp1YjhJQSIsInR5cCI6IkpXVCJ9...

	if !strings.Contains(output, "token") {
		return "", fmt.Errorf("no token found in OpenBao output: %s", output)
	}

	// Look for lines with "token" followed by a value
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "token") {
			// Split by whitespace and get the token value
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				token := strings.TrimSpace(parts[1])
				// Remove any carriage returns or other whitespace
				token = strings.TrimSpace(strings.ReplaceAll(token, "\r", ""))
				return token, nil
			}
		}
	}

	return "", fmt.Errorf("could not parse token from output: %s", output)
}

// executeKubectlRun executes a kubectl run command with the utility image
func executeKubectlRun(config *client.Config, name string, args []string) (string, error) {
	if config.ClusterConfig == nil {
		return "", fmt.Errorf("cluster configuration not available")
	}

	// Build kubectl run command
	cmdArgs := []string{
		"run", name,
		"--image=" + config.ClusterConfig.UtilityImage,
		"--rm", "-i", "--restart=Never",
		"--",
	}
	cmdArgs = append(cmdArgs, args...)

	cmd := buildKubectlCommand(config, cmdArgs)

	if config.Debug {
		logging.Debug("Executing kubectl run: %s", strings.Join(cmd.Args, " "))
	}

	return executeCommand(cmd)
}

// executeKubectlRunWithImage executes a kubectl run command with a custom image
func executeKubectlRunWithImage(config *client.Config, name string, image string, args []string) (string, error) {
	if config.ClusterConfig == nil {
		return "", fmt.Errorf("cluster configuration not available")
	}

	// Build kubectl run command with custom image
	cmdArgs := []string{
		"run", name,
		"--image=" + image,
		"--rm", "-i", "--restart=Never",
		"--",
	}
	cmdArgs = append(cmdArgs, args...)

	cmd := buildKubectlCommand(config, cmdArgs)

	if config.Debug {
		logging.Debug("Executing kubectl run: %s", strings.Join(cmd.Args, " "))
	}

	return executeCommand(cmd)
}

// parseJSONField extracts a field value from JSON output using simple regex
func parseJSONField(jsonStr, fieldName string) (string, error) {
	// Simple regex to extract field values - for production, use proper JSON parser
	pattern := fmt.Sprintf(`"%s"\s*:\s*"([^"]*)"`, fieldName)
	re := regexp.MustCompile(pattern)

	matches := re.FindStringSubmatch(jsonStr)
	if len(matches) < 2 {
		return "", fmt.Errorf("field '%s' not found in JSON", fieldName)
	}

	return matches[1], nil
}

// maskSensitiveData masks sensitive information for logging
func maskSensitiveData(data string) string {
	if len(data) <= 20 {
		if len(data) <= 8 {
			return strings.Repeat("*", len(data))
		}
		return data[:4] + strings.Repeat("*", len(data)-8) + data[len(data)-4:]
	}
	return data[:10] + strings.Repeat("*", len(data)-20) + data[len(data)-10:]
}

// validateClusterAccess checks if we can access the Kubernetes cluster
func validateClusterAccess(config *client.Config) error {
	// Simple check - try to get cluster info
	cmd := buildKubectlCommand(config, []string{"cluster-info"})

	if _, err := executeCommand(cmd); err != nil {
		return fmt.Errorf("cannot access Kubernetes cluster: %w", err)
	}

	return nil
}

// checkNamespaceExists checks if a namespace exists
func checkNamespaceExists(config *client.Config, namespace string) error {
	cmd := buildKubectlCommand(config, []string{"get", "namespace", namespace})

	if _, err := executeCommand(cmd); err != nil {
		return fmt.Errorf("namespace '%s' does not exist: %w", namespace, err)
	}

	return nil
}
