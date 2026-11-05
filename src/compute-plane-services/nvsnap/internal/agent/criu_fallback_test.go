// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/modelvolume"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

const fbBundle = "8ab0d751c1f6277b"

// fallbackAgent is an agent of node n0 running bundle fbBundle, with a
// kube client holding objs.
func fallbackAgent(t *testing.T, objs ...corev1.ConfigMap) (*Agent, *fake.Clientset) {
	t.Helper()
	a := bundleAgent(t)
	stageVersion(t, a, fbBundle, 0)
	if err := os.WriteFile(filepath.Join(a.bundleRoot(), "current-bundle"), []byte(fbBundle+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cs := fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "fn"}})
	for i := range objs {
		objs[i].Namespace = a.criuFailedNamespace()
		if _, err := cs.CoreV1().ConfigMaps(objs[i].Namespace).Create(context.Background(), &objs[i], metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	a.kubeClient = cs
	a.config.NodeName = "n0"
	return a, cs
}

// fnKey is the group record key of configuration uri in namespace fn.
func fnKey(t *testing.T, a *Agent) string {
	t.Helper()
	scope, err := a.criuGroupScope(context.Background(), "fn")
	if err != nil {
		t.Fatal(err)
	}
	return criuGroupKey("cache://cfg", scope)
}

func record(name string, data map[string]string) corev1.ConfigMap {
	return corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"app.kubernetes.io/managed-by": "nvsnap"}}, Data: data}
}

func getRecord(t *testing.T, a *Agent, name string) map[string]string {
	t.Helper()
	cm, err := a.kubeClient.CoreV1().ConfigMaps(a.criuFailedNamespace()).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("record %s: %v", name, err)
	}
	return cm.Data
}

// enginePod is rank ordinal of configuration cache://cfg in namespace fn.
func enginePod(ordinal int) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "engine-" + strconv.Itoa(ordinal), Namespace: "fn", UID: types.UID("uid-" + strconv.Itoa(ordinal)),
			Annotations: map[string]string{cacheURIAnnotation: "cache://cfg", modelvolume.CacheOrdinalAnnotation: strconv.Itoa(ordinal)}},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "engine",
			Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("1")}}}}},
	}
}

func withLibrary(p *corev1.Pod) *corev1.Pod {
	p.Spec.Containers[0].Env = append(p.Spec.Containers[0].Env, corev1.EnvVar{Name: "LD_PRELOAD", Value: webhook.GPUShareLibPath})
	return p
}

func crashing(p *corev1.Pod, restarts, exit int32) *corev1.Pod {
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "engine", RestartCount: restarts,
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: exit, Reason: "Error"}},
		State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}}}
	return p
}

func ready(p *corev1.Pod) *corev1.Pod {
	p.Status.Phase = corev1.PodRunning
	p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	return p
}

