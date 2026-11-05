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

package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func testRailsConfig() RailsConfig {
	return RailsConfig{ //nolint:gosec // SecretName names a Secret; no credential here
		Image:         "registry.example/bedrock-peer@sha256:0123",
		Namespace:     "nvsnap-system",
		ClaimTemplate: "nvsnap-rails",
		SecretName:    "nvsnap-rails",
		PullSecrets:   []string{"pull", ""},
		ServeCPU:      "16", ServeMemory: "32Gi",
		FetchCPU: "32", FetchMemory: "64Gi",
		// The test checkpoints are a few KB: let them take the rails.
		MinSize: "1",
	}
}

var testOwner = &metav1.OwnerReference{APIVersion: "v1", Kind: "Pod", Name: "nvsnap-agent-x", UID: "u1"}

// The rails pod: placed by nodeSelector (the scheduler allocates the DRA
// claim; nodeName would skip it), claims the NICs, reads the shared
// secret, runs unprivileged with IPC_LOCK only, and goes with its node's
// agent pod.
func TestRailsPods_CommonSpec(t *testing.T) {
	serve := railsServePod(testRailsConfig(), "src-node", "/var/lib/containerd/nvsnap-checkpoints", testOwner, time.Now())
	for _, pod := range []*corev1.Pod{serve} {
		s := pod.Spec
		if s.NodeName != "" {
			t.Errorf("%s: nodeName set; the DRA claim would never be allocated", pod.Labels[railsLabelComponent])
		}
		if len(s.ResourceClaims) != 1 || *s.ResourceClaims[0].ResourceClaimTemplateName != "nvsnap-rails" {
			t.Errorf("%s: resource claims %+v", pod.Labels[railsLabelComponent], s.ResourceClaims)
		}
		c := s.Containers[0]
		if len(c.Resources.Claims) != 1 || c.Resources.Claims[0].Name != s.ResourceClaims[0].Name {
			t.Errorf("%s: container does not use the claim: %+v", pod.Labels[railsLabelComponent], c.Resources.Claims)
		}
		if c.SecurityContext.Privileged != nil && *c.SecurityContext.Privileged {
			t.Errorf("%s: privileged", pod.Labels[railsLabelComponent])
		}
		if !slices.Equal(c.SecurityContext.Capabilities.Add, []corev1.Capability{"IPC_LOCK"}) {
			t.Errorf("%s: capabilities %v, want IPC_LOCK only", pod.Labels[railsLabelComponent], c.SecurityContext.Capabilities.Add)
		}
		if ref := c.Env[0].ValueFrom.SecretKeyRef; c.Env[0].Name != "BEDROCK_PEER_SECRET" || ref.Name != "nvsnap-rails" || ref.Key != "secret" {
			t.Errorf("%s: secret env %+v", pod.Labels[railsLabelComponent], c.Env)
		}
		if len(pod.OwnerReferences) != 1 || pod.OwnerReferences[0].Name != "nvsnap-agent-x" {
			t.Errorf("%s: owner %+v", pod.Labels[railsLabelComponent], pod.OwnerReferences)
		}
		if len(s.ImagePullSecrets) != 1 || s.ImagePullSecrets[0].Name != "pull" {
			t.Errorf("%s: pull secrets %+v", pod.Labels[railsLabelComponent], s.ImagePullSecrets)
		}
		if s.ActiveDeadlineSeconds == nil || *s.ActiveDeadlineSeconds <= 0 {
			t.Errorf("%s: no deadline", pod.Labels[railsLabelComponent])
		}
	}
}

