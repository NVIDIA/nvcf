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

package operator

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
	nvcaoptypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/types"
)

const (
	TLSCAName   = "ca.pem"
	TLSCertName = "tls.crt"
	TLSKeyName  = "tls.key"
)

type WebhookCert struct {
	CACertBytes     []byte
	TLSCert, TLSKey []byte
}

func (bc *BackendK8sCache) setupWebhookSecrets(ctx context.Context, nb *nvidiaiov1.NVCFBackend, webhookCert WebhookCert) error {
	log := core.GetLogger(ctx)
	log.Info("setting-up webhook secrets")

	if err := bc.setupTLSCertSecrets(ctx, nb, webhookCert); err != nil {
		return fmt.Errorf("failed to setup %v for NVCFBackend %v/%v, err: %v", NVCAWebhookTLSCertSecretName,
			nb.Namespace, nb.Name, err)
	}

	if err := bc.setupTLSCASecret(ctx, nb, webhookCert); err != nil {
		return fmt.Errorf("failed to setup %v for NVCFBackend %v/%v, err: %v", NVCAWebhookTLSCASecretName,
			nb.Namespace, nb.Name, err)
	}

	return nil
}

func getWebHooksSvcPort(nb *nvidiaiov1.NVCFBackend) int32 {
	webhookSvcPort := DefaultWebhooksServicePortHTTPS
	if nb.Spec.WebhookConfig.ServicePort != 0 {
		webhookSvcPort = nb.Spec.WebhookConfig.ServicePort
	}
	return webhookSvcPort
}

func getWebHooksListenPort(nb *nvidiaiov1.NVCFBackend) int32 {
	webhookListenPort := DefaultWebhooksListenPortHTTP
	if nb.Spec.WebhookConfig.ListenPort != 0 {
		webhookListenPort = nb.Spec.WebhookConfig.ListenPort
	}
	return webhookListenPort
}

func (bc *BackendK8sCache) setupTLSCertSecrets(ctx context.Context, nb *nvidiaiov1.NVCFBackend, wc WebhookCert) error {
	tlsSec := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        NVCAWebhookTLSCertSecretName,
			Namespace:   getSystemNamespace(nb),
			Annotations: getNBAnnotations(nb),
		},
		Data: map[string][]byte{
			TLSCertName: wc.TLSCert,
			TLSKeyName:  wc.TLSKey,
		},
		Type: v1.SecretTypeTLS,
	}
	return bc.createOrUpdateSecret(ctx, tlsSec)
}

func (bc *BackendK8sCache) setupTLSCASecret(ctx context.Context, nb *nvidiaiov1.NVCFBackend, wc WebhookCert) error {
	tlsSec := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        NVCAWebhookTLSCASecretName,
			Namespace:   getSystemNamespace(nb),
			Annotations: getNBAnnotations(nb),
		},
		Data: map[string][]byte{
			TLSCAName: wc.CACertBytes,
		},
	}
	return bc.createOrUpdateSecret(ctx, tlsSec)
}

