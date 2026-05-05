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

package webhook

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"
	admissionv1 "k8s.io/api/admission/v1"
	v1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/enforce/kata"
)

func generateTestCertificates(t *testing.T) (caCert, caKey, cert, key []byte) {
	// Generate CA key
	caPrivKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	// Generate CA cert
	caTemplate := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName:   "webhooks-ca",
			Organization: []string{"NVIDIA"},
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	caCertBytes, err := x509.CreateCertificate(rand.Reader, &caTemplate, &caTemplate, &caPrivKey.PublicKey, caPrivKey)
	require.NoError(t, err)

	// Generate server key
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	// Generate server cert
	template := x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject: pkix.Name{
			CommonName:   "localhost",
			Organization: []string{"NVIDIA"},
		},
		NotBefore:   time.Now(),
		NotAfter:    time.Now().Add(24 * time.Hour),
		KeyUsage:    x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:    []string{"localhost"},
	}

	certBytes, err := x509.CreateCertificate(rand.Reader, &template, &caTemplate, &privKey.PublicKey, caPrivKey)
	require.NoError(t, err)

	// Encode to PEM
	caCertPEM := new(bytes.Buffer)
	pem.Encode(caCertPEM, &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: caCertBytes,
	})

	caKeyPEM := new(bytes.Buffer)
	pem.Encode(caKeyPEM, &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(caPrivKey),
	})

	certPEM := new(bytes.Buffer)
	pem.Encode(certPEM, &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certBytes,
	})

	keyPEM := new(bytes.Buffer)
	pem.Encode(keyPEM, &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privKey),
	})

	return caCertPEM.Bytes(), caKeyPEM.Bytes(), certPEM.Bytes(), keyPEM.Bytes()
}

func TestCmd(t *testing.T) {
	cmd := NewCommand()
	app := &cli.App{
		Name:   cmd.Name,
		Usage:  cmd.Usage,
		Flags:  cmd.Flags,
		Action: cmd.Action,
	}

	ctx := context.Background()

	addr := newLocalhostAddr(t)

	// Generate test certificates
	caCert1, _, cert1, key1 := generateTestCertificates(t)

	certFilePath := filepath.Join(t.TempDir(), "tls.crt")
	keyFilePath := filepath.Join(t.TempDir(), "tls.key")
	require.NoError(t, os.WriteFile(certFilePath, cert1, 0600))
	require.NoError(t, os.WriteFile(keyFilePath, key1, 0600))

	rootCAs1 := x509.NewCertPool()
	require.True(t, rootCAs1.AppendCertsFromPEM(caCert1))

	trpt1 := http.DefaultTransport.(*http.Transport).Clone()
	trpt1.TLSHandshakeTimeout = 250 * time.Millisecond
	trpt1.TLSClientConfig = &tls.Config{
		RootCAs: rootCAs1,
	}
	client := &http.Client{
		Transport: trpt1,
	}

	args := []string{
		"webhook-server",
		"--listen", addr,
		"--tls-key-file", keyFilePath,
		"--tls-cert-file", certFilePath,
	}

	var runErr error
	runReturned := make(chan struct{})
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		tmpFunc := newK8sClient
		newK8sClient = func(ctx context.Context, path string) (kubernetes.Interface, error) {
			return k8sfake.NewSimpleClientset(), nil
		}
		runErr = app.RunContext(ctx, args)
		runReturned <- struct{}{}
		newK8sClient = tmpFunc
	}()

	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		req := newWebhookAdmissionReviewRequestPods(t, addr+"/mutate-pod-nodeaffinity")
		res, err := client.Do(req)
		if !assert.NoError(ct, err) {
			return
		}
		_, err = io.ReadAll(res.Body)
		if assert.NoError(ct, err) {
			assert.EqualValues(ct, http.StatusOK, res.StatusCode)
		}
	}, 5*time.Second, 100*time.Millisecond)

	// Service req/res limit tests.
	var bodySize int64 = 5000000
	cm := &v1.ConfigMap{}
	cm.APIVersion, cm.Kind = "v1", "ConfigMap"
	cm.Name, cm.Namespace = "foo", "bar"
	cmData := &bytes.Buffer{}

	// Test with len(body) < 7MB.
	_, err := io.Copy(cmData, io.LimitReader(rand.Reader, bodySize))
	require.NoError(t, err)
	cm.Data = map[string]string{"key": base64.StdEncoding.EncodeToString(cmData.Bytes())}
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		req := newWebhookAdmissionReviewRequestObject(t, addr+"/nvca-mutating-webhook", cm)
		res, err := client.Do(req)
		if !assert.NoError(ct, err) {
			return
		}
		if !assert.EqualValues(ct, http.StatusOK, res.StatusCode) {
			return
		}
		b, err := io.ReadAll(res.Body)
		if !assert.NoError(ct, err) {
			return
		}
		admres := admissionv1.AdmissionReview{}
		if err := json.Unmarshal(b, &admres); !assert.NoError(ct, err) {
			return
		}
		if assert.NotNil(ct, admres.Response) && assert.NotNil(ct, admres.Response.Result) {
			assert.True(ct, admres.Response.Allowed)
			assert.EqualValues(ct, http.StatusOK, admres.Response.Result.Code, string(b))
		}
	}, 5*time.Second, 100*time.Millisecond)

	// Test with len(body) > 7MB.
	cmData.Reset()
	_, err = io.Copy(cmData, io.LimitReader(rand.Reader, bodySize*2))
	require.NoError(t, err)
	cm.Data = map[string]string{"key": base64.StdEncoding.EncodeToString(cmData.Bytes())}
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		req := newWebhookAdmissionReviewRequestObject(t, addr+"/nvca-mutating-webhook", cm)
		res, err := client.Do(req)
		if !assert.NoError(ct, err) {
			return
		}
		if !assert.EqualValues(ct, http.StatusOK, res.StatusCode) {
			return
		}
		b, err := io.ReadAll(res.Body)
		if !assert.NoError(ct, err) {
			return
		}
		admres := admissionv1.AdmissionReview{}
		if err := json.Unmarshal(b, &admres); !assert.NoError(ct, err) {
			return
		}
		if assert.NotNil(ct, admres.Response) && assert.NotNil(ct, admres.Response.Result) {
			assert.False(ct, admres.Response.Allowed)
			assert.EqualValues(ct, http.StatusRequestEntityTooLarge, admres.Response.Result.Code, string(b))
		}
	}, 5*time.Second, 100*time.Millisecond)

	cancel()
	<-runReturned
	assert.NoError(t, runErr)
}

