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
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	ktesting "k8s.io/client-go/testing"
)

// failWith makes every verb call on resource fail with err, and counts them.
func failWith(client *fake.Clientset, verb, resource string, err error) *atomic.Int32 {
	var calls atomic.Int32
	client.PrependReactor(verb, resource, func(ktesting.Action) (bool, runtime.Object, error) {
		calls.Add(1)
		return true, nil, err
	})
	return &calls
}

// Every row that reads the cluster leaves its result unknown when the read
// fails, with a warning naming the resource and the cause. Only a denial gets
// RBAC advice, and only a transient error is retried. The discovery rows fail
// through the fake's "group" and "resource" actions.
func TestObservationErrors_LeaveEachRowUnknownWithTheCause(t *testing.T) {
	// The Envoy namespace is read only when it is named.
	t.Setenv(envoyGatewayNamespaceEnv, envoyGatewayNamespace)
	t.Setenv(nvcfGatewayNamesEnv, "")
	envoyNS := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: envoyGatewayNamespace}}
	type check func(context.Context, *fake.Clientset, *ValidationState)
	unobserved := func(key string) func(*ValidationState) bool {
		return func(s *ValidationState) bool { return s.Unobserved[key] }
	}
	rows := []struct {
		name, verb, resource string
		objs                 []runtime.Object
		run                  check
		unknown              func(*ValidationState) bool
		warning              string
	}{
		{"StorageClass", "list", "storageclasses", nil,
			func(ctx context.Context, c *fake.Clientset, s *ValidationState) { checkStorageClass(ctx, c, s) },
			func(s *ValidationState) bool { return s.DefaultStorageClassOK == nil },
			"Default StorageClass: status unknown (could not read StorageClasses: "},
		{"Gateway API CRDs", "get", "group", nil,
			func(ctx context.Context, c *fake.Clientset, s *ValidationState) { checkGatewayAPICRDs(ctx, c, s) },
			func(s *ValidationState) bool { return s.GatewayAPICRDsOK == nil },
			"Gateway API CRDs: status unknown (could not read the Gateway API resources: listing API groups: "},
		{"Gateway route types", "get", "group", nil,
			func(ctx context.Context, c *fake.Clientset, s *ValidationState) { checkGatewayRoutes(ctx, c, s) },
			func(s *ValidationState) bool { return s.GatewayRoutesOK == nil },
			"Gateway Route CR Types: status unknown (could not read the Gateway API resources: "},
		{"Envoy namespace", "get", "namespaces", nil,
			func(ctx context.Context, c *fake.Clientset, s *ValidationState) { checkEnvoyGateway(ctx, c, s) },
			func(s *ValidationState) bool { return s.EnvoyGatewayOK == nil },
			"Envoy Gateway: status unknown (could not read namespace envoy-gateway-system: "},
		{"Envoy controller pods", "list", "pods", []runtime.Object{envoyNS},
			func(ctx context.Context, c *fake.Clientset, s *ValidationState) { checkEnvoyGateway(ctx, c, s) },
			func(s *ValidationState) bool { return s.EnvoyGatewayOK == nil },
			"Envoy Gateway: status unknown (could not read the Envoy Gateway controller: " +
				"listing Envoy Gateway controller pods: "},
		{"LoadBalancer Services", "list", "services", nil,
			func(ctx context.Context, c *fake.Clientset, s *ValidationState) {
				checkExternalLoadBalancer(ctx, c, nil, s)
			},
			func(s *ValidationState) bool { return s.ExternalLBOK == nil },
			"External Load Balancer: status unknown (could not read Services: "},
		{"Node-to-Node nodes", "list", "nodes", nil,
			func(ctx context.Context, c *fake.Clientset, s *ValidationState) {
				checkNodeToNode(ctx, c, s, enforcementDefaultImg)
			},
			func(s *ValidationState) bool { return s.NodeToNodeOK == nil },
			"Node-to-Node: status unknown (could not read nodes: "},
		{"Tier-1 Deployments", "list", "deployments", nil,
			func(ctx context.Context, c *fake.Clientset, s *ValidationState) {
				checkTier1Deployments(ctx, c, nil, s)
			},
			func(s *ValidationState) bool { return s.Tier1DeploymentsOK == nil },
			"Tier-1 Deployments: could not read Deployments in 10 control-plane namespace(s): nvcf ("},
		{"Tier-2 StatefulSets", "list", "statefulsets", nil,
			func(ctx context.Context, c *fake.Clientset, s *ValidationState) { checkTier2StatefulSets(ctx, c, s) },
			func(s *ValidationState) bool { return s.Tier2StatefulSetsOK == nil },
			"Tier-2 StatefulSets: could not read StatefulSets in 10 control-plane namespace(s): nvcf ("},
		{"Worker nodes", "list", "nodes", nil,
			func(ctx context.Context, c *fake.Clientset, s *ValidationState) { checkControlPlaneHealth(ctx, c, s) },
			unobserved(CheckKeyWorkerNodesAllReady),
			"Worker Nodes: status unknown (could not read nodes: "},
		{"Admission webhooks", "get", "resource", nil,
			func(ctx context.Context, c *fake.Clientset, s *ValidationState) { checkWebhookSupport(ctx, c, s) },
			unobserved(CheckKeyWebhooks),
			"Admission Webhooks: status unknown (could not read the admissionregistration.k8s.io/v1 API: "},
		{"NetworkPolicy API", "get", "resource", nil,
			func(ctx context.Context, c *fake.Clientset, s *ValidationState) { checkNetworkPolicies(ctx, c, s) },
			unobserved(CheckKeyNetworkPoliciesSupport),
			"Network Policies: status unknown (could not read the networking.k8s.io/v1 API: "},
		{"GPU resources", "list", "nodes", nil,
			func(ctx context.Context, c *fake.Clientset, s *ValidationState) { checkGPUResources(ctx, c, s) },
			unobserved(CheckKeyGPUResources),
			GPUResourcesLabel + ": status unknown (could not read nodes: "},
		{"SMB CSI driver", "get", "csidrivers", nil,
			func(ctx context.Context, c *fake.Clientset, s *ValidationState) { checkSMBCSIDriver(ctx, c, s) },
			unobserved(CheckKeySMBCSI),
			"SMB CSI Driver: status unknown (could not read CSIDriver smb.csi.k8s.io: "},
		{"GPU Operator", "get", "namespaces", nil,
			func(ctx context.Context, c *fake.Clientset, s *ValidationState) { checkGPUOperator(ctx, c, s) },
			unobserved(CheckKeyGPUOperator),
			"GPU Operator: status unknown (could not read the GPU Operator namespace and pods: "},
	}
	causes := []struct {
		name      string
		err       func(resource string) error
		advice    string
		notAdvice string
		retried   bool
	}{
		{"403", func(resource string) error {
			return apierrors.NewForbidden(schema.GroupResource{Resource: resource}, "", errors.New("denied"))
		}, "grant the cluster-validator ServiceAccount get and list on it", "check apiserver health", false},
		{"500", func(string) error { return apierrors.NewInternalError(errors.New("etcd timeout")) },
			"check apiserver health and re-run", "grant the cluster-validator", true},
	}
	for _, row := range rows {
		for _, cause := range causes {
			t.Run(row.name+"/"+cause.name, func(t *testing.T) {
				client := fake.NewSimpleClientset(row.objs...)
				err := cause.err(row.resource)
				calls := failWith(client, row.verb, row.resource, err)
				state := &ValidationState{Log: testLog()}
				row.run(context.Background(), client, state)

				assert.True(t, row.unknown(state), "a failed read is not a result")
				joined := strings.Join(state.Warnings, "; ")
				assert.Contains(t, joined, row.warning)
				assert.Contains(t, joined, err.Error(), "the cause is named")
				assert.Contains(t, joined, cause.advice)
				assert.NotContains(t, joined, cause.notAdvice)
				if cause.retried {
					assert.Greater(t, calls.Load(), int32(1), "a transient error is retried")
				}
			})
		}
	}
}