// One rails pod per node, by a fixed name: it serves the node's whole
// checkpoint directory to up to ServeMaxFetchers fetches at once, and runs
// the node's own fetches into it, so the directory is writable and the pod
// gets the larger of the serve and fetch budgets.
func TestRailsServePod(t *testing.T) {
	until := time.Date(2026, 10, 23, 22, 0, 0, 0, time.UTC)
	pod := railsServePod(testRailsConfig(), "src-node", "/var/lib/containerd/nvsnap-checkpoints", testOwner, until)
	if pod.Name != "nvsnap-rails-serve-src-node" || pod.GenerateName != "" {
		t.Errorf("name %q generateName %q, want the node's fixed name", pod.Name, pod.GenerateName)
	}
	if pod.Spec.NodeSelector["kubernetes.io/hostname"] != "src-node" {
		t.Errorf("serve pod placed on %v, want src-node", pod.Spec.NodeSelector)
	}
	if pod.Labels[railsLabelServes] != "src-node" || pod.Labels[railsLabelFor] != "" {
		t.Errorf("labels %v: want it labelled by the node it serves, not by a fetcher", pod.Labels)
	}
	if pod.Annotations[railsAnnoWantedUntil] != "2026-10-23T22:00:00Z" {
		t.Errorf("wanted-until %q", pod.Annotations[railsAnnoWantedUntil])
	}
	if hp := pod.Spec.Volumes[0].HostPath; hp == nil || hp.Path != "/var/lib/containerd/nvsnap-checkpoints" {
		t.Errorf("volume %+v, want the node's checkpoint directory", pod.Spec.Volumes[0])
	}
	c := pod.Spec.Containers[0]
	if c.VolumeMounts[0].ReadOnly {
		t.Error("the rails pod mounts the checkpoints read-only; its fetches could not land")
	}
	if c.Command[0] != "/bin/sh" || !strings.Contains(c.Command[2], "/data/.rails-jobs") || c.Command[3] != "bedrock-peer" {
		t.Errorf("command %q, want the jobs loop around bedrock-peer", c.Command)
	}
	if c.ReadinessProbe == nil {
		t.Error("serve pod has no readiness probe; the fetch would start before it listens")
	}
	want := "serve --root /data --devices all --port 9890 --max-fetchers 4 --ready-file /tmp/serve.ready"
	if got := strings.Join(c.Args, " "); got != want {
		t.Errorf("serve args %q, want %q", got, want)
	}
	if q := c.Resources.Limits.Cpu().String(); q != "32" {
		t.Errorf("CPU limit %s, want the fetch's 32", q)
	}
	if q := c.Resources.Limits.Memory().String(); q != "64Gi" {
		t.Errorf("memory limit %s, want the fetch's 64Gi, not the sum", q)
	}
}

// A fetch reads its checkpoint, by prefix, from every source into the
// checkpoint's directory under the rails pod's mount.
func TestRailsFetchArgs(t *testing.T) {
	got := strings.Join(railsFetchArgs(testRailsConfig(), []string{"10.0.0.1:9890", "10.0.0.2:9890"}, "ck1"), " ")
	want := "fetch --source 10.0.0.1:9890 --source 10.0.0.2:9890 --prefix ck1 --devices all --dest /data/ck1 --connections 32 --json"
	if got != want {
		t.Errorf("fetch args %q, want %q", got, want)
	}
}

func TestLargerQuantity(t *testing.T) {
	for _, tc := range [][3]string{{"32Gi", "64Gi", "64Gi"}, {"64Gi", "32Gi", "64Gi"}, {"16", "32", "32"}, {"", "8Gi", "8Gi"}, {"8Gi", "", "8Gi"}, {"1024Mi", "1Gi", "1024Mi"}} {
		if got := largerQuantity(tc[0], tc[1]); got != tc[2] {
			t.Errorf("larger(%q, %q) = %q, want %q", tc[0], tc[1], got, tc[2])
		}
	}
}

func TestRailsLabelValue(t *testing.T) {
	long := strings.Repeat("n", 80)
	if v := railsLabelValue(long); len(v) > 63 || v == railsLabelValue(long+"x") {
		t.Errorf("label value %q (len %d) not a distinct fit", v, len(v))
	}
	if v := railsLabelValue("ip-10-1-134-224.us-east-2.compute.internal"); v != "ip-10-1-134-224.us-east-2.compute.internal" {
		t.Errorf("short node name changed: %q", v)
	}
}

