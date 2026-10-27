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

// Package config holds the gds-mcs-controller command line and turns it into
// controller-runtime manager options.
package config

import (
	"errors"
	"flag"
	"fmt"
	"maps"
	"net/url"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	mcsv1beta1 "sigs.k8s.io/mcs-api/pkg/apis/v1beta1"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api"
)

// Defaults of the flags.
const (
	DefaultLeaseTTL                = 30 * time.Second
	MinLeaseTTL                    = 10 * time.Second
	MaxLeaseTTL                    = 5 * time.Minute
	DefaultEndpointsPerSlice       = 100
	MaxEndpointsPerSlice           = 1000
	DefaultMaxConcurrentReconciles = 4
	DefaultHealthProbeBindAddress  = ":8081"
)

// Environment variables read by BindFlags.
const (
	// GDSURLsEnv is a comma-separated list of GDS URLs used when --gds-url
	// is not given.
	GDSURLsEnv = "GDS_URLS"
	// PodNamespaceEnv, set from the downward API, defaults
	// --operator-namespace.
	PodNamespaceEnv = "POD_NAMESPACE"
)

// Values of --cluster-identity-source.
const (
	// IdentitySourceAuto takes each id from its flag when set, else from
	// the ClusterProperty object.
	IdentitySourceAuto = "auto"
	// IdentitySourceFlags requires both id flags and never reads
	// ClusterProperty objects.
	IdentitySourceFlags = "flags"
	// IdentitySourceClusterProperty requires the ClusterProperty objects;
	// id flags, when set, must agree with them.
	IdentitySourceClusterProperty = "clusterproperty"
)

// ClusterPropertyGVK is the KEP-2149 ClusterProperty kind, used as
// unstructured: the controller registers no Go type for it.
var ClusterPropertyGVK = schema.GroupVersionKind{Group: "about.k8s.io", Version: "v1alpha1", Kind: "ClusterProperty"}

// Config is the parsed command line.
type Config struct {
	// GDSURLs is the ordered list of GDS endpoints.
	GDSURLs []string
	// GDSTLSCAFile is a PEM bundle of the CAs that sign the GDS
	// certificates. Empty uses the system roots.
	GDSTLSCAFile string
	// GDSAllowInsecureHTTP permits http:// URLs.
	GDSAllowInsecureHTTP bool

	// ClusterSetID and ClusterID are the KEP-1645 identifiers; see
	// IdentitySource.
	ClusterSetID string
	ClusterID    string
	// IdentitySource is one of the IdentitySource* values.
	IdentitySource string
	// Region of the cluster. Empty derives it from the
	// topology.kubernetes.io/region node label.
	Region string
	// ClusterLabels are the labels of the cluster registration.
	ClusterLabels map[string]string
	// LeaseTTL is the cluster lease requested from GDS; heartbeats run every
	// LeaseTTL/3.
	LeaseTTL time.Duration

	// WatchNamespaces restricts the namespaced caches, the exports and the
	// imports. Empty means all namespaces.
	WatchNamespaces []string
	// CreateNamespaces creates the namespace of an import that has none,
	// instead of holding the import pending.
	CreateNamespaces bool
	// AdoptUnmanagedImports takes over ServiceImports the controller did not
	// create.
	AdoptUnmanagedImports bool
	// AutoExportPools creates ServiceExports for the services[] of
	// API-registered pools of this cluster.
	AutoExportPools bool
	// WriteClusterProperties publishes the resolved identity as
	// ClusterProperty objects.
	WriteClusterProperties bool
	// DeregisterOnShutdown deregisters the cluster, which deletes its exports
	// in GDS, when the controller stops.
	DeregisterOnShutdown bool
	// EndpointsPerSlice is the maximum number of endpoints per imported
	// EndpointSlice.
	EndpointsPerSlice int
	// MaxConcurrentReconciles bounds each controller's workers.
	MaxConcurrentReconciles int

	// MetricsBindAddress serves /metrics; "0" disables it.
	MetricsBindAddress string
	// HealthProbeBindAddress serves /healthz and /readyz.
	HealthProbeBindAddress string
	// LeaderElect enables leader election; only the leader talks to GDS as a
	// writer and applies imports.
	LeaderElect bool
	// OperatorNamespace holds the leader election Lease.
	OperatorNamespace string
	// OTelEnabled turns on OpenTelemetry tracing, configured through the
	// standard OTEL_* environment variables.
	OTelEnabled bool
}