// A transient error that clears is retried into a result; a denial, a missing
// object and an ended context are not retried.
func TestObserve_RetriesOnlyWhatCanClear(t *testing.T) {
	calls := 0
	got, err := observe(context.Background(), func(context.Context) (string, error) {
		calls++
		if calls < 3 {
			return "", apierrors.NewTooManyRequestsError("slow down")
		}
		return "ok", nil
	})
	require.NoError(t, err)
	assert.Equal(t, "ok", got)
	assert.Equal(t, 3, calls)

	for name, terminal := range map[string]error{
		"forbidden":    apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "", errors.New("denied")),
		"unauthorized": apierrors.NewUnauthorized("bad token"),
		"not found":    apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, "p"),
	} {
		calls = 0
		_, err = observe(context.Background(), func(context.Context) (string, error) {
			calls++
			return "", terminal
		})
		assert.Equal(t, terminal, err, name)
		assert.Equal(t, 1, calls, name)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls = 0
	_, err = observe(ctx, func(context.Context) (string, error) {
		calls++
		return "", apierrors.NewInternalError(errors.New("boom"))
	})
	require.Error(t, err)
	assert.Equal(t, 1, calls, "an ended run is not retried")
}

// Discovery takes no context in client-go. A stalled aggregated API must not
// hold the run past its deadline.
func TestDiscoverGatewayAPIResources_EndsWithItsContext(t *testing.T) {
	client := fake.NewSimpleClientset()
	release := make(chan struct{})
	defer close(release)
	client.PrependReactor("get", "group", func(ktesting.Action) (bool, runtime.Object, error) {
		<-release
		return false, nil, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	finishesWithin(t, 5*time.Second, func() {
		_, err := discoverGatewayAPIResources(ctx, client)
		require.ErrorIs(t, err, context.DeadlineExceeded)
	})
}

// Run discovers the Gateway API surface once and shares it.
func TestRun_DiscoversTheGatewayAPIOnce(t *testing.T) {
	client := gatewayDiscoveryClient(gatewayRequiredPairs()...)
	var groupCalls atomic.Int32
	client.PrependReactor("get", "group", func(ktesting.Action) (bool, runtime.Object, error) {
		groupCalls.Add(1)
		return false, nil, nil
	})
	_ = Run(context.Background(), client, routeClient(), "", "", "", false, RoleControlPlane)
	assert.Equal(t, int32(1), groupCalls.Load())
}

// readyComputeCluster passes every critical compute-plane check.
func readyComputeCluster(objs ...runtime.Object) *fake.Clientset {
	client := fake.NewSimpleClientset(append([]runtime.Object{makeNode("gpu-1", true, 4)}, objs...)...)
	disco := client.Discovery().(*fakediscovery.FakeDiscovery)
	disco.Resources = append(disco.Resources, &metav1.APIResourceList{
		GroupVersion: "admissionregistration.k8s.io/v1",
		APIResources: []metav1.APIResource{
			{Name: "mutatingwebhookconfigurations"}, {Name: "validatingwebhookconfigurations"},
		},
	})
	return client
}

func runCaptured(t *testing.T, client kubernetes.Interface, configNS, configName string, role Role) (error, string) {
	t.Helper()
	buf := &bytes.Buffer{}
	l := logrus.New()
	l.SetOutput(buf)
	ctx := core.WithLogger(context.Background(), logrus.NewEntry(l))
	err := Run(ctx, client, nil, configNS, configName, "", false, role)
	return err, buf.String()
}

// A network-checks ConfigMap that cannot be loaded may hide critical checks,
// so the run cannot be Ready: it says why, and shows a critical unknown row.
func TestRun_UnloadableNetworkChecksAreACriticalUnknown(t *testing.T) {
	const ns, name = "nvca-system", "cluster-validator-network-checks"
	err, out := runCaptured(t, readyComputeCluster(), ns, name, RoleComputePlane)
	require.NoError(t, err, "the baseline cluster is Ready, and an absent ConfigMap is no config")
	assert.NotContains(t, out, "Network Checks")

	cm := func(data string) *corev1.ConfigMap {
		return &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Data:       map[string]string{configDataKey: data},
		}
	}
	unreadable := readyComputeCluster()
	failWith(unreadable, "get", "configmaps", apierrors.NewInternalError(errors.New("etcd timeout")))
	for label, client := range map[string]*fake.Clientset{
		"read fails":    unreadable,
		"invalid entry": readyComputeCluster(cm("reachability:\n  endpoints:\n  - name: api\n    protocol: https\n")),
		"malformed":     readyComputeCluster(cm("reachability: [")),
		"key missing": readyComputeCluster(&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		}),
	} {
		err, out := runCaptured(t, client, ns, name, RoleComputePlane)
		assert.Error(t, err, label)
		assert.Contains(t, out, "Network Checks: Status Unknown", label)
		assert.Contains(t, out, "Network checks: could not load "+ns+"/"+name, label)
	}
}