func TestManagerRunTLS(t *testing.T) {
	ctx := context.Background()
	ctx = core.WithDefaultLogger(ctx)

	addr := newLocalhostAddr(t)

	certFilePath := filepath.Join(t.TempDir(), "tls.crt")
	keyFilePath := filepath.Join(t.TempDir(), "tls.key")

	// Generate test certificates
	caCert1, _, cert1, key1 := generateTestCertificates(t)

	rootCAs1 := x509.NewCertPool()
	require.True(t, rootCAs1.AppendCertsFromPEM(caCert1))

	trpt1 := http.DefaultTransport.(*http.Transport).Clone()
	trpt1.TLSHandshakeTimeout = 250 * time.Millisecond
	trpt1.TLSClientConfig = &tls.Config{
		RootCAs: rootCAs1,
	}
	client := &http.Client{
		Transport: trpt1,
	}

	m := &webhookManager{
		cfg: nvcaconfig.Config{
			Webhook: nvcaconfig.WebhookConfig{
				SvcAddress:  addr,
				TLSCertFile: certFilePath,
				TLSKeyFile:  keyFilePath,
			},
		},
		readTimeout:  100 * time.Millisecond,
		writeTimeout: 100 * time.Millisecond,
		k8sClient:    k8sfake.NewSimpleClientset(),
		addNodePublisher: func(ctx context.Context, inf cache.SharedIndexInformer) (*atomic.Bool, cache.InformerSynced, error) {
			return &atomic.Bool{}, nil, nil
		},
		attrFetcher: &mockAttrFetcher{
			attrEnabledFunc: func(a *featureflag.Attribute) bool {
				return a.Key == featureflag.AttrKataRuntimeIsolation.Key
			},
		},
	}

	err := m.startSharedClusterPubSub(ctx, 0)
	require.NoError(t, err)

	nonGPURTClass := &nodev1.RuntimeClass{}
	nonGPURTClass.Name = kata.RuntimeClassNameNonGPU
	_, err = m.k8sClient.NodeV1().RuntimeClasses().Create(ctx, nonGPURTClass, metav1.CreateOptions{})
	require.NoError(t, err)
	m.startKataRuntimeClassHandler(ctx)

	// Expect initial error when files do not exits.
	var runErr error
	runReturned := make(chan struct{})
	ctx1, cancel1 := context.WithCancel(ctx)
	go func() {
		runErr = m.run(ctx1)
		runReturned <- struct{}{}
	}()
	var clientErr error
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		req := newWebhookAdmissionReviewRequestPods(ct, addr+"/mutate-pod-nodeaffinity")
		_, clientErr = client.Do(req)
		assert.ErrorContains(ct, clientErr, "TLS handshake timeout")
	}, 5*time.Second, 100*time.Millisecond)

	cancel1()
	<-runReturned
	assert.NoError(t, runErr)

	// Create files then run again.
	require.NoError(t, os.WriteFile(certFilePath, cert1, 0600))
	require.NoError(t, os.WriteFile(keyFilePath, key1, 0600))
	addr = newLocalhostAddr(t)
	m.cfg.Webhook.SvcAddress = addr

	ctx2, cancel2 := context.WithCancel(ctx)
	go func() {
		runErr = m.run(ctx2)
		runReturned <- struct{}{}
	}()

	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		req := newWebhookAdmissionReviewRequestPods(t, addr+"/mutate-pod-nodeaffinity")
		res, err := client.Do(req)
		if !assert.NoError(ct, err) {
			return
		}
		_, err = io.ReadAll(res.Body)
		if assert.NoError(ct, err) {
			assert.EqualValues(ct, http.StatusOK, res.StatusCode)
		}
	}, 5*time.Second, 100*time.Millisecond)

	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		req := newWebhookAdmissionReviewRequestPods(t, addr+"/mutate-pod-enforcement", &v1.Pod{
			Spec: v1.PodSpec{Containers: []v1.Container{{}}},
		})
		res, err := client.Do(req)
		if !assert.NoError(ct, err) {
			return
		}
		body, err := io.ReadAll(res.Body)
		if assert.NoError(ct, err) {
			assert.EqualValues(ct, http.StatusOK, res.StatusCode)
			patches, allowed := decodePatches(t, body)
			if !assert.True(t, allowed) {
				return
			}
			assert.ElementsMatch(t, []map[string]any{
				{
					"op": "add", "path": "/spec/runtimeClassName",
					"value": "kata-qemu",
				},
				{
					"op": "add", "path": "/metadata/annotations",
					"value": map[string]any{enforcementMutatedAnnotationKey: "true"},
				},
			}, patches)
		}
	}, 5*time.Second, 100*time.Millisecond)

	cancel2()
	<-runReturned
	assert.NoError(t, runErr)
}

