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

package mirror

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	cli "github.com/urfave/cli/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	// AdditionalImagePullSecretsEnvVar is the environment variable containing base64-encoded JSON
	AdditionalImagePullSecretsEnvVar = "NVCA_ADDITIONAL_IMAGE_PULL_SECRETS_JSON_BASE64"
	// SourceNamespaceEnvVar is the environment variable for the source namespace (set via downward API)
	SourceNamespaceEnvVar  = "NVCA_MIRROR_SOURCE_NAMESPACE"
	DefaultSourceNamespace = "nvca-operator"
	DefaultTargetNamespace = "nvca-system"
)

// ReservedSecretNames contains secret names managed by NVCA Operator that cannot be specified
// as additional image pull secrets
// TODO(mcamp): standardize with labels so we could look this list up on startup, the issue
// is that we will have to refetch the list since the secrets are not yet created on
// initial startup (or at least may not be).
var ReservedSecretNames = sets.New(
	"nvca-image-pull-secret",
	"ngc-service-api-key",
	"ngc-api-key",
	"oauth-client-secret-key",
	"oauth-client-id",
	"otel-nvca-config",
	"nvca-webhook-tls-server-certs",
	"nvca-webhook-tls-ca-certs",
)

// DecodeImagePullSecrets decodes base64 JSON into a slice of LocalObjectReference
// and validates that no reserved secret names are specified
func DecodeImagePullSecrets(b64 string) ([]corev1.LocalObjectReference, error) {
	if b64 == "" {
		return []corev1.LocalObjectReference{}, nil
	}
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return []corev1.LocalObjectReference{}, fmt.Errorf("failed to decode base64: %w", err)
	}
	var secrets []corev1.LocalObjectReference
	if err = json.Unmarshal(data, &secrets); err != nil {
		return []corev1.LocalObjectReference{}, fmt.Errorf("failed to unmarshal JSON: %w", err)
	}
	// Filter out empty names and validate against reserved names
	result := make([]corev1.LocalObjectReference, 0, len(secrets))
	for _, s := range secrets {
		if s.Name == "" {
			continue
		}
		// Check if this is a reserved secret name
		if ReservedSecretNames.Has(s.Name) {
			return nil, fmt.Errorf("secret name %q is reserved and managed by NVCA Operator, cannot be specified as an additional image pull secret", s.Name)
		}
		result = append(result, s)
	}
	return result, nil
}

// NewRunCommand creates the run command for the secret sync sidecar
func NewRunCommand() *cli.Command {
	return &cli.Command{
		Name:  "run",
		Usage: "Run the secret sync controller",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "source-namespace",
				Usage:   "Source namespace to watch for secrets",
				Value:   DefaultSourceNamespace,
				EnvVars: []string{SourceNamespaceEnvVar, "SOURCE_NAMESPACE"},
			},
			&cli.StringFlag{
				Name:    "target-namespace",
				Usage:   "Target namespace to copy secrets to",
				Value:   DefaultTargetNamespace,
				EnvVars: []string{"TARGET_NAMESPACE"},
			},
			&cli.StringFlag{
				Name:    "kubeconfig",
				Usage:   "Path to kubeconfig file (optional, for local testing)",
				EnvVars: []string{"KUBECONFIG"},
			},
			&cli.StringFlag{
				Name:    "log-level",
				Usage:   "Log level (debug, info, warn, error)",
				Value:   "info",
				EnvVars: []string{"LOG_LEVEL"},
			},
			&cli.DurationFlag{
				Name:    "resync-period",
				Usage:   "Period at which to resync all secrets (e.g., 30m, 1h, 2h)",
				Value:   DefaultResyncPeriod,
				EnvVars: []string{"RESYNC_PERIOD"},
			},
			&cli.StringFlag{
				Name:    "additional-image-pull-secrets-b64",
				Usage:   "base64 encoded JSON array of imagePullSecret objects with name field",
				EnvVars: []string{AdditionalImagePullSecretsEnvVar},
			},
		},
		Action: runAction,
	}
}

func runAction(c *cli.Context) error {
	ctx := c.Context
	log := core.GetLogger(ctx)

	sourceNamespace := c.String("source-namespace")
	targetNamespace := c.String("target-namespace")
	kubeconfigPath := c.String("kubeconfig")
	resyncPeriod := c.Duration("resync-period")

	// Decode additional image pull secrets from base64 encoded flag
	additionalSecrets, err := DecodeImagePullSecrets(c.String("additional-image-pull-secrets-b64"))
	if err != nil {
		return fmt.Errorf("failed to decode additional image pull secrets: %w", err)
	}

	// Extract secret names from LocalObjectReference objects
	secretNames := make([]string, 0, len(additionalSecrets))
	for _, ref := range additionalSecrets {
		secretNames = append(secretNames, ref.Name)
	}

	// Setup Kubernetes client (needed for cleanup even when no secrets configured)
	config, err := getKubeConfig(kubeconfigPath)
	if err != nil {
		return fmt.Errorf("failed to get Kubernetes config: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	if len(secretNames) == 0 {
		log.Info("No additional image pull secrets configured, cleaning up any existing secrets")

		// Cleanup any existing secrets with the additional-image-pull-secret label
		if err := CleanupAllAdditionalSecrets(ctx, clientset, targetNamespace); err != nil {
			log.WithError(err).Warn("Failed to cleanup additional image pull secrets")
			// Non-fatal, continue
		}

		log.Info("Cleanup complete, blocking until context is canceled")
		<-ctx.Done()
		log.Info("Context canceled, stopping nvca-mirror sidecar")
		return nil
	}

	log.WithFields(map[string]interface{}{
		"sourceNamespace": sourceNamespace,
		"targetNamespace": targetNamespace,
		"secretNames":     secretNames,
		"resyncPeriod":    resyncPeriod,
	}).Info("Starting nvca-mirror sidecar")

	// Create and run the controller
	controller := NewController(
		clientset,
		sourceNamespace,
		targetNamespace,
		secretNames,
		resyncPeriod,
	)

	if err := controller.Run(ctx); err != nil {
		return fmt.Errorf("controller failed: %w", err)
	}

	log.Info("nvca-mirror sidecar stopped")
	return nil
}

// getKubeConfig returns the Kubernetes configuration
func getKubeConfig(kubeconfigPath string) (*rest.Config, error) {
	if kubeconfigPath != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	}
	return rest.InClusterConfig()
}