// The checks may use up the run's deadline, but the summary is still
// published: its write does not inherit the ended context. A run cancelled
// before it finished, as on SIGTERM, publishes nothing and exits so the Job
// retries it, not with the Not-Ready code the Job is failed on.
func TestRun_PublishesTheSummaryAfterItsDeadlineOnly(t *testing.T) {
	t.Setenv(RequireSummaryEnv, "true")
	client, published := summaryRecordingClient(t)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	_ = Run(ctx, client, nil, "", "", "nvca-system", true, RoleComputePlane)
	assert.True(t, published.Load(), "the summary must be written after the checks' deadline")

	client, published = summaryRecordingClient(t)
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	err := Run(ctx, client, nil, "", "", "nvca-system", true, RoleComputePlane)
	require.ErrorIs(t, err, ErrInterrupted)
	assert.False(t, published.Load(), "an interrupted run must not publish")
	assert.Equal(t, 1, ExitCode(err))
}

// summaryRecordingClient serves the version and records whether a summary
// ConfigMap was created.
func summaryRecordingClient(t *testing.T) (kubernetes.Interface, *atomic.Bool) {
	t.Helper()
	published := &atomic.Bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/version":
			fmt.Fprint(w, `{"major":"1","minor":"30","gitVersion":"v1.30.0"}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/configmaps"):
			published.Store(true)
			w.WriteHeader(http.StatusCreated)
			_, _ = io.Copy(w, r.Body)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"NotFound","code":404}`)
		}
	}))
	t.Cleanup(srv.Close)
	client, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL})
	require.NoError(t, err)
	return client, published
}