func TestCRIUFallbackReason(t *testing.T) {
	now := time.Now()
	for name, c := range map[string]struct {
		d      map[string]string
		bundle string
		want   string
	}{
		"complete":                              {map[string]string{"state": "complete"}, fbBundle, ""},
		"capture failed, this bundle":           {map[string]string{"state": "failed", "captures": "1", groupAgentBundleKey: fbBundle}, fbBundle, "failed"},
		"capture failed, another bundle":        {map[string]string{"state": "failed", "captures": "1", groupAgentBundleKey: "old"}, fbBundle, ""},
		"capture failed, no attempts left":      {map[string]string{"state": "failed", "captures": "3", groupAgentBundleKey: "old"}, fbBundle, "failed"},
		"restore failed, captures left":         {map[string]string{"state": "restore-failed", "captures": "1", groupAgentBundleKey: fbBundle}, fbBundle, ""},
		"restore failed, no captures left":      {map[string]string{"state": "restore-failed", "captures": "3", groupAgentBundleKey: fbBundle}, fbBundle, "restore-failed"},
		"library failed, this bundle":           {map[string]string{"state": "gpushare-failed", groupAgentBundleKey: fbBundle}, fbBundle, "gpushare-failed"},
		"library failed, another bundle":        {map[string]string{"state": "gpushare-failed", groupAgentBundleKey: "old"}, fbBundle, ""},
		"bundle unknown":                        {map[string]string{"state": "failed", "captures": "1", groupAgentBundleKey: fbBundle}, "", ""},
		"capturing":                             {map[string]string{"state": "capturing", "startedAt": now.Format(time.RFC3339)}, fbBundle, ""},
		"verifying":                             {map[string]string{"state": "verifying", "sourceNamespace": "fn"}, fbBundle, ""},
		"library failed, no captures left, new": {map[string]string{"state": "gpushare-failed", "captures": "3", groupAgentBundleKey: "old"}, fbBundle, "gpushare-failed"},
	} {
		if got := criuFallbackReason(c.d, now, c.bundle); got != c.want {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
	}
}

// A library failure is never recaptured with CRIU under the same bundle.
func TestCRIUGroupRecapturesGPUShareFailed(t *testing.T) {
	d := map[string]string{"state": "gpushare-failed", groupAgentBundleKey: fbBundle}
	if criuGroupRecaptures(d, time.Now(), fbBundle) {
		t.Error("recaptured under the bundle it failed with")
	}
	if !criuGroupRecaptures(d, time.Now(), "next") {
		t.Error("a new bundle does not get its try")
	}
}

func TestFailedStart(t *testing.T) {
	signal := crashing(enginePod(0), 2, 0)
	signal.Status.ContainerStatuses[0].LastTerminationState.Terminated.Signal = 7
	for name, c := range map[string]struct {
		pod  *corev1.Pod
		want bool
	}{
		"twice, exit 1":      {crashing(enginePod(0), 2, 1), true},
		"twice, by a signal": {signal, true},
		"once":               {crashing(enginePod(0), 1, 1), false},
		"exits 0":            {crashing(enginePod(0), 3, 0), false},
		"ready":              {ready(crashing(enginePod(0), 2, 1)), false},
		"no status":          {enginePod(0), false},
		"no GPU container": {func() *corev1.Pod {
			p := crashing(enginePod(0), 2, 1)
			p.Spec.Containers[0].Resources = corev1.ResourceRequirements{}
			return p
		}(), false},
		"terminated now, once before": {func() *corev1.Pod {
			p := crashing(enginePod(0), 1, 1)
			p.Status.ContainerStatuses[0].State = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 139}}
			return p
		}(), true},
	} {
		if got := failedStart(c.pod) != ""; got != c.want {
			t.Errorf("%s: failed start %v, want %v (%q)", name, got, c.want, failedStart(c.pod))
		}
	}
}

func TestMarkGPUShare(t *testing.T) {
	now := time.Now()
	stale := now.Add(-2 * criuGroupCaptureTimeout).Format(time.RFC3339)
	for name, c := range map[string]struct {
		d        map[string]string
		want     bool
		previous string
	}{
		"no record":           {map[string]string{}, true, ""},
		"capture failed":      {map[string]string{"state": "failed", groupAgentBundleKey: "old"}, true, "failed"},
		"restore failed":      {map[string]string{"state": "restore-failed"}, true, "restore-failed"},
		"complete":            {map[string]string{"state": "complete"}, false, ""},
		"verifying":           {map[string]string{"state": "verifying", "sourceNamespace": "fn"}, false, ""},
		"capturing":           {map[string]string{"state": "capturing", "startedAt": now.Format(time.RFC3339)}, false, ""},
		"capturing, stale":    {map[string]string{"state": "capturing", "startedAt": stale}, true, "failed"},
		"marked, old bundle":  {map[string]string{"state": "gpushare-failed", groupAgentBundleKey: "old", groupGPUSharePreviousKey: "failed"}, true, "failed"},
		"cleared this bundle": {map[string]string{"state": "failed", groupGPUShareClearedKey: fbBundle}, false, ""},
	} {
		got := markGPUShare(c.d, fbBundle, "exit 1", now)
		if got != c.want {
			t.Errorf("%s: marked %v, want %v", name, got, c.want)
			continue
		}
		if !got {
			continue
		}
		if c.d["state"] != "gpushare-failed" || c.d[groupAgentBundleKey] != fbBundle || c.d[groupGPUShareFailureKey] != "exit 1" || c.d[groupGPUSharePreviousKey] != c.previous {
			t.Errorf("%s: record %v", name, c.d)
		}
	}
	// Marked under this bundle already: the failure is updated, the state
	// it replaced kept.
	d := map[string]string{"state": "gpushare-failed", groupAgentBundleKey: fbBundle, groupGPUSharePreviousKey: "restore-failed"}
	if !markGPUShare(d, fbBundle, "again", now) || d[groupGPUSharePreviousKey] != "restore-failed" || d[groupGPUShareFailureKey] != "again" {
		t.Errorf("re-mark: %v", d)
	}
}

