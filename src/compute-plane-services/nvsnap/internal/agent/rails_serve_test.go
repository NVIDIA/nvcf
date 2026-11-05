// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stesting "k8s.io/client-go/testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
)

var podsResource = schema.GroupResource{Resource: "pods"}

// servePodOn is node's serve pod as the API holds it: Ready, claimed until
// until, at resourceVersion rv.
func servePodOn(node string, until time.Time, rv string) *corev1.Pod {
	p := railsServePod(testRailsConfig(), node, "/ck", nil, until)
	p.Namespace = "nvsnap-system"
	p.ResourceVersion = rv
	p.Status = corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.1.1",
		ContainerStatuses: []corev1.ContainerStatus{{Name: "serve", Ready: true}}}
	return p
}

// fetchPodReading is a fetch pod of another node reading from sources.
func fetchPodReading(name string, phase corev1.PodPhase, sources ...string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "nvsnap-system",
			Labels:      map[string]string{railsLabelComponent: "rails-fetch", railsLabelFor: "node-X"},
			Annotations: map[string]string{railsAnnoSources: strings.Join(sources, ",")}},
		Status: corev1.PodStatus{Phase: phase},
	}
}

// patchedUntil records the wanted-until and resourceVersion each patch of
// a serve pod carries.
func patchedUntil(c *railsCluster) *[]map[string]string {
	var got []map[string]string
	c.kube.PrependReactor("patch", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		var p struct {
			Metadata struct {
				ResourceVersion string            `json:"resourceVersion"`
				Annotations     map[string]string `json:"annotations"`
			} `json:"metadata"`
		}
		_ = json.Unmarshal(action.(k8stesting.PatchAction).GetPatch(), &p)
		got = append(got, map[string]string{"rv": p.Metadata.ResourceVersion, "until": p.Metadata.Annotations[railsAnnoWantedUntil]})
		return false, nil, nil
	})
	return &got
}

// No serve pod yet: it is created once, by its fixed name, claimed until
// the fetcher's time; a second fetcher finds it and creates nothing.
func TestAcquireServePod_CreatesOnce(t *testing.T) {
	c := newRailsCluster(t)
	a, _ := railsAgent(t, "", c)
	until := time.Now().Add(time.Minute).Truncate(time.Second)
	for range 2 {
		name, err := a.acquireServePod(t.Context(), "node-A", until)
		if err != nil || name != "nvsnap-rails-serve-node-A" {
			t.Fatalf("acquire: %q %v", name, err)
		}
	}
	if got := c.createdOf("rails-serve"); len(got) != 1 {
		t.Errorf("created %v, want one serve pod", got)
	}
	if u := c.created[0].Annotations[railsAnnoWantedUntil]; u != until.UTC().Format(time.RFC3339) {
		t.Errorf("wanted-until %q, want %s", u, until.UTC().Format(time.RFC3339))
	}
}

// A serve pod claimed for less time than this fetcher needs is extended by
// a patch that carries the resourceVersion read; one claimed for longer is
// left alone.
func TestAcquireServePod_ExtendsClaim(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	c := newRailsCluster(t, servePodOn("node-A", now.Add(-time.Minute), "7"), servePodOn("node-B", now.Add(time.Hour), "8"))
	patches := patchedUntil(c)
	a, _ := railsAgent(t, "", c)
	until := now.Add(5 * time.Minute)
	for _, node := range []string{"node-A", "node-B"} {
		if _, err := a.acquireServePod(t.Context(), node, until); err != nil {
			t.Fatalf("acquire %s: %v", node, err)
		}
	}
	if len(*patches) != 1 || (*patches)[0]["rv"] != "7" || (*patches)[0]["until"] != until.UTC().Format(time.RFC3339) {
		t.Errorf("patches %v, want one of node-A's pod at rv 7 to %s", *patches, until.UTC().Format(time.RFC3339))
	}
	if len(c.created) != 0 {
		t.Errorf("created %v, want the existing pods reused", c.createdNames())
	}
}

