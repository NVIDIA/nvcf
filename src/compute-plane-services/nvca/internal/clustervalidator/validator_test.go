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

package clustervalidator

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

func testLog() *logrus.Entry {
	l := logrus.New()
	l.SetLevel(logrus.DebugLevel)
	return logrus.NewEntry(l)
}

func TestVersionGTE(t *testing.T) {
	tests := []struct {
		name string
		v1   string
		v2   string
		want bool
	}{
		{"equal", "1.16.0", "1.16.0", true},
		{"greater major", "2.0.0", "1.16.0", true},
		{"greater minor", "1.17.0", "1.16.0", true},
		{"greater patch", "1.16.1", "1.16.0", true},
		{"less major", "0.16.0", "1.16.0", false},
		{"less minor", "1.15.0", "1.16.0", false},
		{"less patch", "1.16.0", "1.16.1", false},
		{"with v prefix", "v1.16.0", "1.16.0", true},
		{"with pre-release", "1.16.0-rc1", "1.16.0", true},
		{"invalid v1", "abc", "1.16.0", false},
		{"invalid v2", "1.16.0", "abc", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := versionGTE(tt.v1, tt.v2)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCheckPrerequisites(t *testing.T) {
	ctx := context.Background()

	t.Run("healthy cluster", func(t *testing.T) {
		client := fake.NewSimpleClientset(
			makeNode("node-1", true, 0),
			makeNode("node-2", true, 0),
		)
		state := &ValidationState{Log: testLog(), ControlPlaneHealthy: true}
		err := checkPrerequisites(ctx, client, state)
		assert.NoError(t, err)
		assert.Equal(t, "2", state.TotalNodes)
	})
}

func TestCheckControlPlaneHealth(t *testing.T) {
	ctx := context.Background()

	t.Run("all healthy", func(t *testing.T) {
		client := fake.NewSimpleClientset(
			makeNode("node-1", true, 0),
			makePod("kube-apiserver-node-1", "kube-system", corev1.PodRunning),
			makePod("kube-controller-manager-node-1", "kube-system", corev1.PodRunning),
			makePod("kube-scheduler-node-1", "kube-system", corev1.PodRunning),
			makePod("etcd-node-1", "kube-system", corev1.PodRunning),
			makePod("coredns-abc123", "kube-system", corev1.PodRunning),
			makePod("kube-proxy-xyz789", "kube-system", corev1.PodRunning),
		)
		state := &ValidationState{Log: testLog(), ControlPlaneHealthy: true}
		checkControlPlaneHealth(ctx, client, state)
		assert.True(t, state.ControlPlaneHealthy)
	})

	t.Run("not-ready node", func(t *testing.T) {
		client := fake.NewSimpleClientset(
			makeNode("node-1", true, 0),
			makeNode("node-2", false, 0),
		)
		state := &ValidationState{Log: testLog(), ControlPlaneHealthy: true}
		checkControlPlaneHealth(ctx, client, state)
		assert.False(t, state.ControlPlaneHealthy)
		assert.NotEmpty(t, state.Recommendations)
	})
}

func TestCheckWebhookSupport(t *testing.T) {
	ctx := context.Background()

	// The fake discovery client does not register API group resources, so
	// discoverWebhookAPIs will return false. We test that the function runs
	// without panics and that listing existing webhooks works.
	t.Run("lists existing webhooks without panic", func(t *testing.T) {
		sideEffect := admissionregistrationv1.SideEffectClassNone
		client := fake.NewSimpleClientset(
			&admissionregistrationv1.MutatingWebhookConfiguration{
				ObjectMeta: metav1.ObjectMeta{Name: "test-mutating"},
				Webhooks: []admissionregistrationv1.MutatingWebhook{{
					Name:                    "test.webhook.io",
					SideEffects:             &sideEffect,
					AdmissionReviewVersions: []string{"v1"},
					ClientConfig:            admissionregistrationv1.WebhookClientConfig{URL: strPtr("https://localhost")},
				}},
			},
		)
		state := &ValidationState{Log: testLog()}
		checkWebhookSupport(ctx, client, state)
		// Fake discovery does not populate ServerResourcesForGroupVersion,
		// so WebhooksSupported will be false. This is a limitation of the
		// fake client, not of our code.
		assert.False(t, state.WebhooksSupported)
	})
}

func TestCheckSMBCSIDriver(t *testing.T) {
	ctx := context.Background()

	t.Run("driver installed with valid version", func(t *testing.T) {
		client := fake.NewSimpleClientset(
			&storagev1.CSIDriver{
				ObjectMeta: metav1.ObjectMeta{Name: "smb.csi.k8s.io"},
			},
			&appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "csi-smb-controller",
					Namespace: "kube-system",
				},
				Spec: appsv1.DeploymentSpec{
					Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "csi-smb"}},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "csi-smb"}},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{
								Name:  "smb",
								Image: "registry.k8s.io/sig-storage/smbplugin:v1.16.0",
							}},
						},
					},
				},
			},
		)
		state := &ValidationState{Log: testLog()}
		checkSMBCSIDriver(ctx, client, state)
		assert.True(t, state.SMBCSIDriverOK)
	})

	t.Run("driver not installed", func(t *testing.T) {
		client := fake.NewSimpleClientset()
		state := &ValidationState{Log: testLog()}
		checkSMBCSIDriver(ctx, client, state)
		assert.False(t, state.SMBCSIDriverOK)
		assert.NotEmpty(t, state.Recommendations)
	})
}