func TestRailsConfigValidate(t *testing.T) {
	if err := (RailsConfig{}).Validate(); err != nil {
		t.Errorf("disabled config: %v", err)
	}
	if err := testRailsConfig().Validate(); err != nil {
		t.Errorf("valid config: %v", err)
	}
	bad := testRailsConfig()
	bad.ServeCPU = "sixteen"
	if err := bad.Validate(); err == nil {
		t.Error("bad CPU quantity accepted")
	}
	bad = testRailsConfig()
	bad.MinSize = "big"
	if err := bad.Validate(); err == nil {
		t.Error("bad min size accepted")
	}
	bad = testRailsConfig()
	bad.SecretName = ""
	if err := bad.Validate(); err == nil {
		t.Error("missing secret accepted")
	}
}

func TestFreeSpaceAllows(t *testing.T) {
	const total = 1000 << 20
	for _, tc := range []struct {
		avail uint64
		size  int64
		ok    bool
	}{
		{avail: 900 << 20, size: 500 << 20, ok: true},
		{avail: 900 << 20, size: 800 << 20, ok: true},  // leaves 100 MiB = 10 %
		{avail: 900 << 20, size: 850 << 20, ok: false}, // leaves 5 %
		{avail: 100 << 20, size: 200 << 20, ok: false}, // does not fit
		{avail: 900 << 20, size: -1, ok: false},
	} {
		err := freeSpaceAllows(tc.avail, total, tc.size, 10, "/x")
		if (err == nil) != tc.ok {
			t.Errorf("avail %d size %d: err %v, want ok=%v", tc.avail, tc.size, err, tc.ok)
		}
	}
}

func TestParseFetchOutput(t *testing.T) {
	res, err := parseFetchOutput(0, "copied 3 files\n"+`{"ok":true,"exit_code":0,"error":null,"error_kind":null,"files":3,"bytes":1073741824,"seconds":0.5,"retries":0,"reconnects":0,"failovers":0,"source":"10.0.0.1:9890","content_digest":"ab","attributes":"mode+mtime+owner","setid_kept":false,"setid_cleared":0,"version":"0.1.0","commit":"fd09ff3"}`+"\n", "")
	if err != nil || res.Files != 3 || res.Bytes != 1<<30 || res.Version != "0.1.0" {
		t.Fatalf("success: %+v %v", res, err)
	}
	if g := gibPerSecond(res.Bytes, res.Seconds); g != "2.00" {
		t.Errorf("rate %s, want 2.00", g)
	}

	_, err = parseFetchOutput(4, `{"ok":false,"exit_code":4,"error":"all sources busy","error_kind":"unavailable","files":0,"bytes":0}`, "")
	if err == nil || !strings.Contains(err.Error(), "unavailable") || !strings.Contains(err.Error(), "all sources busy") {
		t.Errorf("exit 4: %v", err)
	}

	// Killed by a signal: no JSON line, the exit code decides.
	_, err = parseFetchOutput(143, "", "")
	if err == nil || !strings.Contains(err.Error(), "143") {
		t.Errorf("killed: %v", err)
	}

	// A zero exit with a line that says otherwise is a failure.
	if _, err = parseFetchOutput(0, `{"ok":false,"exit_code":1,"error":"x","error_kind":"other"}`, ""); err == nil {
		t.Error("ok:false accepted")
	}
}