func TestManagerRunTLSSecretInformer(t *testing.T) {
	ctx := context.Background()
	ctx = core.WithDefaultLogger(ctx)

	certFilePath := filepath.Join(t.TempDir(), "tls.crt")
	keyFilePath := filepath.Join(t.TempDir(), "tls.key")

	// Generate test certificates
	caCert1, _, cert1, key1 := generateTestCertificates(t)
	caCert2, _, cert2, key2 := generateTestCertificates(t)

	rootCAs1 := x509.NewCertPool()
	require.True(t, rootCAs1.AppendCertsFromPEM(caCert1))

	trpt1 := http.DefaultTransport.(*http.Transport).Clone()
	trpt1.TLSHandshakeTimeout = 250 * time.Millisecond
	trpt1.TLSClientConfig = &tls.Config{
		RootCAs: rootCAs1,
	}
	client := &http.Client{
		Transport: trpt1,
	}

	m := &webhookManager{
		cfg: nvcaconfig.Config{
			Webhook: nvcaconfig.WebhookConfig{
				TLSCertFile:   certFilePath,
				TLSKeyFile:    keyFilePath,
				TLSSecretName: "test-tls-secret",
			},
		},
		readTimeout:  100 * time.Millisecond,
		writeTimeout: 100 * time.Millisecond,
		k8sClient:    k8sfake.NewSimpleClientset(),
		addNodePublisher: func(ctx context.Context, inf cache.SharedIndexInformer) (*atomic.Bool, cache.InformerSynced, error) {
			return &atomic.Bool{}, nil, nil
		},
		attrFetcher: &mockAttrFetcher{
			attrEnabledFunc: func(a *featureflag.Attribute) bool {
				return a.Key == featureflag.AttrKataRuntimeIsolation.Key
			},
		},
		namespace: "nvca-system",
	}

	err := m.startSharedClusterPubSub(ctx, 0)
	require.NoError(t, err)

	nonGPURTClass := &nodev1.RuntimeClass{}
	nonGPURTClass.Name = kata.RuntimeClassNameNonGPU
	_, err = m.k8sClient.NodeV1().RuntimeClasses().Create(ctx, nonGPURTClass, metav1.CreateOptions{})
	require.NoError(t, err)
	m.startKataRuntimeClassHandler(ctx)

	var runErr error
	runReturned := make(chan struct{})

	require.NoError(t, os.WriteFile(certFilePath, cert1, 0600))
	require.NoError(t, os.WriteFile(keyFilePath, key1, 0600))
	m.cfg.Webhook.SvcAddress = newLocalhostAddr(t)
	webhookEndpoint := m.cfg.Webhook.SvcAddress + "/mutate-pod-nodeaffinity"

	// The secret must exist prior to start.
	_, err = m.k8sClient.CoreV1().Secrets(m.namespace).Create(ctx, &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.cfg.Webhook.TLSSecretName,
			Namespace: m.namespace,
		},
		Data: map[string][]byte{
			"tls.crt": cert1,
			"tls.key": key1,
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	ctx2, cancel2 := context.WithCancel(ctx)
	t.Cleanup(cancel2)
	go func() {
		runErr = m.run(ctx2)
		runReturned <- struct{}{}
	}()

	// Using the default client should fail with TLS error.
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		req := newWebhookAdmissionReviewRequestPods(t, webhookEndpoint)
		_, err := http.DefaultClient.Do(req)
		assert.EqualError(ct, err, `Post "https://`+webhookEndpoint+`": `+
			`tls: failed to verify certificate: x509: certificate signed by unknown authority`)
	}, 5*time.Second, 100*time.Millisecond)

	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		req := newWebhookAdmissionReviewRequestPods(t, webhookEndpoint)
		res, err := client.Do(req)
		if !assert.NoError(ct, err) {
			return
		}
		_, err = io.ReadAll(res.Body)
		if assert.NoError(ct, err) {
			assert.EqualValues(ct, http.StatusOK, res.StatusCode)
		}
	}, 5*time.Second, 100*time.Millisecond)

	_, err = m.k8sClient.CoreV1().Secrets(m.namespace).Update(ctx, &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.cfg.Webhook.TLSSecretName,
			Namespace: m.namespace,
		},
		Data: map[string][]byte{
			"tls.crt": cert2,
			"tls.key": key2,
			"foo":     []byte("blah"),
			"empty":   {},
		},
	}, metav1.UpdateOptions{})
	require.NoError(t, err)

	rootCAs2 := x509.NewCertPool()
	require.True(t, rootCAs2.AppendCertsFromPEM(caCert2))

	trpt2 := http.DefaultTransport.(*http.Transport).Clone()
	trpt2.TLSHandshakeTimeout = 250 * time.Millisecond
	trpt2.TLSClientConfig = &tls.Config{
		RootCAs: rootCAs2,
	}
	client2 := &http.Client{
		Transport: trpt2,
	}

	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		req := newWebhookAdmissionReviewRequestPods(t, webhookEndpoint)
		res, err := client2.Do(req)
		if !assert.NoError(ct, err) {
			return
		}
		_, err = io.ReadAll(res.Body)
		if assert.NoError(ct, err) {
			assert.EqualValues(ct, http.StatusOK, res.StatusCode)
		}
	}, 5*time.Second, 100*time.Millisecond)

	// Using the old client should fail with TLS error.
	req := newWebhookAdmissionReviewRequestPods(t, webhookEndpoint)
	_, err = client.Do(req)
	require.EqualError(t, err, `Post "https://`+webhookEndpoint+`": `+
		`tls: failed to verify certificate: x509: certificate signed by unknown authority `+
		`(possibly because of "crypto/rsa: verification error" while trying to verify `+
		`candidate authority certificate "webhooks-ca")`)

	cancel2()
	<-runReturned
	assert.NoError(t, runErr)
}