// An unobserved GPU count is a critical unknown, and its key is not published.
func TestPrintSummary_UnobservedGPUIsACriticalUnknown(t *testing.T) {
	buf := &bytes.Buffer{}
	l := logrus.New()
	l.SetOutput(buf)
	state := &ValidationState{
		Log: logrus.NewEntry(l), ControlPlaneHealthy: true, NodesAllReady: true, WebhooksSupported: true,
		NetworkPoliciesSupported: true, SMBCSIDriverOK: true, GPUOperatorInstalled: true,
		Unobserved: map[string]bool{CheckKeyGPUResources: true},
	}
	require.Error(t, printSummary(state))
	assert.Contains(t, buf.String(), GPUResourcesLabel+": Status Unknown (not observed)")
	assert.NotContains(t, buf.String(), GPUResourcesLabel+": Not Available")
	_, published := buildSummary(state, time.Now(), false, VerdictNotReady).Checks[CheckKeyGPUResources]
	assert.False(t, published)
}

// A Deployment list that fails in one namespace is reported even when another
// namespace's Deployment fails the row.
func TestCheckTier1Deployments_UnreadNamespaceIsReportedBesideAFailure(t *testing.T) {
	three := int32(3)
	client := fake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "nvcf", Generation: 1},
		Spec:       appsv1.DeploymentSpec{Replicas: &three},
		Status:     appsv1.DeploymentStatus{ObservedGeneration: 1, UpdatedReplicas: 3, ReadyReplicas: 1},
	})
	client.PrependReactor("list", "deployments", func(a ktesting.Action) (bool, runtime.Object, error) {
		if a.GetNamespace() == "sis" {
			return true, nil, apierrors.NewForbidden(
				schema.GroupResource{Group: "apps", Resource: "deployments"}, "", errors.New("denied"))
		}
		return false, nil, nil
	})
	state := &ValidationState{Log: testLog()}
	checkTier1Deployments(context.Background(), client, nil, state)

	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.False(t, *state.Tier1DeploymentsOK)
	assert.Contains(t, strings.Join(state.Warnings, "; "),
		"Tier-1 Deployments: could not read Deployments in 1 control-plane namespace(s): sis (")
}

// A Gateway seen not to exist fails the row even when the GatewayClass list
// that follows fails: the later error does not discard the earlier finding.
func TestCheckTier1Deployments_MissingGatewayOutlivesAClassListError(t *testing.T) {
	t.Setenv(envoyGatewayNamespaceEnv, "")
	t.Setenv(nvcfGatewayNamesEnv, "nvcf/nvcf-gw")
	for name, err := range map[string]error{
		"403": apierrors.NewForbidden(
			schema.GroupResource{Group: gatewayAPIGroup, Resource: "gatewayclasses"}, "", errors.New("denied")),
		"500": apierrors.NewInternalError(errors.New("etcd timeout")),
	} {
		routes := envoyGatewayClient(t)
		routes.PrependReactor("list", "gatewayclasses", func(ktesting.Action) (bool, runtime.Object, error) {
			return true, nil, err
		})
		log, buf := bufferLog()
		state := &ValidationState{Log: log, PostInstall: true}
		runTier1On(t, state, routes, nvcfService())
		require.NotNil(t, state.Tier1DeploymentsOK, name)
		assert.False(t, *state.Tier1DeploymentsOK, name)
		assert.Contains(t, buf.String(), "NVCF Gateway nvcf/nvcf-gw does not exist", name)
	}

	// A Gateway that exists, with its class unreadable, stays undecided.
	routes := envoyGatewayClient(t, gatewayObject("nvcf", "nvcf-gw", "eg"))
	routes.PrependReactor("list", "gatewayclasses", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewInternalError(errors.New("etcd timeout"))
	})
	state := runTier1(t, true, routes, nvcfService())
	assert.Nil(t, state.Tier1DeploymentsOK)
	assert.Contains(t, strings.Join(state.Warnings, "; "), "check apiserver health")
}

// A proxy Service outside the Gateway's namespace is credited only in the
// controller's namespace, so with the controller unreadable its Gateway is
// unknown, not missing.
func TestCheckExternalLoadBalancer_UncreditedProxyIsUnknown(t *testing.T) {
	t.Setenv(envoyGatewayNamespaceEnv, "")
	t.Setenv(nvcfGatewayNamesEnv, "nvcf/a-gw")
	client := routeDiscoveryClient()
	addServices(t, client,
		gatewayLBService("gateway-proxies", "envoy-a", "nvcf", "a-gw", corev1.ServiceTypeLoadBalancer, "203.0.113.1"))
	client.PrependReactor("list", "deployments", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewInternalError(errors.New("etcd timeout"))
	})
	state := &ValidationState{Log: testLog(), PostInstall: true}
	checkExternalLoadBalancer(context.Background(), client, routeClient(), state)
	assert.Nil(t, state.ExternalLBOK)
	assert.Contains(t, strings.Join(state.Warnings, "; "), "the Envoy Gateway controller namespace is unknown")
}