func TestCheckGPUResources(t *testing.T) {
	ctx := context.Background()

	t.Run("GPU nodes present", func(t *testing.T) {
		client := fake.NewSimpleClientset(
			makeNode("gpu-node-1", true, 4),
		)
		state := &ValidationState{Log: testLog()}
		checkGPUResources(ctx, client, state)
		assert.True(t, state.GPUAvailable)
	})

	t.Run("no GPU nodes", func(t *testing.T) {
		client := fake.NewSimpleClientset(
			makeNode("cpu-node-1", true, 0),
		)
		state := &ValidationState{Log: testLog()}
		checkGPUResources(ctx, client, state)
		assert.False(t, state.GPUAvailable)
		assert.NotEmpty(t, state.Recommendations)
	})
}

func TestCheckGPUOperator(t *testing.T) {
	ctx := context.Background()

	t.Run("operator installed", func(t *testing.T) {
		client := fake.NewSimpleClientset(
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "gpu-operator"}},
			makePod("gpu-operator-abc123", "gpu-operator", corev1.PodRunning),
		)
		state := &ValidationState{Log: testLog()}
		checkGPUOperator(ctx, client, state)
		assert.True(t, state.GPUOperatorInstalled)
	})

	t.Run("operator not installed", func(t *testing.T) {
		client := fake.NewSimpleClientset()
		state := &ValidationState{Log: testLog()}
		checkGPUOperator(ctx, client, state)
		assert.False(t, state.GPUOperatorInstalled)
		assert.NotEmpty(t, state.Recommendations)
	})
}

func TestCheckNetworkPolicies(t *testing.T) {
	ctx := context.Background()

	// Fake discovery doesn't populate ServerResourcesForGroupVersion, so
	// the check exits early. We verify it doesn't panic.
	t.Run("runs without panic on fake client", func(t *testing.T) {
		client := fake.NewSimpleClientset(
			&corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "calico-node-abc",
					Namespace: "kube-system",
					Labels:    map[string]string{"k8s-app": "calico-node"},
				},
				Status: corev1.PodStatus{Phase: corev1.PodRunning},
			},
		)
		state := &ValidationState{Log: testLog()}
		checkNetworkPolicies(ctx, client, state)
		// The fake discovery returns an error for ServerResourcesForGroupVersion,
		// so the function exits early and NetworkPoliciesSupported stays false.
		assert.False(t, state.NetworkPoliciesSupported)
	})
}

func TestTestHTTPS(t *testing.T) {
	t.Run("unreachable host returns false", func(t *testing.T) {
		assert.False(t, testHTTPS("https://192.0.2.1:1")) // RFC 5737 TEST-NET, guaranteed non-routable
	})

	t.Run("invalid URL returns false", func(t *testing.T) {
		assert.False(t, testHTTPS("://not-a-url"))
	})
}