func TestClearGPUShare(t *testing.T) {
	d := map[string]string{"state": "gpushare-failed", groupAgentBundleKey: fbBundle, groupGPUSharePreviousKey: "restore-failed"}
	if !clearGPUShare(d, fbBundle, "exit 1") || d["state"] != "restore-failed" || d[groupGPUShareClearedKey] != fbBundle {
		t.Fatalf("clear: %v", d)
	}
	// Created by the mark: what is left is a configuration CRIU does not
	// capture under this bundle, with the library.
	d = map[string]string{"state": "gpushare-failed", groupAgentBundleKey: fbBundle}
	if !clearGPUShare(d, fbBundle, "exit 1") || d["state"] != "failed" || criuFallbackReason(d, time.Now(), fbBundle) != "failed" {
		t.Fatalf("clear a record the mark made: %v", d)
	}
	if markGPUShare(d, fbBundle, "exit 1", time.Now()) {
		t.Error("marked again under the bundle it was cleared under")
	}
	if clearGPUShare(map[string]string{"state": "gpushare-failed", groupAgentBundleKey: "old"}, fbBundle, "x") {
		t.Error("cleared another bundle's mark")
	}
}

// A pod that crashes twice under the library, never Ready, marks its
// configuration and is replaced; its replacement, admitted without it,
// failing the same way clears the mark.
func TestConsiderGPUShareFailure(t *testing.T) {
	a, cs := fallbackAgent(t)
	c := &criuAutoRestorer{a: a}
	ctx := context.Background()
	pod := withLibrary(crashing(enginePod(0), 2, 1))
	if _, err := cs.CoreV1().Pods("fn").Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	c.considerFallback(ctx, pod)
	key := fnKey(t, a)
	d := getRecord(t, a, criuGroupConfigMapName(key))
	if d["state"] != "gpushare-failed" || d[groupAgentBundleKey] != fbBundle || !strings.Contains(d[groupGPUShareFailureKey], "exit code 1") {
		t.Fatalf("record %v", d)
	}
	if _, err := cs.CoreV1().Pods("fn").Get(ctx, pod.Name, metav1.GetOptions{}); err == nil {
		t.Error("the pod was not replaced")
	}
	fb, ok := a.criuFallbackFor(ctx, "fn", "cache://cfg", 0)
	if !ok || fb.Reason != webhook.FallbackGPUShareFailed {
		t.Fatalf("fallback %+v %v", fb, ok)
	}

	next := crashing(enginePod(0), 2, 1)
	next.UID = "uid-next"
	next.Annotations[webhook.FallbackReasonAnnotation] = webhook.FallbackGPUShareFailed
	c.considerFallback(ctx, next)
	d = getRecord(t, a, criuGroupConfigMapName(key))
	if d["state"] != "failed" || d[groupGPUShareClearedKey] != fbBundle {
		t.Fatalf("not cleared: %v", d)
	}
}