// A Gateway whose proxy Service a failed list may hold is unknown, not
// missing, and the warning names the cause and what to do about it.
func TestCheckExternalLoadBalancer_FailedProxyListLeavesUnseenGatewaysUnknown(t *testing.T) {
	t.Setenv(envoyGatewayNamespaceEnv, "")
	t.Setenv(nvcfGatewayNamesEnv, "nvcf/a-gw,nvcf/b-gw")
	client := fake.NewSimpleClientset(envoyController(envoyGatewayNamespace),
		gatewayLBService(envoyGatewayNamespace, "envoy-a", "nvcf", "a-gw", corev1.ServiceTypeLoadBalancer,
			"203.0.113.1"))
	client.PrependReactor("list", "services", func(a ktesting.Action) (bool, runtime.Object, error) {
		if a.(ktesting.ListAction).GetListRestrictions().Labels.String() == owningGatewayClassLabel {
			return true, nil, apierrors.NewInternalError(errors.New("etcd timeout"))
		}
		return false, nil, nil
	})
	state := &ValidationState{Log: testLog()}
	checkExternalLoadBalancer(context.Background(), client, nil, state)
	assert.Nil(t, state.ExternalLBOK, "b-gw's proxy Service may be in the list that failed")
	joined := strings.Join(state.Warnings, "; ")
	assert.Contains(t, joined,
		"status unknown (no proxy Service confirmed for nvcf/b-gw: listing Envoy proxy Services:")
	assert.Contains(t, joined, "check apiserver health")
	assert.NotContains(t, joined, "no proxy Service found")
}

// Only a denial is answered with RBAC advice. A throttled or failed route
// list is not fixed by grants the launchers already give.
func TestCheckExternalLoadBalancer_DiscoveryErrorAdviceFollowsTheCause(t *testing.T) {
	t.Setenv(nvcfGatewayNamesEnv, "")
	for name, tc := range map[string]struct {
		err    error
		advice string
	}{
		"403": {apierrors.NewForbidden(schema.GroupResource{Group: gatewayAPIGroup, Resource: "httproutes"},
			"", errors.New("denied")), "Grant the cluster-validator ServiceAccount"},
		"500": {apierrors.NewInternalError(errors.New("etcd timeout")), "Re-run once the apiserver is healthy"},
		"429": {apierrors.NewTooManyRequestsError("slow down"), "Re-run once the apiserver is healthy"},
	} {
		dyn := routeClient()
		dyn.PrependReactor("list", "*", func(ktesting.Action) (bool, runtime.Object, error) {
			return true, nil, tc.err
		})
		state := &ValidationState{Log: testLog()}
		checkExternalLoadBalancer(context.Background(), routeDiscoveryClient(), dyn, state)
		assert.Nil(t, state.ExternalLBOK, name)
		joined := strings.Join(state.Recommendations, "; ")
		assert.Contains(t, joined, tc.advice, name)
		if tc.advice != "Grant the cluster-validator ServiceAccount" {
			assert.NotContains(t, joined, "Grant", name)
		}
	}
}

// With configured Gateway names, a route list that fails is said once, so the
// unlisted-Gateway and missing-routes cross-checks are not skipped silently.
func TestResolveGatewayOwnership_ConfiguredCrossCheckFailureIsReported(t *testing.T) {
	t.Setenv(nvcfGatewayNamesEnv, "gw/shared-gw")
	dyn := routeClient()
	dyn.PrependReactor("list", "httproutes", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewInternalError(errors.New("etcd timeout"))
	})
	surface, surfaceErr := discoverGatewayAPIResources(context.Background(), routeDiscoveryClient())
	own := resolveGatewayOwnershipIn(context.Background(), nil, surface, surfaceErr, dyn)
	assert.Equal(t, gatewaySet{"gw/shared-gw": true}, own.gateways, "the configured list still applies")
	state := &ValidationState{Log: testLog(), PostInstall: true}
	own.reportInvalid(state.Log, state)
	own.reportInvalid(state.Log, state)
	require.Len(t, state.Warnings, 1, "reported once per run")
	assert.Contains(t, state.Warnings[0], "Gateway names: could not read the NVCF routes to confirm the "+
		gatewayNamesSetting+" list: listing httproutes: Internal error occurred: etcd timeout")
}

// The probe's events read retries a transient error until it clears, and one
// that does not clear comes back with its cause.
func TestProbeSandboxEvents_RetriesAndKeepsTheCause(t *testing.T) {
	client := fake.NewSimpleClientset()
	calls := 0
	client.PrependReactor("list", "events", func(ktesting.Action) (bool, runtime.Object, error) {
		calls++
		if calls <= 2 {
			return true, nil, apierrors.NewServiceUnavailable("apiserver busy")
		}
		return true, &corev1.EventList{Items: []corev1.Event{podEvent("p", "FailedCreatePodSandBox", 0)}}, nil
	})
	got, err := probeSandboxEvents(context.Background(), client, "ns")
	require.NoError(t, err)
	require.Contains(t, got, "p")
	assert.Equal(t, sandboxFailed, got["p"].state)
	assert.Equal(t, 3, calls)

	client = fake.NewSimpleClientset()
	failWith(client, "list", "events", apierrors.NewServiceUnavailable("apiserver busy"))
	_, err = probeSandboxEvents(context.Background(), client, "ns")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "apiserver busy")
}