// BindFlags registers every flag on fs with its default. GDS_URLS and
// POD_NAMESPACE provide the defaults of --gds-url and --operator-namespace.
func (c *Config) BindFlags(fs *flag.FlagSet) {
	c.bindFlags(fs, os.LookupEnv)
}

func (c *Config) bindFlags(fs *flag.FlagSet, lookupEnv func(string) (string, bool)) {
	urls := &urlList{target: &c.GDSURLs}
	if v, ok := lookupEnv(GDSURLsEnv); ok {
		urls.setDefault(v)
	}
	fs.Var(urls, "gds-url", "GDS URL, repeatable or comma-separated, in failover order. Defaults to $"+GDSURLsEnv+". Required.")
	fs.StringVar(&c.GDSTLSCAFile, "gds-tls-ca-file", "", "PEM bundle of the CAs that sign the GDS certificates. Empty uses the system roots.")
	fs.BoolVar(&c.GDSAllowInsecureHTTP, "gds-allow-insecure-http", false, "Permit plaintext http:// GDS URLs.")
	fs.StringVar(&c.ClusterSetID, "clusterset-id", "", "Clusterset name, a DNS label. Falls back to the ClusterProperty "+api.ClusterPropertyClusterSet+".")
	fs.StringVar(&c.ClusterID, "cluster-id", "", "Cluster id, a DNS label unique in the clusterset. Falls back to the ClusterProperty "+api.ClusterPropertyClusterID+".")
	c.IdentitySource = IdentitySourceAuto
	fs.StringVar(&c.IdentitySource, "cluster-identity-source", IdentitySourceAuto, "Where the clusterset and cluster ids come from: auto (flags win, else ClusterProperty), flags or clusterproperty.")
	fs.StringVar(&c.Region, "region", "", "Region of this cluster. Empty uses the topology.kubernetes.io/region node label.")
	fs.Var((*labelMap)(&c.ClusterLabels), "cluster-labels", "Comma-separated key=value labels of the cluster registration.")
	fs.DurationVar(&c.LeaseTTL, "lease-ttl", DefaultLeaseTTL, "Cluster lease TTL requested from GDS, between 10s and 5m. Heartbeats run every TTL/3.")
	fs.Var((*namespaceList)(&c.WatchNamespaces), "watch-namespaces", "Comma-separated namespaces to export from and import into. Empty means all namespaces.")
	fs.BoolVar(&c.CreateNamespaces, "create-namespaces", false, "Create a missing namespace for an import instead of holding the import pending.")
	fs.BoolVar(&c.AdoptUnmanagedImports, "adopt-unmanaged-imports", false, "Take over ServiceImports this controller did not create.")
	fs.BoolVar(&c.AutoExportPools, "auto-export-pools", true, "Create ServiceExports for the services of API-registered pools of this cluster.")
	fs.BoolVar(&c.WriteClusterProperties, "write-cluster-properties", false, "Publish the clusterset and cluster ids as ClusterProperty objects.")
	fs.BoolVar(&c.DeregisterOnShutdown, "deregister-on-shutdown", false, "Deregister the cluster from GDS, deleting its exports, when the controller stops.")
	fs.IntVar(&c.EndpointsPerSlice, "endpoints-per-slice", DefaultEndpointsPerSlice, "Maximum endpoints per imported EndpointSlice, at most 1000.")
	fs.IntVar(&c.MaxConcurrentReconciles, "max-concurrent-reconciles", DefaultMaxConcurrentReconciles, "Workers of each controller.")
	fs.StringVar(&c.MetricsBindAddress, "metrics-bind-address", metricsserver.DefaultBindAddress, "Address the metrics endpoint binds to. Use 0 to disable it.")
	fs.StringVar(&c.HealthProbeBindAddress, "health-probe-bind-address", DefaultHealthProbeBindAddress, "Address the /healthz and /readyz endpoints bind to.")
	fs.BoolVar(&c.LeaderElect, "leader-elect", true, "Enable leader election so that one replica writes to GDS and applies imports.")
	ns, _ := lookupEnv(PodNamespaceEnv)
	fs.StringVar(&c.OperatorNamespace, "operator-namespace", strings.TrimSpace(ns), "Namespace of the leader election Lease. Defaults to $"+PodNamespaceEnv+"; required with --leader-elect.")
	fs.BoolVar(&c.OTelEnabled, "otel-enabled", false, "Enable OpenTelemetry tracing, configured by the OTEL_* environment variables.")
}