func TestTestTCP(t *testing.T) {
	t.Run("reachable TCP port", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer ln.Close()

		_, portStr, _ := net.SplitHostPort(ln.Addr().String())
		port := 0
		fmt.Sscanf(portStr, "%d", &port)

		assert.True(t, testTCP("127.0.0.1", port, false))
	})

	t.Run("unreachable TCP port", func(t *testing.T) {
		assert.False(t, testTCP("192.0.2.1", 1, false))
	})

	t.Run("unreachable host", func(t *testing.T) {
		assert.False(t, testTCP("invalid.test.example", 443, false))
	})

	t.Run("TLS handshake error counts as reachable", func(t *testing.T) {
		ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
			Certificates: []tls.Certificate{selfSignedCert(t)},
		})
		require.NoError(t, err)
		defer ln.Close()

		go func() {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}()

		_, portStr, _ := net.SplitHostPort(ln.Addr().String())
		port := 0
		fmt.Sscanf(portStr, "%d", &port)

		assert.True(t, testTCP("127.0.0.1", port, true))
	})

	t.Run("unreachable TLS port", func(t *testing.T) {
		assert.False(t, testTCP("192.0.2.1", 443, true))
	})
}

func TestTestEndpoint(t *testing.T) {
	t.Run("unknown protocol", func(t *testing.T) {
		ep := Endpoint{Protocol: "grpc", Host: "localhost", Port: 1234}
		assert.False(t, TestEndpoint(ep))
	})
}

func TestPrintSummary(t *testing.T) {
	t.Run("cluster ready", func(t *testing.T) {
		state := &ValidationState{
			Log:                      testLog(),
			ControlPlaneHealthy:      true,
			WebhooksSupported:        true,
			NetworkPoliciesSupported: true,
			SMBCSIDriverOK:           true,
			GPUAvailable:             true,
			GPUOperatorInstalled:     true,
			K8sVersion:               "v1.28.0",
			TotalNodes:               "3",
		}
		err := printSummary(state)
		assert.NoError(t, err)
	})

	t.Run("cluster not ready", func(t *testing.T) {
		state := &ValidationState{
			Log:                 testLog(),
			ControlPlaneHealthy: false,
			K8sVersion:          "v1.28.0",
			TotalNodes:          "1",
		}
		err := printSummary(state)
		assert.Error(t, err)
	})

	t.Run("no reachability config is excluded", func(t *testing.T) {
		state := &ValidationState{
			Log:                      testLog(),
			ControlPlaneHealthy:      true,
			WebhooksSupported:        true,
			NetworkPoliciesSupported: true,
			SMBCSIDriverOK:           true,
			GPUAvailable:             true,
			GPUOperatorInstalled:     true,
			K8sVersion:               "v1.28.0",
			TotalNodes:               "3",
		}
		err := printSummary(state)
		assert.NoError(t, err)
	})

	t.Run("reachability all pass", func(t *testing.T) {
		ok := true
		critOK := true
		state := &ValidationState{
			Log:                      testLog(),
			ControlPlaneHealthy:      true,
			WebhooksSupported:        true,
			NetworkPoliciesSupported: true,
			SMBCSIDriverOK:           true,
			GPUAvailable:             true,
			GPUOperatorInstalled:     true,
			ReachabilityOK:           &ok,
			ReachabilityCriticalOK:   &critOK,
			K8sVersion:               "v1.28.0",
			TotalNodes:               "3",
		}
		err := printSummary(state)
		assert.NoError(t, err)
	})

	t.Run("reachability critical fail blocks readiness", func(t *testing.T) {
		fail := false
		critFail := false
		state := &ValidationState{
			Log:                      testLog(),
			ControlPlaneHealthy:      true,
			WebhooksSupported:        true,
			NetworkPoliciesSupported: true,
			SMBCSIDriverOK:           true,
			GPUAvailable:             true,
			GPUOperatorInstalled:     true,
			ReachabilityOK:           &fail,
			ReachabilityCriticalOK:   &critFail,
			K8sVersion:               "v1.28.0",
			TotalNodes:               "3",
		}
		err := printSummary(state)
		assert.Error(t, err, "critical endpoint failure must block readiness")
	})

	t.Run("reachability non-critical fail is warning", func(t *testing.T) {
		fail := false
		critOK := true
		state := &ValidationState{
			Log:                      testLog(),
			ControlPlaneHealthy:      true,
			WebhooksSupported:        true,
			NetworkPoliciesSupported: true,
			SMBCSIDriverOK:           true,
			GPUAvailable:             true,
			GPUOperatorInstalled:     true,
			ReachabilityOK:           &fail,
			ReachabilityCriticalOK:   &critOK,
			K8sVersion:               "v1.28.0",
			TotalNodes:               "3",
		}
		err := printSummary(state)
		assert.NoError(t, err, "only non-critical failures should not block readiness")
	})

	t.Run("reachability no critical endpoints", func(t *testing.T) {
		fail := false
		state := &ValidationState{
			Log:                      testLog(),
			ControlPlaneHealthy:      true,
			WebhooksSupported:        true,
			NetworkPoliciesSupported: true,
			SMBCSIDriverOK:           true,
			GPUAvailable:             true,
			GPUOperatorInstalled:     true,
			ReachabilityOK:           &fail,
			K8sVersion:               "v1.28.0",
			TotalNodes:               "3",
		}
		err := printSummary(state)
		assert.NoError(t, err, "no critical endpoints means failures are non-critical")
	})

	t.Run("configurable netpol fail is non-critical", func(t *testing.T) {
		fail := false
		state := &ValidationState{
			Log:                      testLog(),
			ControlPlaneHealthy:      true,
			WebhooksSupported:        true,
			NetworkPoliciesSupported: true,
			SMBCSIDriverOK:           true,
			GPUAvailable:             true,
			GPUOperatorInstalled:     true,
			ConfigurableNetPolOK:     &fail,
			K8sVersion:               "v1.28.0",
			TotalNodes:               "3",
		}
		err := printSummary(state)
		assert.NoError(t, err, "configurable netpol failures are non-critical")
	})

	t.Run("enforcement pass", func(t *testing.T) {
		ok := true
		state := &ValidationState{
			Log:                      testLog(),
			ControlPlaneHealthy:      true,
			WebhooksSupported:        true,
			NetworkPoliciesSupported: true,
			SMBCSIDriverOK:           true,
			GPUAvailable:             true,
			GPUOperatorInstalled:     true,
			EnforcementOK:            &ok,
			K8sVersion:               "v1.28.0",
			TotalNodes:               "3",
		}
		err := printSummary(state)
		assert.NoError(t, err)
	})

	t.Run("enforcement fail is non-critical", func(t *testing.T) {
		fail := false
		state := &ValidationState{
			Log:                      testLog(),
			ControlPlaneHealthy:      true,
			WebhooksSupported:        true,
			NetworkPoliciesSupported: true,
			SMBCSIDriverOK:           true,
			GPUAvailable:             true,
			GPUOperatorInstalled:     true,
			EnforcementOK:            &fail,
			K8sVersion:               "v1.28.0",
			TotalNodes:               "3",
		}
		err := printSummary(state)
		assert.NoError(t, err, "enforcement failures are non-critical")
	})
}