func TestRemoveRailsEntries(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{".bedrock-peer-content", ".bedrock-peer-staging-x/f", "inventory.img", "sub/.bedrock-peer-keep"} {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	removeRailsEntries(dir)
	for p, want := range map[string]bool{".bedrock-peer-content": false, ".bedrock-peer-staging-x": false, "inventory.img": true, "sub/.bedrock-peer-keep": true} {
		_, err := os.Stat(filepath.Join(dir, p))
		if (err == nil) != want {
			t.Errorf("%s exists=%v, want %v", p, err == nil, want)
		}
	}
}

// railsCluster is a fake API server whose rails pods become Ready, and
// the jobs loop of the receiving node's rails pod: each fetch handed to it
// runs the copy (fetchFn) and ends with exit code exit, unless hang holds
// it until it is cancelled.
type railsCluster struct {
	kube     *fake.Clientset
	serveOK  bool
	exit     int32
	hang     bool
	fetchFn  func(args []string)
	mu       sync.Mutex
	created  []*corev1.Pod
	deleted  []string
	jobs     [][]string
	sequence int32
}

// runJobs plays the rails pod's jobs loop (railsPodScript) over the jobs
// directory under dir until the test ends.
func (c *railsCluster) runJobs(t *testing.T, dir string) {
	t.Helper()
	jd := filepath.Join(dir, railsJobsDir)
	stop := make(chan struct{})
	done := make(chan struct{})
	t.Cleanup(func() { close(stop); <-done })
	go func() {
		defer close(done)
		running := map[string]bool{}
		for {
			select {
			case <-stop:
				return
			case <-time.After(2 * time.Millisecond):
			}
			if cs, _ := filepath.Glob(filepath.Join(jd, "*.cancel")); len(cs) > 0 {
				for _, cf := range cs {
					j := strings.TrimSuffix(cf, ".cancel")
					if running[j] {
						_ = os.Rename(cf, j+".cancelled")
						_ = os.WriteFile(j+".out", nil, 0o600)
						_ = os.WriteFile(j+".exit", []byte("143\n"), 0o600)
					}
				}
			}
			as, _ := filepath.Glob(filepath.Join(jd, "*.args"))
			for _, af := range as {
				j := strings.TrimSuffix(af, ".args")
				if os.Rename(af, j+".run") != nil {
					continue
				}
				b, _ := os.ReadFile(j + ".run")
				args := strings.Fields(string(b))
				c.mu.Lock()
				c.jobs = append(c.jobs, args)
				c.mu.Unlock()
				_ = os.WriteFile(j+".pid", []byte("42"), 0o600)
				running[j] = true
				if c.fetchFn != nil {
					c.fetchFn(args)
				}
				if c.hang {
					continue
				}
				out := `{"ok":true,"exit_code":0,"files":1,"bytes":100,"seconds":1}`
				if c.exit != 0 {
					out = fmt.Sprintf(`{"ok":false,"exit_code":%d,"error":"boom","error_kind":"integrity"}`, c.exit)
				}
				_ = os.WriteFile(j+".out", []byte(out+"\n"), 0o600)
				_ = os.WriteFile(j+".exit", []byte(fmt.Sprintf("%d\n", c.exit)), 0o600)
			}
		}
	}()
}

func (c *railsCluster) jobArgs() [][]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.jobs)
}

func newRailsCluster(t *testing.T, objs ...runtime.Object) *railsCluster {
	t.Helper()
	c := &railsCluster{kube: fake.NewClientset(objs...), serveOK: true}
	c.kube.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		pod := action.(k8stesting.CreateAction).GetObject().(*corev1.Pod)
		if pod.Name == "" {
			pod.Name = fmt.Sprintf("%s%d", pod.GenerateName, atomic.AddInt32(&c.sequence, 1))
		}
		switch pod.Labels[railsLabelComponent] {
		case "rails-serve":
			if c.serveOK {
				pod.Status = corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0." + fmt.Sprint(c.sequence),
					ContainerStatuses: []corev1.ContainerStatus{{Name: "serve", Ready: true}}}
			} else {
				pod.Status = corev1.PodStatus{Phase: corev1.PodPending}
			}
		}
		c.mu.Lock()
		c.created = append(c.created, pod.DeepCopy())
		c.mu.Unlock()
		return false, nil, nil
	})
	c.kube.PrependReactor("delete", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		c.mu.Lock()
		c.deleted = append(c.deleted, action.(k8stesting.DeleteAction).GetName())
		c.mu.Unlock()
		return false, nil, nil
	})
	return c
}