// Validate reports every invalid field at once.
func (c *Config) Validate() error {
	var errs []error
	if len(c.GDSURLs) == 0 {
		errs = append(errs, fmt.Errorf("--gds-url (or $%s) is required", GDSURLsEnv))
	}
	scheme := ""
	for _, raw := range c.GDSURLs {
		u, err := url.Parse(raw)
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("--gds-url %q: %w", raw, err))
			continue
		case u.Scheme != "https" && u.Scheme != "http":
			errs = append(errs, fmt.Errorf("--gds-url %q: scheme must be https or http", raw))
			continue
		case u.Hostname() == "":
			errs = append(errs, fmt.Errorf("--gds-url %q has no host", raw))
		case u.Scheme == "http" && !c.GDSAllowInsecureHTTP:
			errs = append(errs, fmt.Errorf("--gds-url %q is plaintext; set --gds-allow-insecure-http", raw))
		}
		if scheme == "" {
			scheme = u.Scheme
		} else if u.Scheme != scheme {
			errs = append(errs, fmt.Errorf("--gds-url %q: every URL must use the scheme %s", raw, scheme))
		}
	}
	if c.GDSTLSCAFile != "" {
		if _, err := os.Stat(c.GDSTLSCAFile); err != nil {
			errs = append(errs, fmt.Errorf("--gds-tls-ca-file: %w", err))
		}
	}
	switch c.IdentitySource {
	case IdentitySourceAuto, IdentitySourceClusterProperty:
	case IdentitySourceFlags:
		if c.ClusterSetID == "" || c.ClusterID == "" {
			errs = append(errs, errors.New("--cluster-identity-source=flags requires --clusterset-id and --cluster-id"))
		}
	default:
		errs = append(errs, fmt.Errorf("--cluster-identity-source must be auto, flags or clusterproperty, got %q", c.IdentitySource))
	}
	for flagName, v := range map[string]string{"--clusterset-id": c.ClusterSetID, "--cluster-id": c.ClusterID} {
		if v == "" {
			continue
		}
		if msgs := validation.IsDNS1123Label(v); len(msgs) > 0 {
			errs = append(errs, fmt.Errorf("%s %q is not a DNS label: %s", flagName, v, strings.Join(msgs, "; ")))
		}
	}
	for _, k := range slices.Sorted(maps.Keys(c.ClusterLabels)) {
		if msgs := validation.IsQualifiedName(k); len(msgs) > 0 {
			errs = append(errs, fmt.Errorf("--cluster-labels key %q: %s", k, strings.Join(msgs, "; ")))
		}
		if msgs := validation.IsValidLabelValue(c.ClusterLabels[k]); len(msgs) > 0 {
			errs = append(errs, fmt.Errorf("--cluster-labels value of %q: %s", k, strings.Join(msgs, "; ")))
		}
	}
	if len(c.ClusterLabels) > api.MaxLabels {
		errs = append(errs, fmt.Errorf("--cluster-labels has %d entries; at most %d are allowed", len(c.ClusterLabels), api.MaxLabels))
	}
	if c.LeaseTTL < MinLeaseTTL || c.LeaseTTL > MaxLeaseTTL {
		errs = append(errs, fmt.Errorf("--lease-ttl must be between %s and %s, got %s", MinLeaseTTL, MaxLeaseTTL, c.LeaseTTL))
	}
	for _, ns := range c.WatchNamespaces {
		if msgs := validation.IsDNS1123Label(ns); len(msgs) > 0 {
			errs = append(errs, fmt.Errorf("--watch-namespaces entry %q is not a namespace name: %s", ns, strings.Join(msgs, "; ")))
		}
	}
	if c.EndpointsPerSlice < 1 || c.EndpointsPerSlice > MaxEndpointsPerSlice {
		errs = append(errs, fmt.Errorf("--endpoints-per-slice must be between 1 and %d, got %d", MaxEndpointsPerSlice, c.EndpointsPerSlice))
	}
	if c.MaxConcurrentReconciles < 1 {
		errs = append(errs, fmt.Errorf("--max-concurrent-reconciles must be at least 1, got %d", c.MaxConcurrentReconciles))
	}
	if c.HealthProbeBindAddress == "" {
		errs = append(errs, errors.New("--health-probe-bind-address is required"))
	}
	if c.LeaderElect && c.OperatorNamespace == "" {
		errs = append(errs, fmt.Errorf("--operator-namespace (or $%s) is required with --leader-elect", PodNamespaceEnv))
	}
	if c.OperatorNamespace != "" {
		if msgs := validation.IsDNS1123Label(c.OperatorNamespace); len(msgs) > 0 {
			errs = append(errs, fmt.Errorf("--operator-namespace %q is not a namespace name: %s", c.OperatorNamespace, strings.Join(msgs, "; ")))
		}
	}
	return errors.Join(errs...)
}