func TestIsTLSOrProtocolError(t *testing.T) {
	t.Run("tls keyword in error", func(t *testing.T) {
		assert.True(t, isTLSOrProtocolError(fmt.Errorf("tls: handshake failure")))
	})

	t.Run("certificate required in error", func(t *testing.T) {
		assert.True(t, isTLSOrProtocolError(fmt.Errorf("remote error: certificate required")))
	})

	t.Run("bad status line", func(t *testing.T) {
		assert.True(t, isTLSOrProtocolError(fmt.Errorf("bad status line from server")))
	})

	t.Run("malformed HTTP", func(t *testing.T) {
		assert.True(t, isTLSOrProtocolError(fmt.Errorf("malformed HTTP response")))
	})

	t.Run("generic connection refused", func(t *testing.T) {
		assert.False(t, isTLSOrProtocolError(fmt.Errorf("connection refused")))
	})

	t.Run("certificate verification error", func(t *testing.T) {
		assert.True(t, isTLSOrProtocolError(&tls.CertificateVerificationError{}))
	})

	t.Run("OpError wrapping tls verification", func(t *testing.T) {
		assert.True(t, isTLSOrProtocolError(&net.OpError{
			Op:  "read",
			Err: &tls.CertificateVerificationError{},
		}))
	})

	t.Run("OpError with non-tls error", func(t *testing.T) {
		assert.False(t, isTLSOrProtocolError(&net.OpError{
			Op:  "read",
			Err: fmt.Errorf("some other error"),
		}))
	})
}

func TestTestEndpoint_TCP(t *testing.T) {
	ep := Endpoint{Protocol: "tcp", Host: "192.0.2.1", Port: 1}
	assert.False(t, TestEndpoint(ep))
}

func TestTestEndpoint_TCPTLS(t *testing.T) {
	ep := Endpoint{Protocol: "tcp+tls", Host: "192.0.2.1", Port: 1}
	assert.False(t, TestEndpoint(ep))
}

