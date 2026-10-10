// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

// A CRIU checkpoint holds the process's memory: its tokens, keys and
// request data. The configuration hash cannot tell two tenants apart (it
// hashes secret references, not their values), so a group record is keyed
// by the configuration and the tenant scope together, and a pod is only
// ever restored from a capture of its own scope.
//
// The scope comes from the namespace, never the pod: a pod's labels are
// the tenant's to write, the platform's namespace labels are not.

// criuScopeLabels name the namespace labels that identify the tenant
// workload, most specific first. NVCF labels each function namespace
// with its function version.
var criuScopeLabels = []string{"function-version-id", "FUNCTION_VERSION_ID"}

const criuScopeTTL = 5 * time.Minute

type criuScopeEntry struct {
	scope string
	at    time.Time
}

var criuScopeCache sync.Map // namespace -> criuScopeEntry

// criuGroupScope returns the tenant scope of namespace ns: its function
// version when the platform labelled it, else the namespace itself.
func (a *Agent) criuGroupScope(ctx context.Context, ns string) (string, error) {
	if e, ok := criuScopeCache.Load(ns); ok && time.Since(e.(criuScopeEntry).at) < criuScopeTTL {
		return e.(criuScopeEntry).scope, nil
	}
	n, err := a.kubeClient.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	scope := criuScopeOf(ns, n.Labels)
	criuScopeCache.Store(ns, criuScopeEntry{scope: scope, at: time.Now()})
	return scope, nil
}

func criuScopeOf(ns string, labels map[string]string) string {
	for _, l := range criuScopeLabels {
		if v := labels[l]; v != "" {
			return "function-version/" + v
		}
	}
	return "namespace/" + ns
}

// criuGroupKey is the group record key of a configuration (its cache URI)
// in a tenant scope.
func criuGroupKey(cacheURI, scope string) string {
	sum := sha256.Sum256([]byte(strings.TrimPrefix(cacheURI, "cache://") + "\x00" + scope))
	return hex.EncodeToString(sum[:8])
}

// lookupCRIUGroupFor is the webhook's view of the group records: the
// complete capture of uri in namespace ns's scope.
func (a *Agent) lookupCRIUGroupFor(ctx context.Context, ns, uri string) (webhook.CRIUGroup, bool) {
	scope, err := a.criuGroupScope(ctx, ns)
	if err != nil {
		a.log.WithError(err).WithField("namespace", ns).Warn("CRIU group restore: cannot read the namespace's scope; cold start")
		return webhook.CRIUGroup{}, false
	}
	return a.lookupCRIUGroup(ctx, criuGroupKey(uri, scope))
}