// The patch loses a race (the pod changed since it was read): the fetcher
// reads the pod again and patches what it read then.
func TestAcquireServePod_RetriesOnConflict(t *testing.T) {
	c := newRailsCluster(t, servePodOn("node-A", time.Now().Add(-time.Minute), "7"))
	a, _ := railsAgent(t, "", c)
	var calls int32
	c.kube.PrependReactor("patch", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			return true, nil, apierrors.NewConflict(podsResource, "nvsnap-rails-serve-node-A", errors.New("changed"))
		}
		return false, nil, nil
	})
	if _, err := a.acquireServePod(t.Context(), "node-A", time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Errorf("patched %d times, want a retry after the conflict", calls)
	}
}

// Another fetcher creates the pod between this one's read and its create:
// AlreadyExists, and the pod is used.
func TestAcquireServePod_CreateRace(t *testing.T) {
	c := newRailsCluster(t)
	a, _ := railsAgent(t, "", c)
	var raced int32
	c.kube.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if atomic.AddInt32(&raced, 1) == 1 {
			other := servePodOn("node-A", time.Now().Add(time.Hour), "3")
			_ = c.kube.Tracker().Add(other)
			return true, nil, apierrors.NewAlreadyExists(podsResource, other.Name)
		}
		return false, nil, nil
	})
	name, err := a.acquireServePod(t.Context(), "node-A", time.Now().Add(time.Minute))
	if err != nil || name != "nvsnap-rails-serve-node-A" {
		t.Fatalf("acquire: %q %v", name, err)
	}
	if atomic.LoadInt32(&raced) != 1 {
		t.Errorf("create called %d times, want once", raced)
	}
}

// A serve pod being deleted is not reused: the fetcher waits for it to go
// and creates a new one.
func TestAcquireServePod_WaitsOutATerminatingPod(t *testing.T) {
	old := servePodOn("node-A", time.Now().Add(time.Hour), "5")
	old.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	old.Finalizers = []string{"x"}
	c := newRailsCluster(t, old)
	a, _ := railsAgent(t, "", c)
	var gets int32
	c.kube.PrependReactor("get", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		if atomic.AddInt32(&gets, 1) == 3 {
			_ = c.kube.Tracker().Delete(corev1.SchemeGroupVersion.WithResource("pods"), "nvsnap-system", old.Name)
		}
		return false, nil, nil
	})
	if _, err := a.acquireServePod(t.Context(), "node-A", time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if got := c.createdOf("rails-serve"); len(got) != 1 {
		t.Errorf("created %v, want a new serve pod after the old one went", got)
	}
}

// A serve pod that ended (its deadline, a crash) is deleted, at the
// resourceVersion read, and replaced.
func TestAcquireServePod_ReplacesAnEndedPod(t *testing.T) {
	ended := servePodOn("node-A", time.Now().Add(time.Hour), "9")
	ended.Status.Phase = corev1.PodFailed
	c := newRailsCluster(t, ended)
	a, _ := railsAgent(t, "", c)
	var precondition string
	c.kube.PrependReactor("delete", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if p := action.(k8stesting.DeleteAction).GetDeleteOptions().Preconditions; p != nil && p.ResourceVersion != nil {
			precondition = *p.ResourceVersion
		}
		return false, nil, nil
	})
	if _, err := a.acquireServePod(t.Context(), "node-A", time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if precondition != "9" {
		t.Errorf("delete precondition %q, want resourceVersion 9", precondition)
	}
	if got := c.createdOf("rails-serve"); len(got) != 1 {
		t.Errorf("created %v, want the ended pod replaced", got)
	}
}