// A pod seen Ready, a placeholder, or a pod without the library is never
// taken for a failed start under it.
func TestConsiderGPUShareFailureLeavesOthers(t *testing.T) {
	ctx := context.Background()
	for name, setup := range map[string]func(c *criuAutoRestorer) *corev1.Pod{
		"seen ready": func(c *criuAutoRestorer) *corev1.Pod {
			p := withLibrary(enginePod(0))
			c.considerFallback(ctx, ready(p.DeepCopy()))
			return crashing(p, 2, 1)
		},
		"placeholder": func(*criuAutoRestorer) *corev1.Pod {
			p := withLibrary(crashing(enginePod(0), 2, 1))
			p.Annotations[webhook.CRIURestoreAnnotation] = "ckpt"
			return p
		},
		"no library": func(*criuAutoRestorer) *corev1.Pod { return crashing(enginePod(0), 2, 1) },
		"usable record": func(c *criuAutoRestorer) *corev1.Pod {
			rec := record(criuGroupConfigMapName(fnKey(t, c.a)), map[string]string{"state": "complete"})
			if _, err := c.a.kubeClient.CoreV1().ConfigMaps(c.a.criuFailedNamespace()).Create(ctx, &rec, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			return withLibrary(crashing(enginePod(0), 2, 1))
		},
	} {
		a, _ := fallbackAgent(t)
		c := &criuAutoRestorer{a: a}
		c.considerFallback(ctx, setup(c))
		cm, err := a.kubeClient.CoreV1().ConfigMaps(a.criuFailedNamespace()).Get(ctx, criuGroupConfigMapName(fnKey(t, a)), metav1.GetOptions{})
		if err == nil && cm.Data["state"] == "gpushare-failed" {
			t.Errorf("%s: marked", name)
		}
	}
}

// The webhook finds the reason through a rank's alias and the capture of
// the pod's own rank only once it is complete.
func TestCRIUFallbackFor(t *testing.T) {
	a, _ := fallbackAgent(t)
	own := fnKey(t, a)
	for _, r := range []corev1.ConfigMap{
		record(criuGroupConfigMapName(own), map[string]string{"state": "alias", "alias": "inst"}),
		record(criuGroupConfigMapName("inst"), map[string]string{"state": "failed", "captures": "1", groupAgentBundleKey: fbBundle}),
		record(cachedirRecordName(own), map[string]string{"rank.0": "complete", "rank.1": "capturing"}),
	} {
		if _, err := a.kubeClient.CoreV1().ConfigMaps(a.criuFailedNamespace()).Create(context.Background(), &r, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	fb, ok := a.criuFallbackFor(ctx, "fn", "cache://cfg", 0)
	if !ok || fb.Reason != "failed" || fb.Capture != webhook.FallbackCaptureID(own, 0) {
		t.Fatalf("rank 0: %+v %v", fb, ok)
	}
	if fb, ok := a.criuFallbackFor(ctx, "fn", "cache://cfg", 1); !ok || fb.Capture != "" {
		t.Fatalf("rank 1 is still capturing: %+v %v", fb, ok)
	}
	if _, ok := a.criuFallbackFor(ctx, "fn", "cache://other", 0); ok {
		t.Fatal("a configuration without a record falls back")
	}
}

func TestClaimCachedirRank(t *testing.T) {
	a, _ := fallbackAgent(t)
	c := &criuAutoRestorer{a: a}
	ctx := context.Background()
	now := time.Now()
	if ok, err := c.claimCachedirRank(ctx, "k", 0, now); err != nil || !ok {
		t.Fatalf("first claim: %v %v", ok, err)
	}
	if ok, _ := c.claimCachedirRank(ctx, "k", 0, now); ok {
		t.Fatal("a rank being captured was claimed again")
	}
	if ok, err := c.claimCachedirRank(ctx, "k", 1, now); err != nil || !ok {
		t.Fatalf("another rank: %v %v", ok, err)
	}
	if ok, _ := c.claimCachedirRank(ctx, "k", 0, now.Add(cachedirCaptureStale+time.Minute)); !ok {
		t.Fatal("a stale capture was not taken over")
	}
	if err := c.endCachedirRank(ctx, "k", 0, 42, true); err != nil {
		t.Fatal(err)
	}
	d := getRecord(t, a, cachedirRecordName("k"))
	if d["rank.0"] != "complete" || d["bytes.0"] != "42" || d["node.0"] != "n0" {
		t.Fatalf("record %v", d)
	}
	if ok, _ := c.claimCachedirRank(ctx, "k", 0, now.Add(time.Hour)); ok {
		t.Fatal("a complete rank was claimed")
	}
	if err := c.endCachedirRank(ctx, "k", 1, 0, false); err != nil {
		t.Fatal(err)
	}
	if d := getRecord(t, a, cachedirRecordName("k")); d["rank.1"] != "" {
		t.Fatalf("a failed capture kept its rank: %v", d)
	}
}

// cacheTree is a cachedir as an engine leaves it.
func cacheTree(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	for p, body := range map[string]string{"cache/triton/k.so": "kernel", "cache/inductor/a.py": "code", "model/blobs/b1": "weights"} {
		if err := os.MkdirAll(filepath.Join(src, filepath.Dir(p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(src, p), []byte(body), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(src, "cache", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src, "model", "snapshots"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../blobs/b1", filepath.Join(src, "model", "snapshots", "w")); err != nil {
		t.Fatal(err)
	}
	return src
}

// A capture holds the tree with its modes and a manifest; a copy of only
// its regular files, as the HTTP peers deliver one, is made whole again.
func TestCachedirCaptureRoundTrip(t *testing.T) {
	a := bundleAgent(t)
	src := cacheTree(t)
	id := webhook.FallbackCaptureID("k", 0)
	man, err := a.writeCachedirCapture(src, id)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(a.config.CheckpointDir, id)
	if len(man.Files) != 3 || man.Bytes != int64(len("kernel")+len("code")+len("weights")) || man.Links["model/snapshots/w"] != "../blobs/b1" {
		t.Fatalf("manifest %+v", man)
	}
	if st, err := os.Stat(filepath.Join(dir, webhook.FallbackCaptureTree, "cache", "triton", "k.so")); err != nil || st.Mode().Perm() != 0o640 {
		t.Fatalf("copy: %v %v", st, err)
	}
	if !localCheckpointComplete(a.config.CheckpointDir, id) {
		t.Fatal("a written capture is not complete")
	}

	// What a fetch from another node lands: the files and the manifest.
	fetched := filepath.Join(t.TempDir(), id)
	for _, f := range append(man.Files, cachedirFile{Path: "../" + cachedirManifestFile}) {
		from := filepath.Join(dir, webhook.FallbackCaptureTree, f.Path)
		to := filepath.Join(fetched, webhook.FallbackCaptureTree, f.Path)
		b, err := os.ReadFile(from)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(to, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := completeCachedirCapture(fetched); err != nil {
		t.Fatal(err)
	}
	// Links live in the manifest only, never in the directory the peers
	// serve; the seed makes them in the pod.
	for _, d := range []string{dir, fetched} {
		if _, err := os.Lstat(filepath.Join(d, webhook.FallbackCaptureTree, "model", "snapshots", "w")); err == nil {
			t.Errorf("a link was made in %s", d)
		}
	}
	if st, err := os.Stat(filepath.Join(fetched, webhook.FallbackCaptureTree, "cache", "empty")); err != nil || !st.IsDir() {
		t.Fatal("empty directory not made")
	}
	// A truncated file fails the check.
	if err := os.WriteFile(filepath.Join(fetched, webhook.FallbackCaptureTree, "cache", "inductor", "a.py"), []byte("c"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := completeCachedirCapture(fetched); err == nil {
		t.Fatal("a truncated capture passed")
	}
}

// A manifest naming a path out of its tree is refused.
func TestCompleteCachedirCaptureConfined(t *testing.T) {
	for _, man := range []string{`{"files":[{"path":"../../etc/passwd","size":1}]}`, `{"files":[],"dirs":["../../escape"]}`} {
		dir := filepath.Join(t.TempDir(), webhook.FallbackCaptureID("k", 0))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, cachedirManifestFile), []byte(man), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := completeCachedirCapture(dir); err == nil {
			t.Errorf("%s: a path out of the tree was used", man)
		}
	}
}

// The capture reads the pod's cachedir through an os.Root: a link the pod
// planted to a host file is recorded as a link, never followed.
func TestCachedirCaptureDoesNotFollowLinks(t *testing.T) {
	a := bundleAgent(t)
	src := t.TempDir()
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("host"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(src, "steal")); err != nil {
		t.Fatal(err)
	}
	man, err := a.writeCachedirCapture(src, webhook.FallbackCaptureID("k", 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(man.Files) != 0 || man.Links["steal"] != secret {
		t.Fatalf("manifest %+v", man)
	}
}

// A Ready fallback pod's rank is captured once, recorded complete and
// marked ready on its node.
func TestCaptureFallbackRank(t *testing.T) {
	a, _ := fallbackAgent(t)
	c := &criuAutoRestorer{a: a}
	pods := t.TempDir()
	defer func(w, s time.Duration, r string) { cachedirWarmup, cachedirSettle, cachedirPodsRoot = w, s, r }(cachedirWarmup, cachedirSettle, cachedirPodsRoot)
	cachedirWarmup, cachedirSettle, cachedirPodsRoot = 0, 0, pods
	pod := ready(enginePod(1))
	pod.Annotations[webhook.FallbackReasonAnnotation] = "failed"
	pod.Spec.Volumes = []corev1.Volume{{Name: webhook.CacheDirVolumeName, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}
	vol := filepath.Join(pods, string(pod.UID), "volumes", "kubernetes.io~empty-dir", webhook.CacheDirVolumeName)
	if err := os.MkdirAll(filepath.Dir(vol), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(cacheTree(t), vol); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if retry, err := c.captureFallbackRank(ctx, pod); err != nil || retry {
		t.Fatalf("capture: %v retry %v", err, retry)
	}
	key := fnKey(t, a)
	id := webhook.FallbackCaptureID(key, 1)
	if d := getRecord(t, a, cachedirRecordName(key)); d["rank.1"] != "complete" || d["node.1"] != "n0" {
		t.Fatalf("record %v", d)
	}
	if _, err := os.Stat(filepath.Join(a.cachedirReadyDir(id), webhook.FallbackReadyFile)); err != nil {
		t.Fatal("not marked ready on its node")
	}
	// A second pod of the rank is not captured again.
	other := pod.DeepCopy()
	other.UID = "uid-other"
	if retry, err := c.captureFallbackRank(ctx, other); err != nil || retry {
		t.Fatalf("second: %v %v", err, retry)
	}
}

// A pod seeded from a capture fetches only its own rank's.
func TestConsiderFallbackFetchOwnRankOnly(t *testing.T) {
	a, _ := fallbackAgent(t)
	a.log = logrus.New()
	c := &criuAutoRestorer{a: a}
	pod := enginePod(0)
	pod.Annotations[webhook.FallbackCaptureAnnotation] = webhook.FallbackCaptureID("another-tenant", 0)
	c.considerFallbackFetch(context.Background(), pod)
	if _, err := os.Stat(filepath.Join(a.config.CheckpointDir, pod.Annotations[webhook.FallbackCaptureAnnotation])); err == nil {
		t.Fatal("fetched another configuration's capture")
	}
}

// Captures no record names, past the retire age and seeding no pod here,
// are retired; a record whose configuration CRIU serves again goes first.
func TestRetirableCachedirCaptures(t *testing.T) {
	a, _ := fallbackAgent(t,
		record(criuGroupConfigMapName("live"), map[string]string{"state": "failed"}),
		record(cachedirRecordName("live"), map[string]string{"rank.0": "complete"}),
		record(criuGroupConfigMapName("served"), map[string]string{"state": "complete"}),
		record(cachedirRecordName("served"), map[string]string{"rank.0": "complete"}),
	)
	old := time.Now().Add(-2 * checkpointRetireAge)
	for _, id := range []string{webhook.FallbackCaptureID("live", 0), webhook.FallbackCaptureID("served", 0),
		webhook.FallbackCaptureID("gone", 0), webhook.FallbackCaptureID("seeding", 0), webhook.FallbackCaptureID("young", 0)} {
		dir := filepath.Join(a.config.CheckpointDir, id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(id, "young") {
			if err := os.Chtimes(dir, old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	seeding := enginePod(0)
	seeding.Annotations[webhook.FallbackCaptureAnnotation] = webhook.FallbackCaptureID("seeding", 0)
	got, err := a.retirableCachedirCaptures(context.Background(), []corev1.Pod{*seeding}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{webhook.FallbackCaptureID("served", 0): true, webhook.FallbackCaptureID("gone", 0): true}
	if len(got) != len(want) {
		t.Fatalf("retired %v", got)
	}
	for _, id := range got {
		if !want[id] {
			t.Errorf("retired %s", id)
		}
	}
	if _, err := a.kubeClient.CoreV1().ConfigMaps(a.criuFailedNamespace()).Get(context.Background(), cachedirRecordName("served"), metav1.GetOptions{}); err == nil {
		t.Error("the record of a configuration CRIU serves was kept")
	}
}

// A fallback pod is never taken for a CRIU capture: its placeholder
// would not have its cachedir.
func TestGroupCaptureSkipsFallbackPods(t *testing.T) {
	a, _ := fallbackAgent(t)
	a.config.CaptureOptIn = []CaptureOptIn{{Label: "function-id"}}
	c := &criuAutoRestorer{a: a}
	pod := ready(enginePod(0))
	pod.Labels = map[string]string{"function-id": "f"}
	pod.Annotations[webhook.FallbackReasonAnnotation] = "failed"
	c.considerGroupCapture(context.Background(), pod)
	if _, err := a.kubeClient.CoreV1().ConfigMaps(a.criuFailedNamespace()).Get(context.Background(), criuGroupConfigMapName(fnKey(t, a)), metav1.GetOptions{}); err == nil {
		t.Error("a fallback pod claimed a CRIU capture")
	}
}

// A deleted pod leaves nothing behind in the agent's memory.
func TestForgetFallback(t *testing.T) {
	a, _ := fallbackAgent(t)
	c := &criuAutoRestorer{a: a}
	pod := ready(enginePod(0))
	c.considerFallback(context.Background(), pod)
	for _, p := range fallbackAttempts {
		c.attempted.Store(p+string(pod.UID), struct{}{})
	}
	c.forgetPod(pod)
	if _, ok := c.seenReady.Load(pod.UID); ok {
		t.Error("seen Ready kept")
	}
	for _, p := range fallbackAttempts {
		if _, ok := c.attempted.Load(p + string(pod.UID)); ok {
			t.Errorf("%s kept", p)
		}
	}
}