func (c *railsCluster) createdNames() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := make([]string, 0, len(c.created))
	for _, p := range c.created {
		n = append(n, p.Name)
	}
	return n
}

// createdOf names the created pods of one component (rails-serve,
// rails-fetch).
func (c *railsCluster) createdOf(component string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var n []string
	for _, p := range c.created {
		if p.Labels[railsLabelComponent] == component {
			n = append(n, p.Name)
		}
	}
	return n
}

func (c *railsCluster) deletedNames() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.deleted)
}

func railsAgent(t *testing.T, catalogURL string, c *railsCluster) (a *Agent, dir string) {
	t.Helper()
	a, dir = receiverAgent(t, catalogURL)
	a.config.Rails = testRailsConfig()
	// A roomy disk, whatever the one the test runs on holds.
	orig := diskSpace
	t.Cleanup(func() { diskSpace = orig })
	diskSpace = func(string) (uint64, uint64, error) { return 900 << 30, 1000 << 30, nil }
	a.kubeClient = c.kube
	c.runJobs(t, dir)
	return a, dir
}

// The happy path: the copy lands in the checkpoint directory, bedrock-peer's
// own entries are removed, the fetch pod is deleted and the shared serve
// pods are left to their nodes' agents, the node registers as a peer, and
// no HTTP file transfer happens.
func TestEnsureLocal_RailsSuccess(t *testing.T) {
	peerURL, id, _ := newPeerAgent(t)
	catalogURL, peerAdds := fakeCatalog(t, []map[string]string{
		{"node_name": "node-A", "agent_url": peerURL},
		{"node_name": "node-B", "agent_url": peerURL},
		{"node_name": "node-C", "agent_url": peerURL}, // past MaxSources
	})
	c := newRailsCluster(t)
	a, dir := railsAgent(t, catalogURL, c)
	c.fetchFn = func([]string) {
		local := filepath.Join(dir, id)
		_ = os.WriteFile(filepath.Join(local, "inventory.img"), []byte("inv"), 0o600)
		_ = os.WriteFile(filepath.Join(local, ".bedrock-peer-content"), []byte("record"), 0o600)
	}

	if err := a.EnsureLocal(t.Context(), id); err != nil {
		t.Fatalf("EnsureLocal: %v", err)
	}
	if !localCheckpointComplete(dir, id) {
		t.Error("checkpoint not complete after a rails fetch")
	}
	if _, err := os.Stat(filepath.Join(dir, id, ".bedrock-peer-content")); err == nil {
		t.Error("bedrock-peer content record left in the checkpoint")
	}
	if _, err := os.Stat(filepath.Join(dir, id, "pages-1.img")); err == nil {
		t.Error("an HTTP peer transfer ran after the rails fetch succeeded")
	}
	jobs := c.jobArgs()
	if len(jobs) != 1 {
		t.Fatalf("%d fetches handed to the rails pod, want 1", len(jobs))
	}
	fetchArgs := jobs[0]
	if n := strings.Count(strings.Join(fetchArgs, " "), "--source"); n != 2 {
		t.Errorf("fetch read from %d sources, want MaxSources=2: %v", n, fetchArgs)
	}
	if !strings.Contains(strings.Join(fetchArgs, " "), "--prefix "+id+" ") || !strings.Contains(strings.Join(fetchArgs, " "), "--dest /data/"+id+" ") {
		t.Errorf("fetch does not name its checkpoint and its directory: %v", fetchArgs)
	}
	if got := c.createdNames(); !sameSet(got, []string{"nvsnap-rails-serve-node-A", "nvsnap-rails-serve-node-B", "nvsnap-rails-serve-receiver-node"}) {
		t.Errorf("created %v, want the sources' and this node's rails pods only", got)
	}
	if len(c.deletedNames()) != 0 {
		t.Errorf("deleted %v; the rails pods are their nodes' agents' to reap", c.deletedNames())
	}
	if left, _ := filepath.Glob(filepath.Join(dir, railsJobsDir, "*")); len(left) != 0 {
		t.Errorf("job files left: %v", left)
	}
	if atomic.LoadInt32(peerAdds) != 1 {
		t.Errorf("peer-add fired %d times, want 1", atomic.LoadInt32(peerAdds))
	}
}