// A checker pod that could not be created never tested the overlay, whatever
// the status code.
func TestCheckNodeToNode_CheckerCreateErrorIsUnknown(t *testing.T) {
	for name, createErr := range map[string]error{
		"403": apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "", errors.New("exceeded quota")),
		"400": apierrors.NewBadRequest("image is not allowed"),
		"500": apierrors.NewInternalError(errors.New("failed calling webhook")),
	} {
		t.Run(name, func(t *testing.T) {
			nodes := []*corev1.Node{makeNode("node-1", true, 0), makeNode("node-2", true, 0)}
			pods := []corev1.Pod{runningProbePod("a", "node-1", "10.0.0.1"), runningProbePod("b", "node-2", "10.0.0.2")}
			f := newN2NFixture(t, nodes, pods, nil, checkerExit(0))
			f.client.PrependReactor("create", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
				return true, nil, createErr
			})
			state := &ValidationState{Log: testLog()}
			checkNodeToNode(context.Background(), f.client, state, enforcementDefaultImg)
			assert.Nil(t, state.NodeToNodeOK)
			joined := strings.Join(state.Warnings, "; ")
			assert.Contains(t, joined, "Node-to-Node: status unknown (could not create the checker pod in ")
			assert.Contains(t, joined, createErr.Error())
		})
	}
}

// enforcementCluster scripts the enforcement test: the server pod is Ready,
// and each probe pod, by name, ends as outcome says ("reach", "block" or
// "refused"; reach by default) or fails to be created with createErr. Probes
// are numbered in the order they run, across phases.
func enforcementCluster(
	t *testing.T, outcome func(name string) string, createErr func(name string) error,
) *fake.Clientset {
	t.Helper()
	prev := enforcementPropDelay
	enforcementPropDelay = time.Millisecond
	t.Cleanup(func() { enforcementPropDelay = prev })
	client := fake.NewSimpleClientset()
	client.PrependReactor("create", "pods", func(a ktesting.Action) (bool, runtime.Object, error) {
		p := a.(ktesting.CreateAction).GetObject().(*corev1.Pod)
		if createErr != nil {
			if err := createErr(p.Name); err != nil {
				return true, nil, err
			}
		}
		return true, p, nil
	})
	client.PrependReactor("get", "pods", func(a ktesting.Action) (bool, runtime.Object, error) {
		name := a.(ktesting.GetAction).GetName()
		if name == enforcementServerPod {
			return true, &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.9",
				Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}, nil
		}
		exited := func(code int32) []corev1.ContainerStatus {
			return []corev1.ContainerStatus{{State: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{ExitCode: code}}}}
		}
		result := ""
		if outcome != nil {
			result = outcome(name)
		}
		switch result {
		case "block":
			return true, &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodFailed,
				ContainerStatuses: exited(1)}}, nil
		case "refused":
			return true, &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodFailed, Reason: "OutOfpods"}}, nil
		default:
			return true, &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodSucceeded,
				ContainerStatuses: exited(0)}}, nil
		}
	})
	return client
}

// Only probes that ran decide an enforcement phase. A probe that could not be
// created, or that the kubelet refused, is not "blocked", and a policy the
// apiserver never accepted is not a CNI that ignores it.
func TestCheckNetworkPolicyEnforcement_OnlyProbesThatRanDecide(t *testing.T) {
	cfg := &EnforcementConfig{Enabled: true, Critical: true, TimeoutSeconds: 5}
	enforcing := func(name string) string {
		return map[string]string{
			"probe-client-3": "block", "probe-allowed-4": "block", "probe-client-5": "block", "probe-client-8": "block",
		}[name]
	}
	baseline := func(name string) bool { return name == "probe-client-1" || name == "probe-allowed-2" }
	afterBaseline := func(name string) error {
		if baseline(name) || name == enforcementServerPod {
			return nil
		}
		return apierrors.NewInternalError(errors.New("etcd timeout"))
	}

	run := func(client *fake.Clientset) *ValidationState {
		state := &ValidationState{Log: testLog()}
		checkNetworkPolicyEnforcement(context.Background(), client, state, cfg)
		return state
	}

	state := run(enforcementCluster(t, enforcing, nil))
	require.NotNil(t, state.EnforcementOK, "an enforcing CNI is verified")
	assert.True(t, *state.EnforcementOK)

	state = run(enforcementCluster(t, nil, afterBaseline))
	assert.Nil(t, state.EnforcementOK, "probes that were never created sent no traffic")
	assert.Contains(t, strings.Join(state.Warnings, "; "),
		"status unknown (the deny-all client probe did not run: could not create the probe pod probe-client-3")

	state = run(enforcementCluster(t, func(name string) string {
		if baseline(name) {
			return "reach"
		}
		return "refused"
	}, nil))
	assert.Nil(t, state.EnforcementOK, "a probe the kubelet refused is not blocked traffic")
	assert.Contains(t, strings.Join(state.Warnings, "; "), "ended Failed without running (OutOfpods)")

	client := enforcementCluster(t, enforcing, nil)
	failWith(client, "create", "networkpolicies", apierrors.NewInternalError(errors.New("etcd timeout")))
	failWith(client, "update", "networkpolicies", apierrors.NewInternalError(errors.New("etcd timeout")))
	state = run(client)
	assert.Nil(t, state.EnforcementOK, "a policy the apiserver never accepted says nothing about the CNI")
	joined := strings.Join(state.Warnings, "; ")
	assert.Contains(t, joined, "could not create the deny-all ingress policy: Internal error occurred: etcd timeout")
	assert.NotContains(t, joined, "CNI does not enforce")

	// An observed failure decides the check even beside a phase that did not
	// run: traffic that got through deny-all is evidence on its own.
	state = run(enforcementCluster(t, nil, func(name string) error {
		if name == "probe-client-5" || name == "probe-allowed-6" {
			return apierrors.NewInternalError(errors.New("etcd timeout"))
		}
		return nil
	}))
	require.NotNil(t, state.EnforcementOK)
	assert.False(t, *state.EnforcementOK)

	// A transient error on a probe create is retried, not taken as the result.
	calls := 0
	state = run(enforcementCluster(t, enforcing, func(name string) error {
		if name == "probe-client-3" {
			calls++
			if calls == 1 {
				return apierrors.NewTooManyRequestsError("slow down")
			}
		}
		return nil
	}))
	require.NotNil(t, state.EnforcementOK)
	assert.True(t, *state.EnforcementOK)
}