func generateWebhookCerts(nb *nvidiaiov1.NVCFBackend, now time.Time) (webhookCert WebhookCert, err error) {
	sLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, sLimit)
	if err != nil {
		return webhookCert, fmt.Errorf("failed to generate serial number for CACert: %v", err)
	}

	// Certs are valid for a year
	notAfter := now.AddDate(1, 0, 0)

	ca := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"NVIDIA"},
			CommonName:   "webhooks-ca",
		},
		SignatureAlgorithm: x509.SHA256WithRSA,
		NotBefore:          now,
		NotAfter:           notAfter,
		ExtKeyUsage:        []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		KeyUsage: x509.KeyUsageKeyEncipherment |
			x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	// rsa keypair
	caPrivKey, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return webhookCert, err
	}

	// create ca cert
	caBytes, err := x509.CreateCertificate(rand.Reader, ca, ca, &caPrivKey.PublicKey, caPrivKey)
	if err != nil {
		return webhookCert, err
	}

	// pem encode
	caPEM := new(bytes.Buffer)
	err = pem.Encode(caPEM, &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: caBytes,
	})
	if err != nil {
		return webhookCert, fmt.Errorf("failed to encode cert, err: %v", err)
	}

	webhookCert.CACertBytes = caPEM.Bytes()

	caPrivKeyPEM := new(bytes.Buffer)
	err = pem.Encode(caPrivKeyPEM, &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(caPrivKey),
	})
	if err != nil {
		return webhookCert, fmt.Errorf("failed to encode cert, err: %v", err)
	}

	serialNumber, err = rand.Int(rand.Reader, sLimit)
	if err != nil {
		return webhookCert, fmt.Errorf("failed to generate serial number for TLSCert: %v", err)
	}

	svcDNSNames := getTLSDNSNames(nb)
	// create webhook tls certificate
	cert := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"NVIDIA"},
			CommonName:   svcDNSNames[0],
		},
		NotBefore:   now,
		NotAfter:    notAfter,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		KeyUsage:    x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		DNSNames:    svcDNSNames,
	}

	certPrivKey, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return webhookCert, err
	}

	certBytes, err := x509.CreateCertificate(rand.Reader, cert, ca, &certPrivKey.PublicKey, caPrivKey)
	if err != nil {
		return webhookCert, err
	}

	certPEM := new(bytes.Buffer)
	err = pem.Encode(certPEM, &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certBytes,
	})
	if err != nil {
		return webhookCert, fmt.Errorf("failed to encode cert, err: %v", err)
	}

	certPrivKeyPEM := new(bytes.Buffer)
	err = pem.Encode(certPrivKeyPEM, &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(certPrivKey),
	})
	if err != nil {
		return webhookCert, fmt.Errorf("failed to encode cert, err: %v", err)
	}

	webhookCert.TLSCert = certPEM.Bytes()
	webhookCert.TLSKey = certPrivKeyPEM.Bytes()
	return webhookCert, err
}

var getTLSDNSNames = func(nb *nvidiaiov1.NVCFBackend) []string {
	svcDNS := fmt.Sprintf("%s.%s.svc", nvcaoptypes.NVCAModuleName, getSystemNamespace(nb))
	return []string{svcDNS, fmt.Sprintf("%s.cluster.local", svcDNS)}
}

func makeLabelSelectorRequirements(labels map[string][]string) []metav1.LabelSelectorRequirement {
	nsLabelSelReqs := make([]metav1.LabelSelectorRequirement, len(labels))
	i := 0
	for lk, lvs := range labels {
		nsLabelSelReqs[i] = metav1.LabelSelectorRequirement{
			Key:      lk,
			Operator: metav1.LabelSelectorOpIn,
			Values:   lvs,
		}
		i++
	}
	return nsLabelSelReqs
}

func makeWebhookClientConfig(
	nb *nvidiaiov1.NVCFBackend,
	webhookCert WebhookCert,
	path string,
) admissionregistrationv1.WebhookClientConfig {
	sport := getWebHooksSvcPort(nb)
	return admissionregistrationv1.WebhookClientConfig{
		CABundle: webhookCert.CACertBytes,
		Service: &admissionregistrationv1.ServiceReference{
			Name:      nvcaoptypes.NVCAModuleName,
			Namespace: getSystemNamespace(nb),
			Path:      &path,
			Port:      &sport,
		},
	}
}

func makeMutatingWebhook(
	webhookName string,
	webhookPath string,
	namespaceSelector *metav1.LabelSelector,
	rules []admissionregistrationv1.RuleWithOperations,
	nb *nvidiaiov1.NVCFBackend,
	webhookCert WebhookCert,
) admissionregistrationv1.MutatingWebhook {
	sec := admissionregistrationv1.SideEffectClassNone
	fpt := admissionregistrationv1.Fail
	mp := admissionregistrationv1.Equivalent
	return admissionregistrationv1.MutatingWebhook{
		Name:                    webhookName,
		AdmissionReviewVersions: []string{"v1"},
		FailurePolicy:           &fpt,
		SideEffects:             &sec,
		MatchPolicy:             &mp,
		ClientConfig:            makeWebhookClientConfig(nb, webhookCert, webhookPath),
		NamespaceSelector:       namespaceSelector,
		Rules:                   rules,
	}
}
