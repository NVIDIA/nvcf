// SPDX-FileCopyrightText: Copyright (c) 2023-2025 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

// loadOrCreateCerts ensures all replicas share the same CA and serving cert.
// It tries to read an existing Secret; if not found it generates fresh certs
// and atomically creates the Secret (losing the race is handled gracefully).
func loadOrCreateCerts(
	ctx context.Context,
	namespace, secretName string,
	dnsNames []string,
) (*CertificateBundle, kubernetes.Interface, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("in-cluster config: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, nil, fmt.Errorf("create kubernetes client: %w", err)
	}

	existingSecret, err := clientset.CoreV1().Secrets(namespace).Get(ctx, secretName, metav1.GetOptions{})
	if err == nil {
		bundle, loadErr := bundleFromSecret(existingSecret)
		if loadErr == nil {
			klog.InfoS("Loaded TLS certificates from existing Secret", "secret", secretName)
			return bundle, clientset, nil
		}
		klog.ErrorS(loadErr, "Existing Secret has invalid certs, replacing", "secret", secretName)

		bundle, err = generateSelfSignedCerts(dnsNames)
		if err != nil {
			return nil, nil, fmt.Errorf("generate certs: %w", err)
		}
		existingSecret.Data = map[string][]byte{
			"ca.crt":  bundle.CACertPEM,
			"tls.crt": bundle.ServerCertPEM,
			"tls.key": bundle.ServerKeyPEM,
		}
		_, err = clientset.CoreV1().Secrets(namespace).Update(ctx, existingSecret, metav1.UpdateOptions{})
		if err != nil {
			return nil, nil, fmt.Errorf("update corrupt secret: %w", err)
		}
		klog.InfoS("Replaced invalid TLS certificate Secret", "secret", secretName, "dnsNames", dnsNames)
		return bundle, clientset, nil
	}

	if !errors.IsNotFound(err) {
		return nil, nil, fmt.Errorf("get secret: %w", err)
	}

	bundle, err := generateSelfSignedCerts(dnsNames)
	if err != nil {
		return nil, nil, fmt.Errorf("generate certs: %w", err)
	}

	newSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "nvcf-pod-mutator",
			},
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"ca.crt":  bundle.CACertPEM,
			"tls.crt": bundle.ServerCertPEM,
			"tls.key": bundle.ServerKeyPEM,
		},
	}

	_, err = clientset.CoreV1().Secrets(namespace).Create(ctx, newSecret, metav1.CreateOptions{})
	if errors.IsAlreadyExists(err) {
		klog.InfoS("Secret already created by another replica, loading it", "secret", secretName)
		secret, err := clientset.CoreV1().Secrets(namespace).Get(ctx, secretName, metav1.GetOptions{})
		if err != nil {
			return nil, nil, fmt.Errorf("get secret after race: %w", err)
		}
		bundle, err = bundleFromSecret(secret)
		if err != nil {
			return nil, nil, fmt.Errorf("parse secret after race: %w", err)
		}
		return bundle, clientset, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("create secret: %w", err)
	}

	klog.InfoS("Created TLS certificate Secret", "secret", secretName, "dnsNames", dnsNames)
	return bundle, clientset, nil
}

func bundleFromSecret(secret *corev1.Secret) (*CertificateBundle, error) {
	caCert := secret.Data["ca.crt"]
	tlsCert := secret.Data["tls.crt"]
	tlsKey := secret.Data["tls.key"]

	if len(caCert) == 0 || len(tlsCert) == 0 || len(tlsKey) == 0 {
		return nil, fmt.Errorf("secret is missing required keys (ca.crt, tls.crt, tls.key)")
	}

	serverCert, err := tls.X509KeyPair(tlsCert, tlsKey)
	if err != nil {
		return nil, fmt.Errorf("parse TLS key pair: %w", err)
	}

	return &CertificateBundle{
		CACertPEM:    caCert,
		ServerCertPEM: tlsCert,
		ServerKeyPEM:  tlsKey,
		ServerCert:   serverCert,
	}, nil
}

// ensureCABundle patches the MutatingWebhookConfiguration with the CA bundle
// so the API server trusts the webhook's serving certificate.
func ensureCABundle(ctx context.Context, clientset kubernetes.Interface, webhookConfigName string, caBundle []byte) error {
	var lastErr error
	for attempt := 1; attempt <= 10; attempt++ {
		wc, err := clientset.AdmissionregistrationV1().
			MutatingWebhookConfigurations().
			Get(ctx, webhookConfigName, metav1.GetOptions{})
		if err != nil {
			lastErr = err
			klog.ErrorS(err, "Failed to get webhook configuration",
				"webhookConfig", webhookConfigName, "attempt", attempt)
			time.Sleep(time.Duration(attempt) * time.Second)
			continue
		}

		needsUpdate := false
		for i := range wc.Webhooks {
			if !bytes.Equal(wc.Webhooks[i].ClientConfig.CABundle, caBundle) {
				wc.Webhooks[i].ClientConfig.CABundle = caBundle
				needsUpdate = true
			}
		}

		if !needsUpdate {
			klog.InfoS("Webhook CA bundle already up to date", "webhookConfig", webhookConfigName)
			return nil
		}

		_, err = clientset.AdmissionregistrationV1().
			MutatingWebhookConfigurations().
			Update(ctx, wc, metav1.UpdateOptions{})
		if err != nil {
			lastErr = err
			klog.ErrorS(err, "Failed to update webhook configuration",
				"webhookConfig", webhookConfigName, "attempt", attempt)
			time.Sleep(time.Duration(attempt) * time.Second)
			continue
		}

		klog.InfoS("Patched webhook configuration with CA bundle",
			"webhookConfig", webhookConfigName)
		return nil
	}

	return fmt.Errorf("failed after 10 attempts: %w", lastErr)
}