// The reaper deletes this node's serve pod only once its claim has passed
// and no running fetch pod reads from the node, and always with the
// resourceVersion it read.
func TestReapServePod(t *testing.T) {
	now := time.Now()
	self := "receiver-node"
	for _, tc := range []struct {
		name    string
		serve   *corev1.Pod
		fetches []*corev1.Pod
		deleted bool
	}{
		{name: "claimed", serve: servePodOn(self, now.Add(time.Minute), "4")},
		{name: "a running fetch reads from it", serve: servePodOn(self, now.Add(-time.Minute), "4"),
			fetches: []*corev1.Pod{fetchPodReading("f1", corev1.PodRunning, "node-Z", self)}},
		{name: "a pending fetch reads from it", serve: servePodOn(self, now.Add(-time.Minute), "4"),
			fetches: []*corev1.Pod{fetchPodReading("f1", corev1.PodPending, self)}},
		{name: "idle", serve: servePodOn(self, now.Add(-time.Minute), "4"), deleted: true},
		{name: "only ended fetches and fetches of other nodes", serve: servePodOn(self, now.Add(-time.Minute), "4"),
			fetches: []*corev1.Pod{fetchPodReading("f1", corev1.PodSucceeded, self), fetchPodReading("f2", corev1.PodRunning, "node-Z")},
			deleted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			objs := []runtime.Object{tc.serve}
			for _, f := range tc.fetches {
				objs = append(objs, f)
			}
			c := newRailsCluster(t, objs...)
			a, _ := railsAgent(t, "", c)
			var precondition string
			c.kube.PrependReactor("delete", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
				if p := action.(k8stesting.DeleteAction).GetDeleteOptions().Preconditions; p != nil && p.ResourceVersion != nil {
					precondition = *p.ResourceVersion
				}
				return false, nil, nil
			})
			a.reapServePod(t.Context(), now)
			if got := len(c.deletedNames()) == 1; got != tc.deleted {
				t.Fatalf("deleted %v, want deleted=%v", c.deletedNames(), tc.deleted)
			}
			if tc.deleted && precondition != "4" {
				t.Errorf("delete precondition %q, want resourceVersion 4", precondition)
			}
		})
	}
}

// A fetcher's claim lands between the reaper's read and its delete: the
// delete's precondition fails and the pod stays.
func TestReapServePod_LosesToAClaim(t *testing.T) {
	c := newRailsCluster(t, servePodOn("receiver-node", time.Now().Add(-time.Minute), "4"))
	a, _ := railsAgent(t, "", c)
	c.kube.PrependReactor("delete", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewConflict(podsResource, action.(k8stesting.DeleteAction).GetName(), errors.New("precondition failed"))
	})
	a.reapServePod(t.Context(), time.Now())
	if _, err := c.kube.CoreV1().Pods("nvsnap-system").Get(t.Context(), "nvsnap-rails-serve-receiver-node", metav1.GetOptions{}); err != nil {
		t.Errorf("serve pod gone after a lost race: %v", err)
	}
}

// An ended serve pod is reaped whatever its claim.
func TestReapServePod_EndedPod(t *testing.T) {
	ended := servePodOn("receiver-node", time.Now().Add(time.Hour), "4")
	ended.Status.Phase = corev1.PodFailed
	c := newRailsCluster(t, ended)
	a, _ := railsAgent(t, "", c)
	a.reapServePod(t.Context(), time.Now())
	if len(c.deletedNames()) != 1 {
		t.Errorf("deleted %v, want the ended serve pod", c.deletedNames())
	}
}