// WatchesNamespace reports whether the controller handles objects in ns.
func (c *Config) WatchesNamespace(ns string) bool {
	return len(c.WatchNamespaces) == 0 || slices.Contains(c.WatchNamespaces, ns)
}

// NewScheme returns the controller's scheme: the client-go types (core and
// discovery/v1 among them) and multicluster.x-k8s.io/v1beta1. ClusterProperty
// objects are handled as unstructured.
func NewScheme() (*runtime.Scheme, error) {
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := mcsv1beta1.Install(s); err != nil {
		return nil, err
	}
	return s, nil
}

// ManagerOptions builds the controller-runtime manager options. With
// WatchNamespaces set, the caches of namespaced objects (Services,
// EndpointSlices, ServiceExports, ServiceImports) are restricted to those
// namespaces; Namespaces themselves stay cluster-wide. Managed fields are
// stripped from cached objects.
func (c *Config) ManagerOptions(scheme *runtime.Scheme) ctrl.Options {
	opts := ctrl.Options{
		Scheme:                        scheme,
		Metrics:                       metricsserver.Options{BindAddress: c.MetricsBindAddress},
		HealthProbeBindAddress:        c.HealthProbeBindAddress,
		LeaderElection:                c.LeaderElect,
		LeaderElectionID:              api.LeaderElectionID,
		LeaderElectionNamespace:       c.OperatorNamespace,
		LeaderElectionReleaseOnCancel: true,
		Cache:                         cache.Options{DefaultTransform: cache.TransformStripManagedFields()},
	}
	if len(c.WatchNamespaces) > 0 {
		namespaces := make(map[string]cache.Config, len(c.WatchNamespaces))
		for _, ns := range c.WatchNamespaces {
			namespaces[ns] = cache.Config{}
		}
		opts.Cache.DefaultNamespaces = namespaces
	}
	return opts
}

// urlList is a flag.Value for repeatable, comma-separated URLs. A default
// from the environment is replaced, not extended, by the first flag.
type urlList struct {
	target     *[]string
	fromEnv    bool
	setOnFlags bool
}

func (l *urlList) setDefault(v string) {
	*l.target = splitList(v)
	l.fromEnv = true
}

func (l *urlList) String() string {
	if l == nil || l.target == nil {
		return ""
	}
	return strings.Join(*l.target, ",")
}

func (l *urlList) Set(v string) error {
	if !l.setOnFlags {
		*l.target = nil
		l.setOnFlags = true
	}
	*l.target = append(*l.target, splitList(v)...)
	return nil
}

func splitList(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
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
	seen := map[string]bool{}
	out := []string{}
	for _, ns := range splitList(value) {
		if !seen[ns] {
			seen[ns] = true
			out = append(out, ns)
		}
	}
	sort.Strings(out)
	*l = out
	return nil
}

// labelMap is a flag.Value for comma-separated key=value pairs.
type labelMap map[string]string

func (m *labelMap) String() string {
	if m == nil || len(*m) == 0 {
		return ""
	}
	parts := make([]string, 0, len(*m))
	for _, k := range slices.Sorted(maps.Keys(*m)) {
		parts = append(parts, k+"="+(*m)[k])
	}
	return strings.Join(parts, ",")
}

func (m *labelMap) Set(value string) error {
	out := map[string]string{}
	for _, kv := range splitList(value) {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return fmt.Errorf("%q is not key=value", kv)
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	*m = out
	return nil
}