// A TLS endpoint that completes TCP from its listen backlog but never answers
// the ClientHello is not reachable, and the probe ends with its context
// rather than hanging the run.
func TestTLSProbe_HandshakeWithNoAnswerIsBounded(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	port := ln.Addr().(*net.TCPAddr).Port

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	finishesWithin(t, 5*time.Second, func() {
		assert.False(t, TestEndpoint(ctx, Endpoint{Host: "127.0.0.1", Port: port, Protocol: protocolTCPTLS}))
	})
}

// The strings nvcf-cli grades a transcript on are part of the interface.
// Changing one must be a deliberate change on both sides.
func TestLauncherContractStrings(t *testing.T) {
	assert.Equal(t, "Validator role: ", RoleMarker)
	assert.Equal(t, "Check Results:", SummaryStart)
	assert.Equal(t, "Cluster is ", VerdictLinePrefix)
	assert.Equal(t, "NVCF-Ready", VerdictReady)
	assert.Equal(t, "NVCF-Ready (with warnings)", VerdictReadyWithWarnings)
	assert.Equal(t, "NVCF-Not-Ready", VerdictNotReady)
	assert.Equal(t, "\u2717", FailIcon)
	assert.Equal(t, FailIcon, iconCross, "failed rows are printed with the fail icon")
	assert.Equal(t, "GPU Resources", GPUResourcesLabel)
	assert.Equal(t, "rollout in progress", RolloutInProgressMarker)
	assert.Equal(t, "rolling update in progress", RollingUpdateMarker)
	assert.Equal(t, "mid-rollout", MidRolloutMarker)
}

// A StatefulSet rollout held at its partition is settled, so no rollout
// marker may appear: a launcher would wait for it forever.
func TestRun_PartitionHeldRolloutPrintsNoRolloutMarker(t *testing.T) {
	objs := makeQuorumSTS("nats", "nats-system", 3, 3, []string{"node-1", "node-2", "node-3"})
	sts := objs[0].(*appsv1.StatefulSet)
	partition := int32(2)
	sts.Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{
		Type:          appsv1.RollingUpdateStatefulSetStrategyType,
		RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{Partition: &partition},
	}
	sts.Status.UpdateRevision, sts.Status.UpdatedReplicas = "nats-r2", 1
	objs = append(objs, &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "standard",
		Annotations: map[string]string{"storageclass.kubernetes.io/is-default-class": "true"}}})

	_, out := runCaptured(t, fake.NewSimpleClientset(objs...), "", "", RoleControlPlane)
	assert.Contains(t, out, "rollout held at partition 2", "the fixture must reach the partition rule")
	for _, marker := range []string{RolloutInProgressMarker, RollingUpdateMarker, MidRolloutMarker} {
		assert.NotContains(t, out, marker)
	}
}

// Each required pair is required on its own: dropping any one fails the
// critical row. Optional route types are the non-critical row's to judge.
func TestCheckGatewayAPICRDs_EachRequiredPairIsRequired(t *testing.T) {
	optional := map[string]bool{}
	for _, r := range gatewayOptionalRouteRequirements {
		optional[r.groupVersion+"/"+r.resource] = true
	}
	all := gatewayRequiredPairs()
	for i, dropped := range all {
		if optional[dropped] {
			continue
		}
		pairs := append(append([]string{}, all[:i]...), all[i+1:]...)
		state := &ValidationState{Log: testLog()}
		checkGatewayAPICRDs(context.Background(), gatewayDiscoveryClient(pairs...), state)
		require.NotNil(t, state.GatewayAPICRDsOK, dropped)
		assert.False(t, *state.GatewayAPICRDsOK, dropped)
	}
}