// No rails pod becomes Ready (a workload holds the NICs): the tier gives
// up without handing over a fetch and the HTTP peers deliver. The pods are
// their nodes' agents' to reap.
func TestEnsureLocal_RailsServeNotReadyFallsThrough(t *testing.T) {
	defer func(d time.Duration) { railsServeReadyTimeout = d }(railsServeReadyTimeout)
	railsServeReadyTimeout = 50 * time.Millisecond

	peerURL, id, _ := newPeerAgent(t)
	catalogURL, _ := fakeCatalog(t, []map[string]string{{"node_name": "node-A", "agent_url": peerURL}})
	c := newRailsCluster(t)
	c.serveOK = false
	a, dir := railsAgent(t, catalogURL, c)

	if err := a.EnsureLocal(t.Context(), id); err != nil {
		t.Fatalf("EnsureLocal: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, id, "pages-1.img")); err != nil {
		t.Errorf("HTTP peer fallback did not deliver: %v", err)
	}
	if len(c.jobArgs()) != 0 {
		t.Error("a fetch was handed over without a ready rails pod")
	}
	if len(c.created) != 2 || len(c.deletedNames()) != 0 {
		t.Errorf("created %v, deleted %v", c.createdNames(), c.deletedNames())
	}
}

// bedrock-peer fails mid-copy: its partial files are cleared and the HTTP
// peers deliver the whole checkpoint.
func TestEnsureLocal_RailsFetchFailsFallsThrough(t *testing.T) {
	peerURL, id, peerTotal := newPeerAgent(t)
	catalogURL, _ := fakeCatalog(t, []map[string]string{{"node_name": "node-A", "agent_url": peerURL}})
	c := newRailsCluster(t)
	c.exit = 5
	a, dir := railsAgent(t, catalogURL, c)
	c.fetchFn = func([]string) {
		_ = os.WriteFile(filepath.Join(dir, id, "partial.img"), []byte("half"), 0o600)
		_ = os.MkdirAll(filepath.Join(dir, id, ".bedrock-peer-staging-1"), 0o700)
	}

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
	if got != peerTotal {
		t.Errorf("checkpoint holds %d bytes, want the peer's %d (partial rails files left?)", got, peerTotal)
	}
	if _, err := os.Stat(filepath.Join(dir, id, ".bedrock-peer-staging-1")); err == nil {
		t.Error("bedrock-peer staging left in the checkpoint")
	}
	if len(c.deletedNames()) != 0 {
		t.Errorf("deleted %v; a failed fetch leaves the rails pods to their nodes' agents", c.deletedNames())
	}
	if left, _ := filepath.Glob(filepath.Join(dir, railsJobsDir, "*")); len(left) != 0 {
		t.Errorf("job files left: %v", left)
	}
}

// Not enough room: no pod is created.
func TestFetchOverRails_RefusesWithoutSpace(t *testing.T) {
	peerURL, id, _ := newPeerAgent(t)
	c := newRailsCluster(t)
	a, dir := railsAgent(t, "", c)
	a.config.Rails.KeepFreePercent = 99 // a test filesystem is never 99 % free
	local := filepath.Join(dir, id)
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}
	err := a.fetchOverRails(t.Context(), id, local, []peerInfo{{NodeName: "node-A", AgentURL: peerURL}})
	if !errors.Is(err, errRailsUnavailable) || !strings.Contains(err.Error(), "free") {
		t.Errorf("err %v, want a free-space refusal", err)
	}
	if len(c.created) != 0 {
		t.Errorf("created pods %v despite the refusal", c.createdNames())
	}
}