// Sources: a node fetching for itself comes last and is taken only when no
// other node lists the checkpoint; a node with a Ready serve pod comes
// first.
func TestRailsSources_Ranking(t *testing.T) {
	has, id, _ := newPeerAgent(t)
	noCheckpoint := httptest.NewServer(http.NotFoundHandler()) // an agent without it
	t.Cleanup(noCheckpoint.Close)
	lacks := noCheckpoint.URL
	busy := fetchPodReading("f1", corev1.PodRunning)
	busy.Labels[railsLabelFor] = "node-busy"
	ready := servePodOn("node-ready", time.Now().Add(time.Hour), "1")
	for _, tc := range []struct {
		name  string
		peers []peerInfo
		max   int
		want  []string
	}{
		{name: "the busy node is skipped", max: 2, want: []string{"node-ready", "node-idle"},
			peers: []peerInfo{{NodeName: "node-busy", AgentURL: has}, {NodeName: "node-idle", AgentURL: has}, {NodeName: "node-ready", AgentURL: has}}},
		{name: "a Ready serve pod first", max: 1, want: []string{"node-ready"},
			peers: []peerInfo{{NodeName: "node-idle", AgentURL: has}, {NodeName: "node-ready", AgentURL: has}}},
		{name: "the busy node when no other lists it", max: 2, want: []string{"node-busy"},
			peers: []peerInfo{{NodeName: "node-idle", AgentURL: lacks}, {NodeName: "node-busy", AgentURL: has}}},
		{name: "not the busy node once another lists it", max: 2, want: []string{"node-idle"},
			peers: []peerInfo{{NodeName: "node-ready", AgentURL: lacks}, {NodeName: "node-idle", AgentURL: has}, {NodeName: "node-busy", AgentURL: has}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newRailsCluster(t, busy.DeepCopy(), ready.DeepCopy())
			a, _ := railsAgent(t, "", c)
			a.config.Rails.MaxSources = tc.max
			got, size := a.railsSources(t.Context(), id, tc.peers, a.log.WithField("t", tc.name))
			if strings.Join(got, ",") != strings.Join(tc.want, ",") || size <= 0 {
				t.Errorf("sources %v size %d, want %v", got, size, tc.want)
			}
		})
	}
}

// A checkpoint under the rails minimum starts no pod.
func TestFetchOverRails_UnderMinSize(t *testing.T) {
	peerURL, id, total := newPeerAgent(t)
	c := newRailsCluster(t)
	a, dir := railsAgent(t, "", c)
	a.config.Rails.MinSize = ""
	if total >= railsDefaultMinBytes {
		t.Fatalf("test checkpoint of %d bytes is not small", total)
	}
	local := filepath.Join(dir, id)
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}
	err := a.fetchOverRails(t.Context(), id, local, []peerInfo{{NodeName: "node-A", AgentURL: peerURL}})
	if !errors.Is(err, errRailsUnavailable) || !strings.Contains(err.Error(), "minimum") {
		t.Errorf("err %v, want the rails minimum", err)
	}
	if len(c.created) != 0 {
		t.Errorf("created %v for a small checkpoint", c.createdNames())
	}
}

// On a cluster with no L2 and no shared filesystem, a small checkpoint
// comes from the HTTP peers, with no rails pod.
func TestEnsureLocal_SmallCheckpointFromPeers(t *testing.T) {
	peerURL, id, total := newPeerAgent(t)
	catalogURL, peerAdds := fakeCatalog(t, []map[string]string{{"node_name": "node-A", "agent_url": peerURL}})
	c := newRailsCluster(t)
	a, dir := railsAgent(t, catalogURL, c)
	a.config.Rails.MinSize = ""
	if err := a.EnsureLocal(t.Context(), id); err != nil {
		t.Fatalf("EnsureLocal: %v", err)
	}
	var got int64
	_ = filepath.Walk(filepath.Join(dir, id), func(_ string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			got += info.Size()
		}
		return nil
	})
	if got != total || !localCheckpointComplete(dir, id) {
		t.Errorf("fetched %d of %d bytes, complete=%v", got, total, localCheckpointComplete(dir, id))
	}
	if len(c.created) != 0 {
		t.Errorf("created rails pods %v for a small checkpoint", c.createdNames())
	}
	if atomic.LoadInt32(peerAdds) != 1 {
		t.Errorf("peer-add fired %d times, want 1", atomic.LoadInt32(peerAdds))
	}
}