func TestToEndpoint(t *testing.T) {
	ep := toEndpoint(ReachabilityEndpoint{
		URL:      "https://example.com",
		Host:     "example.com",
		Port:     443,
		Protocol: "https",
	})
	assert.Equal(t, "https://example.com", ep.URL)
	assert.Equal(t, "example.com", ep.Host)
	assert.Equal(t, 443, ep.Port)
	assert.Equal(t, "https", ep.Protocol)
}

func TestCheckConfigurableReachability_AllUnreachable(t *testing.T) {
	state := &ValidationState{Log: testLog()}
	cfg := &ReachabilityConfig{
		Endpoints: []ReachabilityEndpoint{
			{Name: "bad-ep", Host: "192.0.2.1", Port: 1, Protocol: "tcp"},
		},
	}
	checkConfigurableReachability(state, cfg)
	require.NotNil(t, state.ReachabilityOK)
	assert.False(t, *state.ReachabilityOK)
}

func TestCheckConfigurableReachability_CriticalFail(t *testing.T) {
	state := &ValidationState{Log: testLog()}
	cfg := &ReachabilityConfig{
		Endpoints: []ReachabilityEndpoint{
			{Name: "critical-ep", Host: "192.0.2.1", Port: 1, Protocol: "tcp", Critical: true},
		},
	}
	checkConfigurableReachability(state, cfg)
	require.NotNil(t, state.ReachabilityOK)
	assert.False(t, *state.ReachabilityOK)
	require.NotNil(t, state.ReachabilityCriticalOK)
	assert.False(t, *state.ReachabilityCriticalOK)
	assert.NotEmpty(t, state.Recommendations)
}

func TestCheckConfigurableReachability_Unreachable(t *testing.T) {
	state := &ValidationState{Log: testLog()}
	cfg := &ReachabilityConfig{
		Endpoints: []ReachabilityEndpoint{
			{Name: "ep", Host: "192.0.2.1", Port: 1, Protocol: "tcp"},
		},
	}
	checkConfigurableReachability(state, cfg)
	require.NotNil(t, state.ReachabilityOK)
	assert.False(t, *state.ReachabilityOK)
}

func TestCheckConfigurableReachability_NonCriticalFailOnly(t *testing.T) {
	state := &ValidationState{Log: testLog()}
	cfg := &ReachabilityConfig{
		Endpoints: []ReachabilityEndpoint{
			{Name: "non-crit", Host: "192.0.2.1", Port: 1, Protocol: "tcp", Critical: false},
		},
	}
	checkConfigurableReachability(state, cfg)
	require.NotNil(t, state.ReachabilityOK)
	assert.False(t, *state.ReachabilityOK)
	assert.Nil(t, state.ReachabilityCriticalOK)
	assert.NotEmpty(t, state.Warnings)
}

func TestEndpointDisplayAddr(t *testing.T) {
	t.Run("URL endpoint", func(t *testing.T) {
		ep := Endpoint{URL: "https://example.com", Protocol: "https"}
		assert.Equal(t, "https://example.com", ep.DisplayAddr())
	})

	t.Run("TCP endpoint", func(t *testing.T) {
		ep := Endpoint{Host: "example.com", Port: 4222, Protocol: "tcp"}
		assert.Equal(t, "example.com:4222", ep.DisplayAddr())
	})
}

// --- helpers ---

func makeNode(name string, ready bool, gpus int) *corev1.Node {
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}

	capacity := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("8"),
		corev1.ResourceMemory: resource.MustParse("32Gi"),
	}
	if gpus > 0 {
		capacity["nvidia.com/gpu"] = *resource.NewQuantity(int64(gpus), resource.DecimalSI)
	}

	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{
				Type:   corev1.NodeReady,
				Status: status,
			}},
			Capacity:    capacity,
			Allocatable: capacity.DeepCopy(),
		},
	}
}

func makePod(name, namespace string, phase corev1.PodPhase) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Status: corev1.PodStatus{Phase: phase},
	}
}

func strPtr(s string) *string {
	return &s
}

func selfSignedCert(t *testing.T) tls.Certificate {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{"localhost"},
	}
	derBytes, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)

	return tls.Certificate{
		Certificate: [][]byte{derBytes},
		PrivateKey:  key,
	}
}

func init() {
	// Register corev1 and other types with the fake client scheme.
	_ = []runtime.Object{
		&corev1.Node{},
		&corev1.Pod{},
		&corev1.Namespace{},
	}
}