// Only this node and peers that do not list the checkpoint: no source.
func TestFetchOverRails_NoSource(t *testing.T) {
	peerURL, _, _ := newPeerAgent(t)
	c := newRailsCluster(t)
	a, dir := railsAgent(t, "", c)
	err := a.fetchOverRails(t.Context(), "other-id", dir, []peerInfo{
		{NodeName: "receiver-node", AgentURL: peerURL}, // this node
		{NodeName: "node-A", AgentURL: peerURL},        // does not have other-id
	})
	if !errors.Is(err, errRailsUnavailable) {
		t.Errorf("err %v, want errRailsUnavailable", err)
	}
	if len(c.created) != 0 {
		t.Errorf("created pods %v without a source", c.createdNames())
	}
}

// The restore's context ends mid-fetch: the fetch pod is still deleted.
func TestFetchOverRails_CleansUpAfterCancel(t *testing.T) {
	defer func(d time.Duration) { railsJobPoll = d }(railsJobPoll)
	railsJobPoll = 5 * time.Millisecond
	peerURL, id, _ := newPeerAgent(t)
	c := newRailsCluster(t)
	c.hang = true
	a, dir := railsAgent(t, "", c)
	ctx, cancel := context.WithCancel(t.Context())
	c.fetchFn = func([]string) { cancel() }
	local := filepath.Join(dir, id)
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := a.fetchOverRails(ctx, id, local, []peerInfo{{NodeName: "node-A", AgentURL: peerURL}}); err == nil {
		t.Fatal("fetch succeeded after its context ended")
	}
	if len(c.jobArgs()) != 1 {
		t.Errorf("%d fetches handed over, want 1", len(c.jobArgs()))
	}
	if left, _ := filepath.Glob(filepath.Join(dir, railsJobsDir, "*")); len(left) != 0 {
		t.Errorf("job files left after the cancel: %v", left)
	}
}

// The rails pod ends while it runs the fetch (its node's agent restarted
// and swept it): the fetch fails at the next pod check, not at its timeout.
func TestFetchOverRails_RailsPodGoneMidFetch(t *testing.T) {
	defer func(p, c time.Duration) { railsJobPoll, railsJobPodCheck = p, c }(railsJobPoll, railsJobPodCheck)
	railsJobPoll, railsJobPodCheck = 5*time.Millisecond, 10*time.Millisecond
	peerURL, id, _ := newPeerAgent(t)
	c := newRailsCluster(t)
	c.hang = true
	a, dir := railsAgent(t, "", c)
	c.fetchFn = func([]string) {
		_ = c.kube.CoreV1().Pods("nvsnap-system").Delete(context.Background(), "nvsnap-rails-serve-receiver-node", metav1.DeleteOptions{})
	}
	local := filepath.Join(dir, id)
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err := a.fetchOverRails(t.Context(), id, local, []peerInfo{{NodeName: "node-A", AgentURL: peerURL}})
	if !errors.Is(err, errRailsPodGone) {
		t.Errorf("err %v, want errRailsPodGone", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("gave up after %s", d)
	}
}

// This node's rails pod cannot be scheduled (a workload took the node's
// memory): the tier gives up after the short unschedulable grace, not the
// ready timeout, naming the scheduler's reason, and hands over no fetch.
func TestFetchOverRails_OwnRailsPodUnschedulable(t *testing.T) {
	defer func(d, g time.Duration) { railsServeReadyTimeout, railsPodUnschedulableGrace = d, g }(railsServeReadyTimeout, railsPodUnschedulableGrace)
	railsServeReadyTimeout = time.Hour
	railsPodUnschedulableGrace = 10 * time.Millisecond

	peerURL, id, _ := newPeerAgent(t)
	c := newRailsCluster(t)
	a, dir := railsAgent(t, "", c)
	local := filepath.Join(dir, id)
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}
	c.kube.PrependReactor("get", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.(k8stesting.GetAction).GetName() == "nvsnap-rails-serve-receiver-node" {
			// Claimed long enough that the fetcher does not patch it.
			p := railsServePod(testRailsConfig(), "receiver-node", "/ck", nil, time.Now().Add(72*time.Hour))
			p.Namespace = "nvsnap-system"
			p.Status = corev1.PodStatus{Phase: corev1.PodPending, Conditions: []corev1.PodCondition{{
				Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable,
				Message: "0/22 nodes are available: 1 Insufficient memory"}}}
			return true, p, nil
		}
		return false, nil, nil
	})
	start := time.Now()
	err := a.fetchOverRails(t.Context(), id, local, []peerInfo{{NodeName: "node-A", AgentURL: peerURL}})
	if !errors.Is(err, errRailsUnavailable) || !strings.Contains(err.Error(), "Insufficient memory") {
		t.Errorf("err %v, want errRailsUnavailable naming the scheduler's reason", err)
	}
	if d := time.Since(start); d > 30*time.Second {
		t.Errorf("gave up after %s, want about the unschedulable grace", d)
	}
	if len(c.jobArgs()) != 0 {
		t.Error("a fetch was handed to a rails pod that never started")
	}
}

