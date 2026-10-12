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

// Package config holds the operator's command-line configuration and turns it
// into controller-runtime manager options.
package config

import (
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

const (
	// LeaderElectionID names the leader election lease.
	LeaderElectionID = "pylon-operator.pylon.nvidia.com"

	// DefaultClusterCredentialSecret is the Secret holding the T0 cluster
	// credential.
	DefaultClusterCredentialSecret = "pylon-operator-cluster-credential"
	// DefaultProbeInterval is the health probe period.
	DefaultProbeInterval = 10 * time.Second
	// DefaultScrapeInterval is the transport metrics scrape period.
	DefaultScrapeInterval = 5 * time.Second
	// DefaultHealthProbeBindAddress serves /healthz and /readyz.
	DefaultHealthProbeBindAddress = ":8081"
	// DefaultInitialInputTPS is Pylon's --initial-input-tps, the input
	// tokens per second Pylon assumes for its queue estimate until it has
	// measured the backend. 100 is the value NVCF passes to every Pylon.
	DefaultInitialInputTPS = 100.0

	// ManagedByLabel and ManagedBy mark every object the operator creates:
	// transport Deployments and the replicated credential Secrets and trust
	// bundle ConfigMaps. The Deployment cache is restricted to them.
	ManagedByLabel = "app.kubernetes.io/managed-by"
	// ManagedBy is the ManagedByLabel value.
	ManagedBy = "pylon-operator"

	// PodNamespaceEnv is the environment variable, set from the downward
	// API, that defaults --operator-namespace.
	PodNamespaceEnv = "POD_NAMESPACE"
)

// serviceAccountNamespaceFile is the fallback for --operator-namespace when
// POD_NAMESPACE is unset. A variable so tests can point it elsewhere.
var serviceAccountNamespaceFile = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"

// Config is the parsed command line.
type Config struct {
	// ClusterID is the installation's cluster name, a DNS label.
	ClusterID string
	// RouterGRPCAddress is the router's registration address given to Pylon.
	RouterGRPCAddress string
	// PylonImage is the image of the transport pods.
	PylonImage string
	// PylonImagePullPolicy is the transport container's imagePullPolicy.
	// Empty leaves the Kubernetes default, which is Always for a :latest or
	// untagged image and IfNotPresent otherwise.
	PylonImagePullPolicy string
	// PylonImagePullSecrets names the image pull Secrets of the transport
	// pods. The pods reference them in their own namespace, so each Secret
	// must exist in every namespace with an InferenceEndpoint; the operator
	// does not replicate them. Empty references none.
	PylonImagePullSecrets []string
	// WatchNamespaces restricts the namespaced caches. Empty means all.
	WatchNamespaces []string
	// TransportReplicas is the replica count of each transport Deployment.
	TransportReplicas int32
	// ProbeInterval is the period of the backend health probe.
	ProbeInterval time.Duration
	// ScrapeInterval is the period of the transport metrics scrape.
	ScrapeInterval time.Duration
	// MetricsBindAddress is where the manager serves metrics; "0" disables.
	MetricsBindAddress string
	// HealthProbeBindAddress is where the manager serves health probes.
	HealthProbeBindAddress string
	// LeaderElect enables leader election.
	LeaderElect bool
	// DevInsecureTransport disables TLS verification on the tunnel, for k3d.
	DevInsecureTransport bool
	// ClusterCredentialSecret is the Secret holding the cluster credential.
	// The operator reads it in OperatorNamespace and replicates its
	// cluster-token key into every namespace with an InferenceEndpoint.
	ClusterCredentialSecret string
	// OperatorNamespace is the namespace the operator runs in, where the
	// cluster credential Secret and the trust bundle ConfigMap live.
	OperatorNamespace string
	// TrustBundleConfigMap is the ConfigMap in OperatorNamespace whose ca.crt
	// key holds the router's CA bundle. Empty mounts no trust bundle.
	TrustBundleConfigMap string
	// InitialInputTPS is passed to every transport pod as
	// --initial-input-tps.
	InitialInputTPS float64
}

// BindFlags registers every flag on fs with its default.
func (c *Config) BindFlags(fs *flag.FlagSet) {
	fs.StringVar(&c.ClusterID, "cluster-id", "", "Name of this installation's cluster, a DNS label. Required.")
	fs.StringVar(&c.RouterGRPCAddress, "router-grpc-address", "", "gRPC address of the LLM request router that transport pods register with. Required.")
	fs.StringVar(&c.PylonImage, "pylon-image", "", "Container image of the Pylon transport pods. Required.")
	fs.StringVar(&c.PylonImagePullPolicy, "pylon-image-pull-policy", "", "imagePullPolicy of the Pylon transport container: Always, IfNotPresent or Never. Empty uses the Kubernetes default.")
	fs.Var((*nameList)(&c.PylonImagePullSecrets), "pylon-image-pull-secrets", "Comma-separated image pull Secrets of the Pylon transport pods. Each must exist in every namespace with an InferenceEndpoint. Empty uses none.")
	fs.Var((*namespaceList)(&c.WatchNamespaces), "watch-namespaces", "Comma-separated namespaces to watch. Empty watches all namespaces.")
	c.TransportReplicas = 1
	fs.Var((*int32Value)(&c.TransportReplicas), "transport-replicas", "Replicas of each transport Deployment.")
	fs.DurationVar(&c.ProbeInterval, "probe-interval", DefaultProbeInterval, "Period of the backend health probe.")
	fs.DurationVar(&c.ScrapeInterval, "scrape-interval", DefaultScrapeInterval, "Period of the transport pod metrics scrape.")
	fs.StringVar(&c.MetricsBindAddress, "metrics-bind-address", metricsserver.DefaultBindAddress, "Address the metrics endpoint binds to. Use 0 to disable it.")
	fs.StringVar(&c.HealthProbeBindAddress, "health-probe-bind-address", DefaultHealthProbeBindAddress, "Address the health probe endpoint binds to.")
	fs.BoolVar(&c.LeaderElect, "leader-elect", false, "Enable leader election so only one replica reconciles.")
	fs.BoolVar(&c.DevInsecureTransport, "dev-insecure-transport", false, "Run transport pods with --quic-insecure. For local k3d clusters only.")
	fs.StringVar(&c.ClusterCredentialSecret, "cluster-credential-secret", DefaultClusterCredentialSecret, "Secret in the operator namespace that holds the cluster credential for transport pods, in key cluster-token.")
	fs.StringVar(&c.OperatorNamespace, "operator-namespace", defaultOperatorNamespace(), "Namespace the operator runs in. Defaults to $POD_NAMESPACE, then to the service account namespace. Required.")
	fs.StringVar(&c.TrustBundleConfigMap, "trust-bundle-configmap", "", "ConfigMap in the operator namespace whose ca.crt key is the router CA bundle. Empty mounts none.")
	fs.Float64Var(&c.InitialInputTPS, "initial-input-tps", DefaultInitialInputTPS, "Initial input tokens per second passed to transport pods as --initial-input-tps.")
}

// defaultOperatorNamespace returns $POD_NAMESPACE, or the namespace of the
// mounted service account token, or "".
func defaultOperatorNamespace() string {
	if ns := strings.TrimSpace(os.Getenv(PodNamespaceEnv)); ns != "" {
		return ns
	}
	data, err := os.ReadFile(serviceAccountNamespaceFile)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// Validate reports every invalid field at once.
func (c *Config) Validate() error {
	var errs []error
	if c.ClusterID == "" {
		errs = append(errs, errors.New("--cluster-id is required"))
	} else if msgs := validation.IsDNS1123Label(c.ClusterID); len(msgs) > 0 {
		errs = append(errs, fmt.Errorf("--cluster-id %q is not a DNS label: %s", c.ClusterID, strings.Join(msgs, "; ")))
	}
	if c.RouterGRPCAddress == "" {
		errs = append(errs, errors.New("--router-grpc-address is required"))
	}
	if c.PylonImage == "" {
		errs = append(errs, errors.New("--pylon-image is required"))
	}
	switch corev1.PullPolicy(c.PylonImagePullPolicy) {
	case "", corev1.PullAlways, corev1.PullIfNotPresent, corev1.PullNever:
	default:
		errs = append(errs, fmt.Errorf("--pylon-image-pull-policy must be Always, IfNotPresent or Never, got %q", c.PylonImagePullPolicy))
	}
	for _, name := range c.PylonImagePullSecrets {
		if msgs := validation.IsDNS1123Subdomain(name); len(msgs) > 0 {
			errs = append(errs, fmt.Errorf("--pylon-image-pull-secrets entry %q is not a Secret name: %s", name, strings.Join(msgs, "; ")))
		}
	}
	if c.OperatorNamespace == "" {
		errs = append(errs, fmt.Errorf("--operator-namespace is required; set it or %s", PodNamespaceEnv))
	} else if msgs := validation.IsDNS1123Label(c.OperatorNamespace); len(msgs) > 0 {
		errs = append(errs, fmt.Errorf("--operator-namespace %q is not a namespace name: %s", c.OperatorNamespace, strings.Join(msgs, "; ")))
	}
	for _, ns := range c.WatchNamespaces {
		if msgs := validation.IsDNS1123Label(ns); len(msgs) > 0 {
			errs = append(errs, fmt.Errorf("--watch-namespaces entry %q is not a namespace name: %s", ns, strings.Join(msgs, "; ")))
		}
	}
	if c.TransportReplicas < 1 {
		errs = append(errs, fmt.Errorf("--transport-replicas must be at least 1, got %d", c.TransportReplicas))
	}
	if c.ProbeInterval <= 0 {
		errs = append(errs, fmt.Errorf("--probe-interval must be positive, got %s", c.ProbeInterval))
	}
	if c.ScrapeInterval <= 0 {
		errs = append(errs, fmt.Errorf("--scrape-interval must be positive, got %s", c.ScrapeInterval))
	}
	if msgs := validation.IsDNS1123Subdomain(c.ClusterCredentialSecret); len(msgs) > 0 {
		errs = append(errs, fmt.Errorf("--cluster-credential-secret %q is not a Secret name: %s", c.ClusterCredentialSecret, strings.Join(msgs, "; ")))
	}
	if c.TrustBundleConfigMap != "" {
		if msgs := validation.IsDNS1123Subdomain(c.TrustBundleConfigMap); len(msgs) > 0 {
			errs = append(errs, fmt.Errorf("--trust-bundle-configmap %q is not a ConfigMap name: %s", c.TrustBundleConfigMap, strings.Join(msgs, "; ")))
		}
	}
	if !(c.InitialInputTPS > 0) || math.IsInf(c.InitialInputTPS, 0) {
		errs = append(errs, fmt.Errorf("--initial-input-tps must be a positive number, got %v", c.InitialInputTPS))
	}
	return errors.Join(errs...)
}

// ManagerOptions builds the controller-runtime manager options. When
// WatchNamespaces is set, caches of namespaced objects are restricted to those
// namespaces; cluster-scoped objects such as Nodes are unaffected.
//
// The caches of the objects the transport step and the registration
// observer read are narrowed further: Deployments and Pods to those labelled
// ManagedByLabel=ManagedBy, which are the transport Deployments and their
// pods, and Secrets and ConfigMaps to the configured credential and trust
// bundle names in the watched namespaces plus OperatorNamespace, where the
// sources live. No other Pod, Secret or ConfigMap is ever cached.
func (c *Config) ManagerOptions(scheme *runtime.Scheme) ctrl.Options {
	opts := ctrl.Options{
		Scheme:                        scheme,
		Metrics:                       metricsserver.Options{BindAddress: c.MetricsBindAddress},
		HealthProbeBindAddress:        c.HealthProbeBindAddress,
		LeaderElection:                c.LeaderElect,
		LeaderElectionID:              LeaderElectionID,
		LeaderElectionReleaseOnCancel: true,
	}
	if len(c.WatchNamespaces) > 0 {
		namespaces := make(map[string]cache.Config, len(c.WatchNamespaces))
		for _, ns := range c.WatchNamespaces {
			namespaces[ns] = cache.Config{}
		}
		opts.Cache = cache.Options{DefaultNamespaces: namespaces}
	}
	managed := labels.SelectorFromSet(labels.Set{ManagedByLabel: ManagedBy})
	opts.Cache.ByObject = map[client.Object]cache.ByObject{
		&appsv1.Deployment{}: {Label: managed},
		&corev1.Pod{}:        {Label: managed},
		&corev1.Secret{}:     c.sourceAndReplicas(c.ClusterCredentialSecret),
	}
	if c.TrustBundleConfigMap != "" {
		opts.Cache.ByObject[&corev1.ConfigMap{}] = c.sourceAndReplicas(c.TrustBundleConfigMap)
	}
	return opts
}

// sourceAndReplicas caches the objects called name in the watched namespaces
// and in OperatorNamespace. A nil Namespaces map means every namespace.
func (c *Config) sourceAndReplicas(name string) cache.ByObject {
	by := cache.ByObject{Field: fields.OneTermEqualSelector("metadata.name", name)}
	if len(c.WatchNamespaces) > 0 {
		by.Namespaces = make(map[string]cache.Config, len(c.WatchNamespaces)+1)
		for _, ns := range append([]string{c.OperatorNamespace}, c.WatchNamespaces...) {
			if ns != "" {
				by.Namespaces[ns] = cache.Config{}
			}
		}
	}
	return by
}

// namespaceList is a flag.Value for a comma-separated, de-duplicated and
// sorted list of namespaces.
type namespaceList []string

func (l *namespaceList) String() string {
	if l == nil {
		return ""
	}
	return strings.Join(*l, ",")
}

func (l *namespaceList) Set(value string) error {
	seen := map[string]struct{}{}
	out := []string{}
	for _, ns := range strings.Split(value, ",") {
		ns = strings.TrimSpace(ns)
		if ns == "" {
			continue
		}
		if _, ok := seen[ns]; ok {
			continue
		}
		seen[ns] = struct{}{}
		out = append(out, ns)
	}
	sort.Strings(out)
	*l = out
	return nil
}

// nameList is a flag.Value for a comma-separated, de-duplicated list of
// object names. The order of first appearance is kept.
type nameList []string

func (l *nameList) String() string {
	if l == nil {
		return ""
	}
	return strings.Join(*l, ",")
}

func (l *nameList) Set(value string) error {
	seen := map[string]struct{}{}
	out := []string{}
	for _, name := range strings.Split(value, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	*l = out
	return nil
}

// int32Value is a flag.Value for an int32.
type int32Value int32

func (v *int32Value) String() string {
	if v == nil {
		return "0"
	}
	return strconv.FormatInt(int64(*v), 10)
}

func (v *int32Value) Set(value string) error {
	n, err := strconv.ParseInt(value, 10, 32)
	if err != nil {
		return err
	}
	*v = int32Value(n)
	return nil
}