// Kubernetes 1.26.0 is the first release that binds PVCs with the newest of
// several defaults.
func TestCheckStorageClass_MultipleDefaultsAtTheVersionBoundary(t *testing.T) {
	mk := func(name string) *storagev1.StorageClass {
		return &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: name,
			Annotations: map[string]string{"storageclass.kubernetes.io/is-default-class": "true"}}}
	}
	for version, want := range map[string]bool{"v1.26.0": true, "v1.25.99": false} {
		state := &ValidationState{Log: testLog(), K8sVersion: version}
		checkStorageClass(context.Background(), fake.NewSimpleClientset(mk("a"), mk("b")), state)
		require.NotNil(t, state.DefaultStorageClassOK, version)
		assert.Equal(t, want, *state.DefaultStorageClassOK, version)
	}
}

// With a relocated controller, the Envoy row looks for its pods there.
func TestCheckEnvoyGateway_UsesTheConfiguredNamespace(t *testing.T) {
	t.Setenv(envoyGatewayNamespaceEnv, "gateway")
	pod := makeEnvoyControllerPod("envoy-gateway-0", true)
	pod.Namespace = "gateway"
	client := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "gateway"}}, pod)
	state := &ValidationState{Log: testLog()}
	checkEnvoyGateway(context.Background(), client, state)
	require.NotNil(t, state.EnvoyGatewayOK)
	assert.True(t, *state.EnvoyGatewayOK)
}

// NVCF Gateways exposed only through NodePort have no LB to verify: the row has
// no result, and the warning says the cluster's shape decided that, not a
// failed read.
func TestCheckExternalLoadBalancer_NodePortOnlyIsNotAssessed(t *testing.T) {
	envoyNS := envoyGatewayNamespaceName()
	t.Setenv(nvcfGatewayNamesEnv, "nvcf/a-gw")
	client := routeDiscoveryClient()
	addServices(t, client, gatewayLBService(envoyNS, "envoy-a", "nvcf", "a-gw", corev1.ServiceTypeNodePort, ""))
	state := &ValidationState{Log: testLog()}
	checkExternalLoadBalancer(context.Background(), client, routeClient(), state)
	assert.Nil(t, state.ExternalLBOK)
	assert.Equal(t, []string{"External Load Balancer: not assessed (no NVCF Gateway is exposed through a " +
		"LoadBalancer Service)"}, state.Warnings)
}

// GRPCRoute and UDPRoute parents are NVCF Gateways too.
func TestDiscoverNVCFGateways_GRPCAndUDPRoutes(t *testing.T) {
	dyn := routeClient(
		route("GRPCRoute", "nvcf", "grpc", "nvcf-gateway-routes-1.18.2", parentRef("name", "grpc-gw")),
		route("UDPRoute", "nvcf", "llm", "nvcf-gateway-routes-1.18.2", parentRef("name", "udp-gw", "namespace", "gw")),
	)
	client := gatewayDiscoveryClient(gatewayAPIGroup+"/v1/grpcroutes", gatewayAPIGroup+"/v1alpha2/udproutes")
	got, err := discoverNVCFGateways(context.Background(), client, dyn)
	require.NoError(t, err)
	assert.Equal(t, gatewaySet{"nvcf/grpc-gw": true, "gw/udp-gw": true}, got)
}

// A merged-gateways proxy serves the NVCF Gateways of its class. Another
// team's per-Gateway proxy of the same class does not, so its pending address
// must not fail the NVCF row.
func TestJudgeNVCFGatewayServices_ForeignPerGatewayServiceBesideAMergedProxy(t *testing.T) {
	envoyNS := envoyGatewayNamespaceName()
	merged := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "envoy-eg", Namespace: envoyNS,
			Labels: map[string]string{owningGatewayClassLabel: "eg"}},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
	}
	merged.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: "203.0.113.1"}}
	foreign := gatewayLBService(envoyNS, "envoy-team-b", "team-b", "b-gw", corev1.ServiceTypeLoadBalancer, "")
	foreign.Labels[owningGatewayClassLabel] = "eg"

	t.Setenv(envoyGatewayNamespaceEnv, "")
	t.Setenv(nvcfGatewayNamesEnv, "nvcf/a-gw")
	client := gatewayDiscoveryClient(gatewayAPIGroup+"/v1/gateways", gatewayAPIGroup+"/v1/gatewayclasses")
	addServices(t, client, merged, foreign)
	routes := envoyGatewayClient(t, gatewayObject("nvcf", "a-gw", "eg"))
	state := &ValidationState{Log: testLog()}
	checkExternalLoadBalancer(context.Background(), client, routes, state)
	require.NotNil(t, state.ExternalLBOK)
	assert.True(t, *state.ExternalLBOK)
	assert.NotContains(t, strings.Join(state.Warnings, "; "), "envoy-team-b")
}
