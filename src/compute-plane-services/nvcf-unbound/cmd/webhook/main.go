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
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/klog/v2"
)

var (
	runtimeScheme = runtime.NewScheme()
	codecs        = serializer.NewCodecFactory(runtimeScheme)
	deserializer  = codecs.UniversalDeserializer()

	port              int
	metricsPort       int
	unboundIP         string
	stubNameserver    string
	certificatesImage string
	webhookConfigName string
	serviceName       string
	namespace         string
	certSecretName    string
)

func init() {
	flag.IntVar(&port, "port", 8443, "Webhook server port")
	flag.IntVar(&metricsPort, "metrics-port", 9090, "Metrics server port")
	flag.StringVar(&unboundIP, "unbound-ip", getEnv("UNBOUND_IP", "10.96.0.100"), "Unbound DNS cluster IP")
	flag.StringVar(&stubNameserver, "stub-nameserver", getEnv("STUB_NAMESERVER", "10.96.0.10"), "Stub nameserver IP")
	flag.StringVar(&certificatesImage, "certificates-image", getEnv("CERTIFICATES_IMAGE", ""), "Certificates image with NVCF certs")
	flag.StringVar(&webhookConfigName, "webhook-config-name", getEnv("WEBHOOK_CONFIG_NAME", ""), "MutatingWebhookConfiguration name")
	flag.StringVar(&serviceName, "service-name", getEnv("SERVICE_NAME", ""), "Webhook Kubernetes Service name")
	flag.StringVar(&namespace, "namespace", getEnv("POD_NAMESPACE", "default"), "Webhook namespace")
	flag.StringVar(&certSecretName, "cert-secret-name", getEnv("CERT_SECRET_NAME", ""), "Secret name for shared TLS certs")
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func main() {
	klog.InitFlags(nil)
	flag.Parse()

	klog.InfoS("Starting NVCF Pod Mutator Webhook",
		"port", port,
		"metricsPort", metricsPort,
		"unboundIP", unboundIP,
		"stubNameserver", stubNameserver,
		"certificatesImage", certificatesImage,
	)

	// Build DNS SANs for the serving certificate
	dnsNames := []string{
		serviceName,
		fmt.Sprintf("%s.%s", serviceName, namespace),
		fmt.Sprintf("%s.%s.svc", serviceName, namespace),
		fmt.Sprintf("%s.%s.svc.cluster.local", serviceName, namespace),
	}

	ctx := context.Background()

	// Load or create certs via a shared Kubernetes Secret so all replicas
	// present the same CA to the API server.
	certBundle, clientset, err := loadOrCreateCerts(ctx, namespace, certSecretName, dnsNames)
	if err != nil {
		klog.ErrorS(err, "Failed to obtain TLS certificates")
		os.Exit(1)
	}

	// Patch MutatingWebhookConfiguration with our CA so the API server trusts us
	if webhookConfigName != "" {
		if err := ensureCABundle(ctx, clientset, webhookConfigName, certBundle.CACertPEM); err != nil {
			klog.ErrorS(err, "Failed to patch webhook configuration — API server calls will fail")
			os.Exit(1)
		}
	} else {
		klog.InfoS("WEBHOOK_CONFIG_NAME not set, skipping CA bundle patch")
	}

	// Metrics server (plain HTTP)
	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", MetricsHandler())
	metricsMux.HandleFunc("/health", serveHealth)

	metricsServer := &http.Server{
		Addr:         fmt.Sprintf(":%d", metricsPort),
		Handler:      metricsMux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	go func() {
		klog.InfoS("Metrics server starting", "address", metricsServer.Addr)
		if err := metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			klog.ErrorS(err, "Metrics server failed")
		}
	}()

	// Webhook HTTPS server
	webhookMux := http.NewServeMux()
	webhookMux.HandleFunc("/mutate", serveMutate)
	webhookMux.HandleFunc("/health", serveHealth)
	webhookMux.HandleFunc("/readyz", serveHealth)

	webhookServer := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: webhookMux,
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{certBundle.ServerCert},
			MinVersion:   tls.VersionTLS12,
		},
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	// Graceful shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-stop
		klog.InfoS("Shutting down")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = webhookServer.Shutdown(ctx)
		_ = metricsServer.Shutdown(ctx)
	}()

	klog.InfoS("Webhook server ready", "address", webhookServer.Addr)
	if err := webhookServer.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
		klog.ErrorS(err, "Webhook server failed")
		os.Exit(1)
	}
}

func serveHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func serveMutate(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	status := "success"

	defer func() {
		duration := time.Since(startTime)
		RecordWebhookRequest("/mutate", status, duration.Seconds())
	}()

	body, err := io.ReadAll(r.Body)
	if err != nil {
		klog.ErrorS(err, "Failed to read request body")
		status = "error"
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	admissionReview := admissionv1.AdmissionReview{}
	if _, _, err := deserializer.Decode(body, nil, &admissionReview); err != nil {
		klog.ErrorS(err, "Failed to decode admission review")
		status = "error"
		http.Error(w, "Failed to decode admission review", http.StatusBadRequest)
		return
	}

	if admissionReview.Request == nil {
		klog.Error("Admission review request is nil")
		status = "error"
		http.Error(w, "Admission review request is nil", http.StatusBadRequest)
		return
	}

	response := mutatePod(admissionReview.Request)

	RecordPodProcessed(admissionReview.Request.Namespace, response.Allowed)

	admissionReview.Response = response
	admissionReview.Response.UID = admissionReview.Request.UID

	duration := time.Since(startTime)
	klog.InfoS("Processed mutation",
		"namespace", admissionReview.Request.Namespace,
		"name", admissionReview.Request.Name,
		"operation", admissionReview.Request.Operation,
		"allowed", response.Allowed,
		"patchSize", len(response.Patch),
		"duration", duration,
	)

	if !response.Allowed {
		status = "rejected"
	}

	responseBytes, err := json.Marshal(admissionReview)
	if err != nil {
		klog.ErrorS(err, "Failed to marshal response")
		status = "error"
		http.Error(w, "Failed to marshal response", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(responseBytes)
}

func mutatePod(req *admissionv1.AdmissionRequest) *admissionv1.AdmissionResponse {
	pod := corev1.Pod{}
	if err := json.Unmarshal(req.Object.Raw, &pod); err != nil {
		klog.ErrorS(err, "Failed to unmarshal pod")
		return &admissionv1.AdmissionResponse{
			Allowed: false,
			Result: &metav1.Status{
				Message: fmt.Sprintf("Failed to unmarshal pod: %v", err),
			},
		}
	}

	klog.V(4).InfoS("Mutating pod",
		"namespace", req.Namespace,
		"name", pod.Name,
		"containers", len(pod.Spec.Containers),
		"initContainers", len(pod.Spec.InitContainers),
	)

	if hasInference, _ := getInferenceContainer(&pod); hasInference {
		RecordInferenceContainerDetected()
	}

	patches := []JSONPatch{}

	certStart := time.Now()
	certPatches := mutateCertificates(&pod)
	certDuration := time.Since(certStart)

	if len(certPatches) > 0 {
		RecordMutation("certificates", len(certPatches), certDuration.Seconds(), true)
	} else {
		RecordMutationSkipped("certificates", "already_applied")
	}
	patches = append(patches, certPatches...)

	dnsStart := time.Now()
	dnsPatches := mutateDNS(&pod)
	dnsDuration := time.Since(dnsStart)

	if len(dnsPatches) > 0 {
		RecordMutation("dns", len(dnsPatches), dnsDuration.Seconds(), true)
	} else {
		RecordMutationSkipped("dns", "already_applied")
	}
	patches = append(patches, dnsPatches...)

	patchBytes, err := json.Marshal(patches)
	if err != nil {
		klog.ErrorS(err, "Failed to marshal patches")
		return &admissionv1.AdmissionResponse{
			Allowed: false,
			Result: &metav1.Status{
				Message: fmt.Sprintf("Failed to marshal patches: %v", err),
			},
		}
	}

	klog.V(4).InfoS("Generated patches",
		"count", len(patches),
		"patchSize", len(patchBytes),
	)

	patchType := admissionv1.PatchTypeJSONPatch
	return &admissionv1.AdmissionResponse{
		Allowed:   true,
		Patch:     patchBytes,
		PatchType: &patchType,
	}
}

// JSONPatch represents a JSON Patch operation (RFC 6902)
type JSONPatch struct {
	Op    string      `json:"op"`
	Path  string      `json:"path"`
	Value interface{} `json:"value,omitempty"`
}