// A node that serves and fetches at once (an instance restored onto its
// own nodes, ranks swapped): its rails pod, already serving another node,
// runs its fetch. Nothing is deleted, and the pod's claim is moved to
// cover the fetch.
func TestFetchOverRails_FetchesInItsServingRailsPod(t *testing.T) {
	peerURL, id, _ := newPeerAgent(t)
	c := newRailsCluster(t, servePodOn("receiver-node", time.Now().Add(time.Minute), "4"))
	a, dir := railsAgent(t, "", c)
	local := filepath.Join(dir, id)
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}
	claims := patchedUntil(c)
	c.fetchFn = func([]string) { _ = os.WriteFile(filepath.Join(local, "inventory.img"), []byte("inv"), 0o600) }
	if err := a.fetchOverRails(t.Context(), id, local, []peerInfo{{NodeName: "node-A", AgentURL: peerURL}}); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if got := c.createdNames(); !sameSet(got, []string{"nvsnap-rails-serve-node-A"}) {
		t.Errorf("created %v, want only node-A's rails pod: this node's already runs", got)
	}
	if len(c.deletedNames()) != 0 {
		t.Errorf("deleted %v: the serving rails pod must stay", c.deletedNames())
	}
	if len(c.jobArgs()) != 1 {
		t.Errorf("%d fetches handed over, want 1", len(c.jobArgs()))
	}
	extended := false
	for _, p := range *claims {
		if u, err := time.Parse(time.RFC3339, p["until"]); err == nil && u.After(time.Now().Add(railsServeReadyTimeout)) {
			extended = true
		}
	}
	if !extended {
		t.Errorf("this node's rails pod's claim was not moved to cover the fetch: %v", *claims)
	}
}

// countingCatalog serves a checkpoint's sources and peer-adds, and counts
// the L2 tier's lookups of the checkpoint's row.
func countingCatalog(t *testing.T, peers []map[string]string) (url string, l2Lookups *int32) {
	t.Helper()
	var lookups int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/sources"):
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"peers": peers})
		case strings.HasSuffix(r.URL.Path, "/peer-add"):
			w.WriteHeader(http.StatusNoContent)
		default: // the L2 tier's row lookup
			atomic.AddInt32(&lookups, 1)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &lookups
}

// On a cluster with an L2 volume, a small checkpoint still comes from the
// peers first: the volume is not consulted.
func TestEnsureLocal_SmallCheckpointPeersBeforeL2(t *testing.T) {
	peerURL, id, _ := newPeerAgent(t)
	catalogURL, l2Lookups := countingCatalog(t, []map[string]string{{"node_name": "node-A", "agent_url": peerURL}})
	c := newRailsCluster(t)
	a, dir := railsAgent(t, catalogURL, c)
	a.config.Rails.MinSize = ""
	a.l2Backend = &checkpointstore.PerCapturePVCBackend{KubeClient: c.kube, Namespace: "nvsnap-system"}
	if err := a.EnsureLocal(t.Context(), id); err != nil {
		t.Fatalf("EnsureLocal: %v", err)
	}
	if !localCheckpointComplete(dir, id) {
		t.Error("checkpoint not complete")
	}
	if n := atomic.LoadInt32(l2Lookups); n != 0 {
		t.Errorf("the L2 tier was consulted %d times for a small checkpoint the peers hold", n)
	}
}

// The peers cannot deliver a small checkpoint: the L2 tier is tried after
// them, and with nothing there the fetch fails.
func TestEnsureLocal_SmallCheckpointFallsBackToL2(t *testing.T) {
	gone := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(gone.Close)
	peerURL, id, _ := newPeerAgent(t)
	// The source lists it (the rails tier reads its size) but serves no
	// file of it (the peer tier fails).
	flaky := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/manifest") {
			http.NotFound(w, r)
			return
		}
		resp, err := http.Get(peerURL + r.URL.Path) //nolint:noctx,gosec // proxies to the test peer agent
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(flaky.Close)
	catalogURL, l2Lookups := countingCatalog(t, []map[string]string{{"node_name": "node-A", "agent_url": flaky.URL}})
	c := newRailsCluster(t)
	a, _ := railsAgent(t, catalogURL, c)
	a.config.Rails.MinSize = ""
	a.l2Backend = &checkpointstore.PerCapturePVCBackend{KubeClient: c.kube, Namespace: "nvsnap-system"}
	err := a.EnsureLocal(t.Context(), id)
	if err == nil || !strings.Contains(err.Error(), "no tier delivered it") {
		t.Fatalf("EnsureLocal: %v, want the small checkpoint's failure after the peers and L2", err)
	}
	if n := atomic.LoadInt32(l2Lookups); n == 0 {
		t.Error("the L2 tier was not tried after the peers failed")
	}
}