func newWebhookAdmissionReviewRequestPods(t require.TestingT, u string, pods ...*v1.Pod) *http.Request {
	b := newPodAdmissionReviewRequestBodyPods(t, pods...)
	return newWebhookAdmissionReviewRequest(t, u, b)
}

func newWebhookAdmissionReviewRequestObject(t require.TestingT, u string, obj client.Object) *http.Request {
	b := newPodAdmissionReviewRequestBody(t, obj)
	return newWebhookAdmissionReviewRequest(t, u, b)
}

func newWebhookAdmissionReviewRequest(t require.TestingT, u string, b []byte) *http.Request {
	r, err := http.NewRequest("POST", "https://"+u, bytes.NewBuffer(b))
	require.NoError(t, err)
	r.Header.Set("Content-Type", "application/json")
	return r
}

func newLocalhostAddr(t *testing.T) string {
	tmpLis, err := net.Listen("tcp", "127.0.0.1:")
	require.NoError(t, err)
	_, port, err := net.SplitHostPort(tmpLis.Addr().String())
	tmpLis.Close()
	require.NoError(t, err)
	return "localhost:" + port
}

func decodePatches(t *testing.T, resBody []byte) (patches []map[string]any, allowed bool) {
	t.Helper()
	var arSpec struct {
		Response struct {
			Allowed bool   `json:"allowed"`
			Patch   string `json:"patch"`
		} `json:"response"`
	}
	err := json.Unmarshal(resBody, &arSpec)
	require.NoError(t, err)

	if !arSpec.Response.Allowed {
		return nil, false
	}

	patchStr, err := base64.StdEncoding.DecodeString(arSpec.Response.Patch)
	require.NoError(t, err)

	patches = []map[string]any{}
	err = json.Unmarshal([]byte(patchStr), &patches)
	require.NoError(t, err)

	return patches, true
}
