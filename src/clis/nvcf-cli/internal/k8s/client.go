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

package k8s

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"

	"nvcf-cli/internal/logging"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Client wraps the Kubernetes client with helper methods
type Client struct {
	clientset *kubernetes.Clientset
	config    *rest.Config
}

// ClientConfig holds configuration for Kubernetes client creation
type ClientConfig struct {
	KubeconfigPath string
	Debug          bool
}

// NewClient creates a new Kubernetes client with kubeconfig priority handling
func NewClient(config *ClientConfig) (*Client, error) {
	restConfig, err := buildRestConfig(config.KubeconfigPath, config.Debug)
	if err != nil {
		return nil, fmt.Errorf("failed to build Kubernetes config: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	return &Client{
		clientset: clientset,
		config:    restConfig,
	}, nil
}

// buildRestConfig builds the Kubernetes REST config with the same priority as kubectl:
// 1. Explicit kubeconfig path (highest priority)
// 2. KUBECONFIG environment variable
// 3. Default location (~/.kube/config)
// 4. In-cluster config (lowest priority)
func buildRestConfig(kubeconfigPath string, debug bool) (*rest.Config, error) {
	var configPath string

	// Priority 1: Explicit kubeconfig path
	if kubeconfigPath != "" {
		configPath = kubeconfigPath
		if debug {
			logging.Debug("Using explicit kubeconfig path: %s", configPath)
		}
	} else {
		// Priority 2: KUBECONFIG environment variable
		if kubeEnvPath := os.Getenv("KUBECONFIG"); kubeEnvPath != "" {
			configPath = kubeEnvPath
			if debug {
				logging.Debug("Using KUBECONFIG environment variable: %s", configPath)
			}
		} else {
			// Priority 3: Default location
			homeDir, err := os.UserHomeDir()
			if err == nil {
				defaultPath := filepath.Join(homeDir, ".kube", "config")
				if _, err := os.Stat(defaultPath); err == nil {
					configPath = defaultPath
					if debug {
						logging.Debug("Using default kubeconfig location: %s", configPath)
					}
				}
			}
		}
	}

	// Try loading from kubeconfig file first
	if configPath != "" {
		if _, err := os.Stat(configPath); err == nil {
			config, err := clientcmd.BuildConfigFromFlags("", configPath)
			if err != nil {
				return nil, fmt.Errorf("failed to load kubeconfig from %s: %w", configPath, err)
			}
			if debug {
				logging.Debug("Successfully loaded kubeconfig from: %s", configPath)
			}
			return config, nil
		} else {
			if debug {
				logging.Debug("Kubeconfig file not found at %s, trying in-cluster config", configPath)
			}
		}
	}

	// Priority 4: In-cluster config (fallback)
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load in-cluster config and no valid kubeconfig found: %w", err)
	}

	if debug {
		logging.Debug("Using in-cluster Kubernetes configuration")
	}
	return config, nil
}

// GetSecret retrieves a secret from the specified namespace
func (c *Client) GetSecret(ctx context.Context, namespace, name string) (*v1.Secret, error) {
	secret, err := c.clientset.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get secret %s/%s: %w", namespace, name, err)
	}
	return secret, nil
}

// GetSecretData retrieves and decodes a specific key from a secret
func (c *Client) GetSecretData(ctx context.Context, namespace, secretName, key string) (string, error) {
	secret, err := c.GetSecret(ctx, namespace, secretName)
	if err != nil {
		return "", err
	}

	data, exists := secret.Data[key]
	if !exists {
		return "", fmt.Errorf("key %s not found in secret %s/%s", key, namespace, secretName)
	}

	// Data is already decoded by the Kubernetes client
	return string(data), nil
}

// GetBase64SecretData retrieves and base64-decodes a specific key from a secret
func (c *Client) GetBase64SecretData(ctx context.Context, namespace, secretName, key string) (string, error) {
	encodedData, err := c.GetSecretData(ctx, namespace, secretName, key)
	if err != nil {
		return "", err
	}

	decodedData, err := base64.StdEncoding.DecodeString(encodedData)
	if err != nil {
		return "", fmt.Errorf("failed to base64 decode secret data: %w", err)
	}

	return string(decodedData), nil
}

// CheckPodExists verifies if a pod exists in the specified namespace
func (c *Client) CheckPodExists(ctx context.Context, namespace, podName string) (bool, error) {
	_, err := c.clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		// Pod doesn't exist or other error
		return false, err
	}
	return true, nil
}

// GetServiceAccountToken reads the service account token from the mounted path
func GetServiceAccountToken() (string, error) {
	tokenPath := "/var/run/secrets/kubernetes.io/serviceaccount/token"
	tokenBytes, err := os.ReadFile(tokenPath)
	if err != nil {
		return "", fmt.Errorf("failed to read service account token from %s: %w", tokenPath, err)
	}
	return string(tokenBytes), nil
}

// TestConnection tests the Kubernetes client connection
func (c *Client) TestConnection(ctx context.Context) error {
	_, err := c.clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{Limit: 1})
	if err != nil {
		return fmt.Errorf("failed to test Kubernetes connection: %w", err)
	}
	return nil
}