// Only the scheduler's "no node" report counts as unschedulable.
func TestPodUnschedulable(t *testing.T) {
	cond := func(st corev1.ConditionStatus, reason string) *corev1.Pod {
		return &corev1.Pod{Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{
			Type: corev1.PodScheduled, Status: st, Reason: reason, Message: "m"}}}}
	}
	for name, c := range map[string]struct {
		pod  *corev1.Pod
		want bool
	}{
		"no conditions yet":       {&corev1.Pod{}, false},
		"unschedulable":           {cond(corev1.ConditionFalse, corev1.PodReasonUnschedulable), true},
		"scheduled":               {cond(corev1.ConditionTrue, ""), false},
		"not scheduled, no cause": {cond(corev1.ConditionFalse, ""), false},
		"scheduling gated":        {cond(corev1.ConditionFalse, corev1.PodReasonSchedulingGated), false},
	} {
		if _, got := podUnschedulable(c.pod); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}

// A restarted agent deletes its node's fetch pods and its node's serve pod,
// and nothing of another node's.
func TestSweepRailsPods(t *testing.T) {
	mine := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "mine", Namespace: "nvsnap-system",
		Labels: map[string]string{railsLabelFor: "receiver-node"}}}
	myServe := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "nvsnap-rails-serve-receiver-node", Namespace: "nvsnap-system",
		Labels: map[string]string{railsLabelServes: "receiver-node"}}}
	other := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "nvsnap-system",
		Labels: map[string]string{railsLabelFor: "node-B"}}}
	otherServe := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "nvsnap-rails-serve-node-B", Namespace: "nvsnap-system",
		Labels: map[string]string{railsLabelServes: "node-B"}}}
	c := newRailsCluster(t, mine, myServe, other, otherServe)
	a, _ := railsAgent(t, "", c)
	a.sweepRailsPods(t.Context())
	if !sameSet(c.deletedNames(), []string{"mine", "nvsnap-rails-serve-receiver-node"}) {
		t.Errorf("deleted %v, want [mine nvsnap-rails-serve-receiver-node]", c.deletedNames())
	}

	// Off: nothing is listed or deleted.
	c2 := newRailsCluster(t, mine)
	b, _ := railsAgent(t, "", c2)
	b.config.Rails.Image = ""
	b.sweepRailsPods(t.Context())
	if len(c2.deleted) != 0 {
		t.Errorf("disabled sweep deleted %v", c2.deleted)
	}
}

func sameSet(a, b []string) bool {
	x, y := slices.Clone(a), slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(x, y)
}
