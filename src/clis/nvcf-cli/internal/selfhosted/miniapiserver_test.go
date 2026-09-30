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

package selfhosted

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// miniAPIServer is an in-memory apiserver over real HTTP, for what the fake
// clientset cannot show: request contexts and calls that hang. A request whose
// context is already cancelled never reaches it, which is the point. It
// stores what is created, lists it back, and records deletes.
type miniAPIServer struct {
	t       *testing.T
	mu      sync.Mutex
	objects map[string]map[string]any // collection path -> name -> object
	deletes []string                  // "<collection>/<name>"
	// hangDeletes holds every DELETE until the client gives up.
	hangDeletes atomic.Bool
	// jobCreated is closed on the first Job create.
	jobCreated chan struct{}
	onceJob    sync.Once
	// runningJob, when set, is the status every Job read returns.
	runningJob bool
}

func newMiniAPIServer(t *testing.T) (*miniAPIServer, kubernetes.Interface) {
	t.Helper()
	m := &miniAPIServer{t: t, objects: map[string]map[string]any{}, jobCreated: make(chan struct{}), runningJob: true}
	srv := httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(func() {
		srv.CloseClientConnections()
		srv.Close()
	})
	client, err := kubernetes.NewForConfig(&rest.Config{
		Host:          srv.URL,
		ContentConfig: rest.ContentConfig{ContentType: "application/json"},
	})
	require.NoError(t, err)
	return m, client
}

func (m *miniAPIServer) deleted() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.deletes...)
}

func (m *miniAPIServer) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimSuffix(r.URL.Path, "/")
	collection, name := path, ""
	if i := strings.LastIndex(path, "/"); i > 0 && isNamedPath(path) {
		collection, name = path[:i], path[i+1:]
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	switch r.Method {
	case http.MethodPost:
		body, _ := io.ReadAll(r.Body)
		var obj map[string]any
		_ = json.Unmarshal(body, &obj)
		meta, _ := obj["metadata"].(map[string]any)
		n, _ := meta["name"].(string)
		meta["uid"] = "uid-" + n
		if m.objects[collection] == nil {
			m.objects[collection] = map[string]any{}
		}
		m.objects[collection][n] = obj
		if strings.HasSuffix(collection, "/jobs") {
			m.onceJob.Do(func() { close(m.jobCreated) })
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(obj)
	case http.MethodGet:
		if name == "" {
			items := []any{}
			for _, o := range m.objects[collection] {
				items = append(items, o)
			}
			// A typed client decodes only its own list kind.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"apiVersion": listAPIVersion(collection), "kind": listKinds[collection[strings.LastIndex(collection, "/")+1:]],
				"items": items,
			})
			return
		}
		obj, ok := m.objects[collection][name]
		if !ok {
			notFound(w)
			return
		}
		_ = json.NewEncoder(w).Encode(obj)
	case http.MethodDelete:
		if m.hangDeletes.Load() {
			// The server notices a closed connection only once the body has
			// been read, so read it before waiting on the request context.
			_, _ = io.Copy(io.Discard, r.Body)
			m.mu.Unlock()
			<-r.Context().Done()
			m.mu.Lock()
			return
		}
		m.deletes = append(m.deletes, collection+"/"+name)
		delete(m.objects[collection], name)
		if strings.HasSuffix(collection, "/jobs") {
			// The Job's pods go with it, as the garbage collector would.
			delete(m.objects, strings.TrimSuffix(collection, "/jobs")+"/pods")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"kind": "Status", "apiVersion": "v1", "status": "Success"})
	default:
		// PATCH and PUT: accept and echo nothing interesting.
		_ = json.NewEncoder(w).Encode(map[string]any{})
	}
}

// listKinds maps a collection's resource to the kind a typed client decodes.
var listKinds = map[string]string{
	"jobs": "JobList", "pods": "PodList", "secrets": "SecretList", "configmaps": "ConfigMapList",
	"serviceaccounts": "ServiceAccountList", "clusterroles": "ClusterRoleList",
	"clusterrolebindings": "ClusterRoleBindingList", "namespaces": "NamespaceList",
}

// listAPIVersion is the group version in a collection path: "v1" under /api,
// "<group>/<version>" under /apis.
func listAPIVersion(collection string) string {
	parts := strings.Split(strings.Trim(collection, "/"), "/")
	if len(parts) >= 3 && parts[0] == "apis" {
		return parts[1] + "/" + parts[2]
	}
	return "v1"
}

