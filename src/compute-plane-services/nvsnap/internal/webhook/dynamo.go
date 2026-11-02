// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package webhook

// Dynamo profile. A Dynamo component names its Dynamo namespace in its
// environment, and the operator derives it from the Kubernetes namespace,
// which is new for every instance. A restored component would then keep
// the name of the instance it was captured in, and the new instance's
// frontend would not find it. The webhook gives every component of an
// instance one fixed Dynamo namespace instead, so a restored component and
// a fresh one agree. The Dynamo namespace is only a name: discovery and
// request routing stay inside the instance as long as its etcd and NATS
// are the instance's own, so the profile applies only then.

import (
	"context"
	"net"
	"net/url"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

const (
	dynNamespaceEnv       = "DYN_NAMESPACE"
	dynNamespacePrefixEnv = "DYN_NAMESPACE_PREFIX"
	// dynLeaseTTLEnv is the TTL, in seconds, of the etcd lease a Dynamo
	// component registers under (Dynamo's default is 10).
	dynLeaseTTLEnv  = "ETCD_LEASE_TTL"
	dynDiscoveryEnv = "DYN_DISCOVERY_BACKEND"
	// DynDiscoveryAnnotation is the operator's per-pod discovery backend.
	DynDiscoveryAnnotation = "nvidia.com/dynamo-discovery-backend"
)

// dynamoEndpointEnv are the endpoints a component reaches its instance's
// discovery and request plane through.
var dynamoEndpointEnv = []string{"ETCD_ENDPOINTS", "NATS_SERVER"}

// dynamoComponent reports a pod with a container that names a Dynamo
// namespace.
func dynamoComponent(pod *corev1.Pod) bool {
	for _, c := range pod.Spec.Containers {
		if literalEnv(&c, dynNamespaceEnv) != "" {
			return true
		}
	}
	return false
}

// instanceLocalEndpoints reports whether every Dynamo endpoint the pod
// names is a service of its own namespace: a host without a dot resolves
// in the pod's namespace. An endpoint that is unset, set by reference or
// qualified with another namespace may be shared with other instances.
func instanceLocalEndpoints(pod *corev1.Pod) bool {
	for _, c := range pod.Spec.Containers {
		if literalEnv(&c, dynNamespaceEnv) == "" {
			continue
		}
		for _, name := range dynamoEndpointEnv {
			v, set := envValue(&c, name)
			if !set || v == "" {
				return false
			}
			for _, ep := range strings.Split(v, ",") {
				if !localHost(strings.TrimSpace(ep)) {
					return false
				}
			}
		}
	}
	return true
}

// localHost reports whether an endpoint names a service of the pod's own
// namespace: a host name without a dot. An IP address names no namespace,
// whatever its form ("[2001:db8::1]:2379" has no dot either).
func localHost(endpoint string) bool {
	host := endpoint
	if strings.Contains(endpoint, "://") {
		u, err := url.Parse(endpoint)
		if err != nil {
			return false
		}
		host = u.Host
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "" || net.ParseIP(host) != nil {
		return false
	}
	return !strings.Contains(host, ".")
}

// dynamoNamespacePatches sets the fixed Dynamo namespace on every
// container that names one, and the namespace prefix the frontend filters
// models by to match. Only for a pod a CRIU capture may take (the same
// candidates whose liveness probes are widened): the name exists for a
// restored component, and two graphs in one namespace that each had their
// own Dynamo namespace would otherwise register under one name and route
// to each other's workers. A cluster that does not capture with CRIU
// (CaptureCandidate nil) never sees the rewrite.
func (m *Mutator) dynamoNamespacePatches(ctx context.Context, pod *corev1.Pod) []PatchOp {
	if m.DynamoNamespace == "" || m.CaptureCandidate == nil || !dynamoComponent(pod) || !instanceLocalEndpoints(pod) {
		return nil
	}
	if gpuRequest(pod) == 0 && !m.CaptureCandidate(ctx, pod) {
		return nil
	}
	var patches []PatchOp
	for i := range pod.Spec.Containers {
		c := &pod.Spec.Containers[i]
		var vars []corev1.EnvVar
		for _, name := range []string{dynNamespaceEnv, dynNamespacePrefixEnv} {
			if v, set := envValue(c, name); set && v != m.DynamoNamespace {
				vars = append(vars, corev1.EnvVar{Name: name, Value: m.DynamoNamespace})
			}
		}
		// A lease that outlasts a capture: capturing a running instance
		// freezes its GPU workers for seconds (longer for a large model),
		// and an etcd that sees a 10 s lease go unrenewed deregisters the
		// worker of the instance that keeps serving. A longer TTL costs
		// nothing but a slower drop of a crashed component from routing.
		// A value the workload sets itself is kept.
		if _, names := envValue(c, dynNamespaceEnv); names && m.DynamoLeaseTTL > 0 {
			if _, set := envValue(c, dynLeaseTTLEnv); !set {
				vars = append(vars, corev1.EnvVar{Name: dynLeaseTTLEnv, Value: strconv.Itoa(m.DynamoLeaseTTL)})
			}
		}
		patches = append(patches, setContainerEnv(i, c, vars)...)
	}
	if len(patches) > 0 {
		m.logger().WithField("pod", pod.Namespace+"/"+pod.Name).WithField("dynamoNamespace", m.DynamoNamespace).
			Info("dynamo: fixed Dynamo namespace set, so a restored component and a fresh one agree")
	}
	return patches
}

// dynamoRestorable reports a Dynamo component whose identity a group
// restore keeps consistent: it registers in its instance's own etcd under
// the fixed Dynamo namespace, and that etcd is captured and restored with
// it as a companion. A component that registers as Kubernetes objects in
// its namespace cannot follow a restore into another namespace.
func (m *Mutator) dynamoRestorable(pod *corev1.Pod) bool {
	return DynamoRestorable(pod, m.DynamoNamespace)
}

// DynamoRestorable is dynamoRestorable for the fixed Dynamo namespace
// fixed.
func DynamoRestorable(pod *corev1.Pod, fixed string) bool {
	return fixed != "" && dynamoComponent(pod) && DynamoDiscovery(pod) == "etcd" && instanceLocalEndpoints(pod)
}

// DynamoComponent reports a pod with a container that names a Dynamo
// namespace.
func DynamoComponent(pod *corev1.Pod) bool { return dynamoComponent(pod) }

// DynamoNamespaceIs reports whether every container of the pod that names
// a Dynamo namespace names fixed: the running pod got the profile.
func DynamoNamespaceIs(pod *corev1.Pod, fixed string) bool {
	for _, c := range pod.Spec.Containers {
		if v, set := envValue(&c, dynNamespaceEnv); set && v != fixed {
			return false
		}
	}
	return true
}

// DynamoDiscovery returns the discovery backend a Dynamo component
// registers with: its env, else the operator's annotation.
func DynamoDiscovery(pod *corev1.Pod) string {
	for _, c := range pod.Spec.Containers {
		if v := literalEnv(&c, dynDiscoveryEnv); v != "" {
			return v
		}
	}
	return pod.Annotations[DynDiscoveryAnnotation]
}

// literalEnv returns a container's literal value of name, or "".
func literalEnv(c *corev1.Container, name string) string {
	v, _ := envValue(c, name)
	return v
}

// envValue returns a container's literal value of name; set is false when
// the variable is absent or comes from a reference.
func envValue(c *corev1.Container, name string) (value string, set bool) {
	for _, e := range c.Env {
		if e.Name == name {
			if e.ValueFrom != nil {
				return "", false
			}
			return e.Value, true
		}
	}
	return "", false
}