// isNamedPath reports whether path names one object rather than a collection:
// its last segment follows a resource segment.
func isNamedPath(path string) bool {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	resources := map[string]bool{
		"jobs": true, "pods": true, "secrets": true, "configmaps": true, "serviceaccounts": true,
		"clusterroles": true, "clusterrolebindings": true, "namespaces": true,
	}
	return len(parts) >= 2 && resources[parts[len(parts)-2]]
}

func notFound(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNotFound)
	_, _ = io.WriteString(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"NotFound","code":404}`)
}

// runInterrupted starts a run against m, cancels it once the Job exists, and
// returns the result.
func runInterrupted(t *testing.T, client kubernetes.Interface, m *miniAPIServer) ClusterValidatorResult {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan ClusterValidatorResult, 1)
	go func() {
		done <- runClusterValidator(ctx, client, "registry.example.com/validator:1",
			"", false, clusterValidatorComputePlaneRole, nil, nil)
	}()
	select {
	case <-m.jobCreated:
	case <-time.After(30 * time.Second):
		t.Fatal("the run never created its Job")
	}
	cancel()
	select {
	case res := <-done:
		return res
	case <-time.After(60 * time.Second):
		t.Fatal("the interrupted run did not return")
	}
	return ClusterValidatorResult{}
}

// On an interrupt the cleanup requests really go out. A cleanup context
// derived from the cancelled run context sends none, which the fake clientset
// cannot show because it ignores contexts.
func TestRunClusterValidator_InterruptSendsCleanupOverTheWire(t *testing.T) {
	m, client := newMiniAPIServer(t)
	res := runInterrupted(t, client, m)
	require.Error(t, res.Err)

	got := strings.Join(m.deleted(), " ")
	for _, want := range []string{"/jobs/", "/clusterrolebindings/", "/clusterroles/", "/serviceaccounts/"} {
		assert.Contains(t, got, want)
	}
}

// A delete that never answers does not hold the CLI open: every cleanup call
// is bounded.
func TestRunClusterValidator_HungDeletesAreBounded(t *testing.T) {
	prev := validatorCleanupTimeout
	validatorCleanupTimeout = 500 * time.Millisecond
	t.Cleanup(func() { validatorCleanupTimeout = prev })
	m, client := newMiniAPIServer(t)
	m.hangDeletes.Store(true)

	start := time.Now()
	res := runInterrupted(t, client, m)
	require.Error(t, res.Err)
	assert.Less(t, time.Since(start), 25*time.Second)
}

// A control-plane run that times out once its pod has ended makes all three
// deferred sweeps in turn, each on its own bounded context. With every DELETE
// hanging, the run still returns within ClusterValidatorRunCeiling, which the
// check's budget is sized from.
func TestClusterValidatorRunCeiling_CoversHungSweeps(t *testing.T) {
	prevTimeout, prevGrace, prevLogs, prevCleanup, prevMargin := clusterValidatorTimeout,
		validatorDeadlineGrace, clusterValidatorLogFetchTimeout, validatorCleanupTimeout, validatorRunMargin
	clusterValidatorTimeout, validatorDeadlineGrace, clusterValidatorLogFetchTimeout, validatorCleanupTimeout,
		validatorRunMargin = 3*time.Second, time.Second, 500*time.Millisecond, time.Second, 200*time.Millisecond
	t.Cleanup(func() {
		clusterValidatorTimeout, validatorDeadlineGrace, clusterValidatorLogFetchTimeout, validatorCleanupTimeout,
			validatorRunMargin = prevTimeout, prevGrace, prevLogs, prevCleanup, prevMargin
	})
	// An NGC image with NGC_API_KEY mints a pull Secret, and a registry makes
	// the ConfigMap, so each of the three sweeps has an object to delete.
	t.Setenv("NGC_API_KEY", "key")
	m, client := newMiniAPIServer(t)
	m.hangDeletes.Store(true)

	start := time.Now()
	res := runClusterValidator(context.Background(), client, "nvcr.io/nvidia/validator:1",
		"", false, clusterValidatorControlPlaneRole, []RegistryEntry{{Registry: "nvcr.io"}}, nil)
	elapsed := time.Since(start)
	require.Error(t, res.Err)
	require.Contains(t, res.Err.Error(), "waiting for job", "the run reached the Job wait and timed out there")
	assert.GreaterOrEqual(t, elapsed, validatorDeferredSweeps*validatorCleanupTimeout,
		"every sweep ran and hung until its bound")
	assert.LessOrEqual(t, elapsed, ClusterValidatorRunCeiling())
}
