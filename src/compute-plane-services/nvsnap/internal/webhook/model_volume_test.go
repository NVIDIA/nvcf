// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"context"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/election"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/modelvolume"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/rootfsonly"
)

func mvMutator(t *testing.T, mode modelvolume.Mode, role election.Role, kc *fake.Clientset) (*Mutator, *fakeElector) {
	t.Helper()
	m, el := mvMutatorReader(t, mode, modelvolume.ReaderHostPath, role, kc)
	return m, el
}

func mvMutatorReader(t *testing.T, mode modelvolume.Mode, reader modelvolume.ReaderMode, role election.Role, kc *fake.Clientset) (*Mutator, *fakeElector) {
	t.Helper()
	el := &fakeElector{role: role}
	tx, _ := checkpointstore.LookupVolumeHandleTransform("nvmesh")
	return &Mutator{
		Backend:        newBackend(t),
		CacheDir:       "/opt/nvsnap",
		Composer:       &rootfsonly.HashInputComposer{CUDADriverMajor: 580},
		Elector:        el,
		ModelVolume:    &modelvolume.Provisioner{Kube: kc, Cfg: modelvolume.Config{Mode: mode, StorageClass: "sc", Size: resource.MustParse("512Gi"), Reader: reader}},
		ReadOnlyMinter: &checkpointstore.SharedVolumePromoter{KubeClient: kc, StorageClass: "sc", Transform: tx, Log: logrus.New()},
	}, el
}

// The prd11 function shape: NGC init download into an emptyDir, engine
// from a positional MODEL_PATH, two pods per instance.
func ngcFunctionPod() *corev1.Pod {
	gpu := corev1.ResourceRequirements{Limits: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("4")}}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "sr-fn", GenerateName: "mini-service-kimi-k3-"},
		Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{{
				Name: "download-ngc-model", Image: "nvcr.io/org/ultra:vllm", Command: []string{"/bin/bash", "-c"},
				Args:         []string{"set -euo pipefail\nngc registry model download-version --dest \"${NGC_MODEL_MOUNT}\" \"${NGC_MODEL_NAME}\"\n"},
				Env:          []corev1.EnvVar{{Name: "NGC_MODEL_NAME", Value: "org/team/nemotron3-ultra-genrm:bf16-fixed"}, {Name: "NGC_MODEL_MOUNT", Value: "/config/models"}},
				VolumeMounts: []corev1.VolumeMount{{Name: "ngc-models", MountPath: "/config/models"}, {Name: "secrets", MountPath: "/var/secrets", ReadOnly: true}, {Name: "scripts", MountPath: "/opt/kimi-k3"}},
			}},
			Containers: []corev1.Container{{
				Name: "kimi-k3", Image: "nvcr.io/org/ultra:vllm", Command: []string{"/bin/bash", "/opt/kimi-k3/start.sh"},
				Env:          []corev1.EnvVar{{Name: "MODEL_PATH", Value: "/config/models/nemotron3-ultra-genrm"}, {Name: "HF_HUB_OFFLINE", Value: "1"}},
				Resources:    gpu,
				VolumeMounts: []corev1.VolumeMount{{Name: "dshm", MountPath: "/dev/shm"}, {Name: "ngc-models", MountPath: "/config/models"}},
			}},
			Volumes: []corev1.Volume{
				{Name: "dshm", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
				{Name: "ngc-models", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
				{Name: "secrets", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "function-secrets"}}},
				{Name: "scripts", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "kimi-k3-scripts"}}}},
			},
		},
	}
}

func stockVLLMPod() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "fn", GenerateName: "vllm-"},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "vllm", Image: "vllm/vllm-openai:v0.20.0", Command: []string{"/bin/bash", "-lc"},
			Args:      []string{"vllm serve --model Qwen/Qwen2.5-32B-Instruct --tensor-parallel-size 4"},
			Env:       []corev1.EnvVar{{Name: "HF_TOKEN", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{Key: "token"}}}},
			Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("4")}},
		}}},
	}
}

type mvView struct {
	labels, annotations map[string]string
	volumes             map[string]corev1.Volume // by name, from replace/add ops
	roMounts            []string
	initScripts         map[string]string
	newInits            []corev1.Container
	env                 map[string]string
}

func viewMV(pod *corev1.Pod, patches []PatchOp) mvView {
	v := mvView{labels: map[string]string{}, annotations: map[string]string{}, volumes: map[string]corev1.Volume{}, initScripts: map[string]string{}, env: map[string]string{}}
	for _, p := range patches {
		switch {
		case strings.HasPrefix(p.Path, "/metadata/labels/"):
			v.labels[strings.ReplaceAll(strings.TrimPrefix(p.Path, "/metadata/labels/"), "~1", "/")] = p.Value.(string)
		case strings.HasPrefix(p.Path, "/metadata/annotations/"):
			v.annotations[strings.ReplaceAll(strings.TrimPrefix(p.Path, "/metadata/annotations/"), "~1", "/")] = p.Value.(string)
		case strings.HasSuffix(p.Path, "/readOnly") && p.Value == true:
			v.roMounts = append(v.roMounts, p.Path)
		case strings.HasPrefix(p.Path, "/spec/initContainers/") && strings.HasSuffix(p.Path, "/args"):
			idx := strings.Split(strings.TrimPrefix(p.Path, "/spec/initContainers/"), "/")[0]
			v.initScripts[pod.Spec.InitContainers[atoi(idx)].Name] = p.Value.([]string)[0]
		}
		switch val := p.Value.(type) {
		case corev1.Volume:
			v.volumes[val.Name] = val
		case corev1.Container:
			if strings.HasPrefix(p.Path, "/spec/initContainers") {
				v.newInits = append(v.newInits, val)
			}
		case corev1.EnvVar:
			v.env[val.Name] = val.Value
		}
	}
	return v
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

func TestModelVolume_FirstPodBlock_NGCInit_CreatesJob(t *testing.T) {
	kc := fake.NewSimpleClientset()
	m, el := mvMutator(t, modelvolume.ModeBlock, election.RoleLeader, kc)
	pod := ngcFunctionPod()
	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatal(err)
	}
	v := viewMV(pod, patches)
	uri := "ngc://org/team/nemotron3-ultra-genrm:bf16-fixed"
	if el.called != 0 {
		t.Error("the download step is a Job; no election runs")
	}
	if v.annotations[modelvolume.IdentityAnnotation] != uri || v.labels[modelvolume.RoleLabel] != "reader" || v.labels[modelvolume.PendingLabel] != "true" {
		t.Errorf("every pod is a reader: ann=%v labels=%v", v.annotations, v.labels)
	}
	// Block mode: no claim is created at admission. The size of the model
	// is unknown until it has been downloaded, and not every storage
	// system can grow a volume, so the Job stages into an emptyDir and the
	// agent sizes the claim from what landed (the cachedir shape).
	if claims, _ := kc.CoreV1().PersistentVolumeClaims("").List(context.Background(), metav1.ListOptions{}); len(claims.Items) != 0 {
		t.Errorf("block mode creates no claim at admission, got %d", len(claims.Items))
	}
	job, err := kc.BatchV1().Jobs("sr-fn").Get(context.Background(), modelvolume.JobName(uri), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("download Job must be created: %v", err)
	}
	// The download is an init container; a hold container keeps the pod,
	// and with it the staged emptyDir, alive until the agent has copied.
	if len(job.Spec.Template.Spec.InitContainers) != 1 || len(job.Spec.Template.Spec.Containers) != 1 || job.Spec.Template.Spec.Containers[0].Name != modelvolume.HoldContainer {
		t.Fatalf("staging pod shape: inits=%d containers=%+v", len(job.Spec.Template.Spec.InitContainers), job.Spec.Template.Spec.Containers)
	}
	jc := job.Spec.Template.Spec.InitContainers[0]
	script := jc.Args[0]
	if jc.Image != "nvcr.io/org/ultra:vllm" || !strings.Contains(script, "ngc registry model download-version") || !strings.Contains(script, "touch /config/models/.nvsnap-complete") {
		t.Errorf("job must run the chart's own download then touch the marker:\n%s", script)
	}
	landing := job.Spec.Template.Spec.Volumes[0]
	if len(jc.Env) != 2 || jc.VolumeMounts[0].MountPath != "/config/models" || landing.EmptyDir == nil || landing.Name != "ngc-models" || job.Annotations[modelvolume.StagingAnnotation] != "ngc-models" {
		t.Errorf("job must carry the init's env and stage into an emptyDir at the init's path, named by the staging annotation: env=%v mounts=%v landing=%+v ann=%v", jc.Env, jc.VolumeMounts, landing, job.Annotations)
	}
	if job.Spec.ActiveDeadlineSeconds == nil || *job.Spec.ActiveDeadlineSeconds < 3600 {
		t.Error("a staging Job carries an active deadline as the safety net for an agent that never copies")
	}
	// The NGC key lives in a secret file and the init's helper scripts in
	// a ConfigMap; the Job must mount both or the download cannot run.
	jobVols := map[string]corev1.Volume{}
	for _, vol := range job.Spec.Template.Spec.Volumes {
		jobVols[vol.Name] = vol
	}
	if len(jc.VolumeMounts) != 3 || jobVols["secrets"].Secret == nil || jobVols["secrets"].Secret.SecretName != "function-secrets" || jobVols["scripts"].ConfigMap == nil {
		t.Errorf("job must carry every volume the init mounted, only the landing one redirected: mounts=%v volumes=%v", jc.VolumeMounts, jobVols)
	}
	if _, dshm := jobVols["dshm"]; dshm || len(jobVols) != 3 {
		t.Errorf("volumes the init does not mount stay behind: %v", jobVols)
	}
	ps := job.Spec.Template.Spec
	if jc.Resources.Requests.Cpu().IsZero() || jc.Resources.Limits.Memory().IsZero() || ps.SecurityContext == nil || ps.SecurityContext.SeccompProfile == nil ||
		jc.SecurityContext == nil || jc.SecurityContext.AllowPrivilegeEscalation == nil || *jc.SecurityContext.AllowPrivilegeEscalation || jc.SecurityContext.Capabilities == nil ||
		ps.AutomountServiceAccountToken == nil || *ps.AutomountServiceAccountToken {
		t.Errorf("job pod must satisfy the function-namespace baselines (requests/limits, seccomp, no escalation, dropped caps, no SA token): %+v %+v", jc.Resources, ps.SecurityContext)
	}
	s := v.initScripts["download-ngc-model"]
	if !strings.Contains(s, "while [ ! -f /config/models/.nvsnap-complete ]") {
		t.Errorf("the pod's own init becomes a wait:\n%s", s)
	}
	if v.env["TORCHINDUCTOR_CACHE_DIR"] != "/opt/nvsnap/cache/torchinductor" || v.env["HF_HOME"] != "" {
		t.Errorf("Block mode: compile caches to the local cachedir, model env untouched: %v", v.env)
	}
}

func TestModelVolume_ReaderBlock_PendingBind(t *testing.T) {
	kc := fake.NewSimpleClientset()
	m, _ := mvMutator(t, modelvolume.ModeBlock, election.RoleFollower, kc)
	pod := ngcFunctionPod()
	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatal(err)
	}
	v := viewMV(pod, patches)
	if v.labels[modelvolume.RoleLabel] != "reader" || v.labels[modelvolume.PendingLabel] != "true" || v.annotations[modelvolume.LandingAnnotation] != "/config/models" {
		t.Errorf("Block reader stamp: %v %v", v.labels, v.annotations)
	}
	vol, replaced := v.volumes["ngc-models"]
	if !replaced || vol.HostPath == nil || vol.HostPath.Path != "/var/lib/containerd/nvsnap-models/"+modelvolume.Key("ngc://org/team/nemotron3-ultra-genrm:bf16-fixed") || *vol.HostPath.Type != corev1.HostPathDirectoryOrCreate {
		t.Errorf("Block reader lands on a hostPath under the model host root for the agent to bind into, got %+v", vol)
	}
	var propagations int
	for _, p := range patches {
		if strings.HasSuffix(p.Path, "/mountPropagation") && p.Value == corev1.MountPropagationHostToContainer {
			propagations++
		}
	}
	if propagations != 2 {
		t.Errorf("engine and download init mounts must propagate host mounts in, got %d", propagations)
	}
	s := v.initScripts["download-ngc-model"]
	if !strings.Contains(s, "while [ ! -f /config/models/.nvsnap-complete ]") || !strings.Contains(s, "ngc registry model download-version") || !strings.Contains(s, "deadline passed") {
		t.Errorf("reader init must wait for the marker and fall back to its own download:\n%s", s)
	}
	for _, p := range patches {
		if strings.HasPrefix(p.Path, "/spec/schedulingGates") {
			t.Fatal("no pod is ever gated on the model volume path")
		}
	}
}

// Default reader mode on block storage: no hostPath (Kyverno's
// disallow-host-path rejects it in function namespaces); the pod
// references the read-only claim in its namespace and waits on binding.
func TestModelVolume_ReaderBlockPVC_ReferencesReadOnlyClaim(t *testing.T) {
	kc := fake.NewSimpleClientset()
	m, _ := mvMutatorReader(t, modelvolume.ModeBlock, "", election.RoleFollower, kc)
	pod := ngcFunctionPod()
	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatal(err)
	}
	v := viewMV(pod, patches)
	uri := "ngc://org/team/nemotron3-ultra-genrm:bf16-fixed"
	if v.labels[modelvolume.RoleLabel] != "reader" || v.labels[modelvolume.PendingLabel] != "true" {
		t.Errorf("PVC reader is pending until the agent mints its claim: %v", v.labels)
	}
	vol, replaced := v.volumes["ngc-models"]
	if !replaced || vol.PersistentVolumeClaim == nil || vol.PersistentVolumeClaim.ClaimName != modelvolume.ReadOnlyClaimName(uri) {
		t.Errorf("landing volume becomes the read-only claim, got %+v", vol)
	}
	for _, p := range patches {
		if strings.HasSuffix(p.Path, "/mountPropagation") {
			t.Error("no mount propagation in PVC mode")
		}
		if vv, ok := p.Value.(corev1.Volume); ok && vv.HostPath != nil {
			t.Error("no hostPath in PVC mode")
		}
	}
	if _, err := kc.CoreV1().PersistentVolumeClaims("sr-fn").Get(context.Background(), modelvolume.ReadOnlyClaimName(uri), metav1.GetOptions{}); err == nil {
		t.Error("the read-only claim is minted by the agent after completion, not at admission of an incomplete volume")
	}
	if s := v.initScripts["download-ngc-model"]; !strings.Contains(s, "while [ ! -f /config/models/.nvsnap-complete ]") {
		t.Errorf("reader init still waits for the marker on the bound claim:\n%s", s)
	}
}

// Volume already complete: the webhook mints the read-only claim in the
// pod namespace itself so the pod binds immediately, and it is not pending.
// Engine-download chart in PVC mode: the injected init carries no
// mount propagation (nothing arrives from the host) and satisfies the
// baselines; the Job container gets requests and limits.
func TestModelVolume_ReaderBlockPVC_InjectedInitHardened(t *testing.T) {
	kc := fake.NewSimpleClientset()
	m, _ := mvMutatorReader(t, modelvolume.ModeBlock, modelvolume.ReaderPVC, election.RoleFollower, kc)
	pod := stockVLLMPod()
	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatal(err)
	}
	v := viewMV(pod, patches)
	var init corev1.Container
	for _, c := range v.newInits {
		if c.Name == injectedDownloadInit {
			init = c
		}
	}
	if init.Name == "" {
		t.Fatalf("injected download init expected, got %v", v.newInits)
	}
	for _, vm := range init.VolumeMounts {
		if vm.MountPropagation != nil {
			t.Errorf("no mount propagation in PVC mode: %+v", vm)
		}
	}
	if init.Resources.Limits.Memory().IsZero() || init.SecurityContext == nil || init.SecurityContext.Capabilities == nil {
		t.Errorf("injected init must carry limits and a security context: %+v %+v", init.Resources, init.SecurityContext)
	}
	uri := "hf://Qwen/Qwen2.5-32B-Instruct"
	job, err := kc.BatchV1().Jobs(pod.Namespace).Get(context.Background(), modelvolume.JobName(uri), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("download Job: %v", err)
	}
	if jc := job.Spec.Template.Spec.Containers[0]; jc.Resources.Limits.Cpu().IsZero() || jc.Resources.Requests.Memory().IsZero() {
		t.Errorf("engine-download Job container needs requests and limits: %+v", jc.Resources)
	}
}

// The chart runs its engine as a fixed non-root user with an fsGroup.
// The download Job and the injected init inherit that posture: the
// volume is written by the user that reads it, and runAsNonRoot holds.
func TestModelVolume_InheritsChartUserPosture(t *testing.T) {
	kc := fake.NewSimpleClientset()
	m, _ := mvMutatorReader(t, modelvolume.ModeBlock, modelvolume.ReaderPVC, election.RoleFollower, kc)
	pod := stockVLLMPod()
	uid, gid, nonRoot := int64(1000), int64(2000), true
	pod.Spec.SecurityContext = &corev1.PodSecurityContext{FSGroup: &gid}
	pod.Spec.Containers[0].SecurityContext = &corev1.SecurityContext{RunAsNonRoot: &nonRoot, RunAsUser: &uid, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}}
	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatal(err)
	}
	v := viewMV(pod, patches)
	isc := v.newInits[0].SecurityContext
	if isc.RunAsNonRoot == nil || !*isc.RunAsNonRoot || isc.RunAsUser == nil || *isc.RunAsUser != uid || isc.SeccompProfile == nil {
		t.Errorf("injected init inherits the engine's user posture: %+v", isc)
	}
	job, err := kc.BatchV1().Jobs(pod.Namespace).Get(context.Background(), modelvolume.JobName("hf://Qwen/Qwen2.5-32B-Instruct"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ps := job.Spec.Template.Spec
	jsc := ps.Containers[0].SecurityContext
	if jsc.RunAsNonRoot == nil || !*jsc.RunAsNonRoot || jsc.RunAsUser == nil || *jsc.RunAsUser != uid {
		t.Errorf("Job container inherits the engine's user posture: %+v", jsc)
	}
	if ps.SecurityContext == nil || ps.SecurityContext.FSGroup == nil || *ps.SecurityContext.FSGroup != gid || ps.SecurityContext.SeccompProfile == nil {
		t.Errorf("Job pod inherits the pod security context plus seccomp: %+v", ps.SecurityContext)
	}
	var drops []corev1.Capability
	if jsc.Capabilities != nil {
		drops = jsc.Capabilities.Drop
	}
	if len(drops) != 2 || drops[1] != "NET_RAW" {
		t.Errorf("NET_RAW is dropped by name: %v", drops)
	}
}

func TestModelVolume_ReaderBlockPVC_CompleteMintsAtAdmission(t *testing.T) {
	uri := "ngc://org/team/nemotron3-ultra-genrm:bf16-fixed"
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "pvc-done", Labels: map[string]string{modelvolume.IdentityLabel: modelvolume.Key(uri), modelvolume.CompleteLabel: "true", "app.kubernetes.io/managed-by": "nvsnap"}},
		Spec: corev1.PersistentVolumeSpec{
			Capacity:                      corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("512Gi")},
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			PersistentVolumeSource:        corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{Driver: "nvmesh-csi.excelero.com", VolumeHandle: "cluster:csi-done:vol:sr-fn"}},
		},
	}
	kc := fake.NewSimpleClientset(pv)
	m, _ := mvMutatorReader(t, modelvolume.ModeBlock, modelvolume.ReaderPVC, election.RoleFollower, kc)
	pod := ngcFunctionPod()
	pod.Namespace = "sr-other"
	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatal(err)
	}
	v := viewMV(pod, patches)
	if _, pending := v.labels[modelvolume.PendingLabel]; pending {
		t.Errorf("complete volume: the reader is not pending, labels %v", v.labels)
	}
	ro, err := kc.CoreV1().PersistentVolumeClaims("sr-other").Get(context.Background(), modelvolume.ReadOnlyClaimName(uri), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("read-only claim must be minted in the pod namespace at admission: %v", err)
	}
	roPV, _ := kc.CoreV1().PersistentVolumes().Get(context.Background(), ro.Spec.VolumeName, metav1.GetOptions{})
	if roPV.Spec.CSI.VolumeHandle != "cluster:csi-done:vol:sr-other" {
		t.Errorf("handle rewritten to the reader namespace: %+v", roPV.Spec.CSI)
	}
	if _, err := kc.BatchV1().Jobs("sr-other").Get(context.Background(), modelvolume.JobName(uri), metav1.GetOptions{}); err == nil {
		t.Error("no download Job for a complete volume")
	}
}

func TestModelVolume_ReaderRWX_SharesClaim(t *testing.T) {
	kc := fake.NewSimpleClientset()
	m, _ := mvMutator(t, modelvolume.ModeRWX, election.RoleFollower, kc)
	pod := ngcFunctionPod()
	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatal(err)
	}
	v := viewMV(pod, patches)
	uri := "ngc://org/team/nemotron3-ultra-genrm:bf16-fixed"
	vol := v.volumes["ngc-models"]
	if vol.PersistentVolumeClaim == nil || vol.PersistentVolumeClaim.ClaimName != modelvolume.ClaimName(uri) {
		t.Errorf("RWX reader mounts the shared claim, got %+v", vol)
	}
	if pvc, err := kc.CoreV1().PersistentVolumeClaims("sr-fn").Get(context.Background(), modelvolume.ClaimName(uri), metav1.GetOptions{}); err != nil || pvc.Spec.AccessModes[0] != corev1.ReadWriteMany {
		t.Errorf("shared claim must be RWX: %v", err)
	}
	if v.labels[modelvolume.PendingLabel] != "" {
		t.Error("RWX readers are not pending; the filesystem delivers the marker")
	}
	if !strings.HasPrefix(v.env["TORCHINDUCTOR_CACHE_DIR"], "/config/models/.nvsnap/cache/") || !strings.HasSuffix(v.env["TORCHINDUCTOR_CACHE_DIR"], "/torchinductor") {
		t.Errorf("RWX mode: compile caches live in the shared volume under a config key, got %q", v.env["TORCHINDUCTOR_CACHE_DIR"])
	}
}

func TestModelVolume_EngineDownload_JobRunsHF(t *testing.T) {
	kc := fake.NewSimpleClientset()
	m, _ := mvMutator(t, modelvolume.ModeRWX, election.RoleLeader, kc)
	pod := stockVLLMPod()
	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatal(err)
	}
	v := viewMV(pod, patches)
	uri := "hf://Qwen/Qwen2.5-32B-Instruct"
	job, err := kc.BatchV1().Jobs("fn").Get(context.Background(), modelvolume.JobName(uri), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("engine-download needs a download Job: %v", err)
	}
	jc := job.Spec.Template.Spec.Containers[0]
	s := jc.Args[0]
	if jc.Image != pod.Spec.Containers[0].Image || !strings.Contains(s, "hf download Qwen/Qwen2.5-32B-Instruct") || !strings.Contains(s, "touch /root/.cache/huggingface/.nvsnap-complete") || jc.VolumeMounts[0].MountPath != "/root/.cache/huggingface" {
		t.Errorf("job must run hf download on the engine image into HF_HOME: image=%s mounts=%v\n%s", jc.Image, jc.VolumeMounts, s)
	}
	var sawToken bool
	for _, e := range jc.Env {
		if e.Name == "HF_TOKEN" && e.ValueFrom != nil {
			sawToken = true
		}
	}
	if !sawToken {
		t.Error("registry credentials must be forwarded to the download Job")
	}
	if len(v.newInits) != 1 || v.newInits[0].Name != "nvsnap-model-download" || !strings.Contains(v.newInits[0].Args[0], "while [ ! -f") {
		t.Errorf("the pod gets a wait init, got %v", v.newInits)
	}
	if v.env["HF_HUB_OFFLINE"] != "1" {
		t.Error("engine must start offline and read the volume")
	}
	vol, ok := v.volumes[modelVolumeName]
	if !ok || vol.PersistentVolumeClaim == nil {
		t.Errorf("RWX mode: rootfs landing gets the claim volume: %+v", vol)
	}
}

func TestModelVolume_CompleteCreatesNoJob(t *testing.T) {
	kc := fake.NewSimpleClientset()
	m, el := mvMutator(t, modelvolume.ModeRWX, election.RoleLeader, kc)
	uri := "hf://Qwen/Qwen2.5-32B-Instruct"
	if _, err := m.ModelVolume.EnsureWriterClaim(context.Background(), uri, "fn"); err != nil {
		t.Fatal(err)
	}
	if err := m.ModelVolume.MarkComplete(context.Background(), uri, "fn"); err != nil {
		t.Fatal(err)
	}
	patches, err := m.Mutate(context.Background(), stockVLLMPod())
	if err != nil {
		t.Fatal(err)
	}
	v := viewMV(stockVLLMPod(), patches)
	if el.called != 0 || v.labels[modelvolume.RoleLabel] != "reader" {
		t.Errorf("complete volume: reader role; labels=%v", v.labels)
	}
	if _, err := kc.BatchV1().Jobs("fn").Get(context.Background(), modelvolume.JobName(uri), metav1.GetOptions{}); err == nil {
		t.Error("a complete volume needs no download Job")
	}
}

func TestModelVolume_LeavesOthersAlone(t *testing.T) {
	kc := fake.NewSimpleClientset()
	m, el := mvMutator(t, modelvolume.ModeRWX, election.RoleLeader, kc)
	customer := stockVLLMPod()
	customer.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{{Name: "models", MountPath: "/root/.cache/huggingface"}}
	customer.Spec.Volumes = []corev1.Volume{{Name: "models", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "their-nfs"}}}}
	if p, err := m.modelVolumePatches(context.Background(), customer); err != nil || p != nil {
		t.Errorf("customer PVC at HF_HOME must be left alone: %v %v", p, err)
	}
	noGPU := stockVLLMPod()
	noGPU.Spec.Containers[0].Resources = corev1.ResourceRequirements{}
	if p, _ := m.modelVolumePatches(context.Background(), noGPU); p != nil {
		t.Error("a pod without GPUs is not a model worker")
	}
	frontend := stockVLLMPod()
	frontend.Spec.Containers[0].Args = []string{"python3 -m dynamo.frontend --router-mode kv"}
	if p, _ := m.modelVolumePatches(context.Background(), frontend); p != nil {
		t.Error("a pod naming no model is left alone")
	}
	if el.called != 0 {
		t.Error("no election for pods that are left alone")
	}
}

// A fresh failure record means the last copy gave up; a reader that
// referenced the read-only claim would wait forever, so the pod keeps its
// own download and nothing is created for it.
func TestModelVolume_RecentFailureLeavesPodAlone(t *testing.T) {
	kc := fake.NewSimpleClientset()
	m, _ := mvMutatorReader(t, modelvolume.ModeBlock, "", election.RoleFollower, kc)
	uri := "ngc://org/team/nemotron3-ultra-genrm:bf16-fixed"
	if err := m.ModelVolume.RecordFailure(context.Background(), uri, "copy failed"); err != nil {
		t.Fatal(err)
	}
	pod := ngcFunctionPod()
	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range patches {
		if strings.Contains(p.Path, "nvsnap.io~1model") || strings.HasPrefix(p.Path, "/spec/volumes") || strings.HasPrefix(p.Path, "/spec/initContainers") {
			t.Errorf("pod must be left on its own download, got patch %s", p.Path)
		}
	}
	if jobs, _ := kc.BatchV1().Jobs("").List(context.Background(), metav1.ListOptions{}); len(jobs.Items) != 0 {
		t.Error("no download Job while the failure record is fresh")
	}
}

func cacheMutator(t *testing.T, kc *fake.Clientset) *Mutator {
	t.Helper()
	m, _ := mvMutatorReader(t, modelvolume.ModeBlock, "", election.RoleFollower, kc)
	ccfg := m.ModelVolume.Cfg
	ccfg.Kind = modelvolume.KindCache
	m.CacheVolume = &modelvolume.Provisioner{Kube: kc, Cfg: ccfg}
	return m
}

func completeModelPV(t *testing.T, kc *fake.Clientset, uri string) {
	t.Helper()
	pv := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pv-" + modelvolume.Key(uri),
		Labels: map[string]string{modelvolume.IdentityLabel: modelvolume.Key(uri), modelvolume.CompleteLabel: "true"}},
		Spec: corev1.PersistentVolumeSpec{Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("2Gi")},
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			PersistentVolumeSource:        corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{Driver: "nvmesh-csi.excelero.com", VolumeHandle: "c:csi-m:v"}}}}
	if _, err := kc.CoreV1().PersistentVolumes().Create(context.Background(), pv, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
}

// No cache volume for this engine configuration and ordinal yet: the pod
// keeps its local cachedir and is marked for capture after Ready, with the
// identity (config hash + ordinal) and the emptyDir location stamped.
func TestCacheVolume_FirstPodMarkedForCapture(t *testing.T) {
	kc := fake.NewSimpleClientset()
	uri := "ngc://org/team/nemotron3-ultra-genrm:bf16-fixed"
	completeModelPV(t, kc, uri)
	m := cacheMutator(t, kc)
	pod := ngcFunctionPod()
	pod.Name = "mini-service-kimi-k3-1"
	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatal(err)
	}
	v := viewMV(pod, patches)
	if v.labels[CacheCaptureLabel] != "true" || !strings.HasPrefix(v.annotations[CacheURIAnnotation], "cache://") || !strings.HasSuffix(v.annotations[CacheURIAnnotation], "/1") ||
		v.annotations[CacheVolumeAnnotation] != cacheDirVolumeName || v.annotations[CacheSubpathAnnotation] != "cache" {
		t.Errorf("capture stamp: labels=%v ann=%v", v.labels, v.annotations)
	}
	for _, c := range v.newInits {
		if c.Name == cacheSeedInitName {
			t.Error("no seed init without a complete cache volume")
		}
	}
	// Same config, ordinal 0 is a different key.
	pod0 := ngcFunctionPod()
	pod0.Name = "mini-service-kimi-k3-0"
	p0, _ := m.Mutate(context.Background(), pod0)
	if a := viewMV(pod0, p0).annotations[CacheURIAnnotation]; !strings.HasSuffix(a, "/0") || strings.TrimSuffix(a, "/0") != strings.TrimSuffix(v.annotations[CacheURIAnnotation], "/1") {
		t.Errorf("ordinal 0 shares the config hash and differs in ordinal: %s vs %s", a, v.annotations[CacheURIAnnotation])
	}
}

// Cache volume complete: the read-only claim is minted at admission and a
// seed init copies it into the pod's cachedir before the engine starts.
func TestCacheVolume_CompleteSeedsCachedir(t *testing.T) {
	kc := fake.NewSimpleClientset()
	uri := "ngc://org/team/nemotron3-ultra-genrm:bf16-fixed"
	completeModelPV(t, kc, uri)
	m := cacheMutator(t, kc)
	pod := ngcFunctionPod()
	pod.Name = "mini-service-kimi-k3-0"
	curi, ok := m.cacheURI(pod)
	if !ok {
		t.Fatal("cache uri")
	}
	cpv := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pv-cache",
		Labels: map[string]string{modelvolume.CacheLabel: modelvolume.Key(curi), modelvolume.CompleteLabel: "true"}},
		Spec: corev1.PersistentVolumeSpec{Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			PersistentVolumeSource:        corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{Driver: "nvmesh-csi.excelero.com", VolumeHandle: "c:csi-c:v"}}}}
	if _, err := kc.CoreV1().PersistentVolumes().Create(context.Background(), cpv, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatal(err)
	}
	v := viewMV(pod, patches)
	if v.labels[CacheCaptureLabel] != "" {
		t.Error("a pod seeded from a complete cache is not a capture candidate")
	}
	claim := m.CacheVolume.Cfg.ReadOnlyClaimName(curi)
	vol, ok := v.volumes[cacheSeedVolumeName]
	if !ok || vol.PersistentVolumeClaim == nil || vol.PersistentVolumeClaim.ClaimName != claim || !vol.PersistentVolumeClaim.ReadOnly {
		t.Errorf("cache seed volume must reference the read-only cache claim: %+v", vol)
	}
	pvc, err := kc.CoreV1().PersistentVolumeClaims("sr-fn").Get(context.Background(), claim, metav1.GetOptions{})
	if err != nil || pvc.Labels[modelvolume.CacheLabel] != modelvolume.Key(curi) || pvc.Spec.AccessModes[0] != corev1.ReadOnlyMany {
		t.Errorf("read-only cache claim minted at admission with the cache label: %v %v", err, pvc)
	}
	var seed *corev1.Container
	for i := range v.newInits {
		if v.newInits[i].Name == cacheSeedInitName {
			seed = &v.newInits[i]
		}
	}
	if seed == nil {
		t.Fatalf("seed init missing: %v", v.newInits)
	}
	if seed.Image != pod.Spec.Containers[0].Image || !strings.Contains(seed.Args[0], "cp -a /nvsnap-cache-seed/. /opt/nvsnap/cache/") || !strings.HasSuffix(seed.Args[0], "exit 0") ||
		seed.SecurityContext == nil || seed.SecurityContext.Capabilities == nil || seed.Resources.Limits.Cpu().IsZero() {
		t.Errorf("seed init copies best effort with the baselines: %+v", seed)
	}
	mounts := map[string]string{}
	for _, vm := range seed.VolumeMounts {
		mounts[vm.Name] = vm.MountPath
	}
	if mounts[cacheSeedVolumeName] != cacheSeedMount || mounts[cacheDirVolumeName] != "/opt/nvsnap" {
		t.Errorf("seed mounts: %v", mounts)
	}
}

func TestPodOrdinal(t *testing.T) {
	for _, tc := range []struct {
		name string
		lbl  string
		want int
	}{{"mini-service-kimi-k3-7", "", 7}, {"vllm-7c9f8b5d4-x2k9q", "", 0}, {"lws-0-3", "", 3}, {"lws-0-3", "5", 5}, {"", "", 0}} {
		p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: tc.name}}
		if tc.lbl != "" {
			p.Labels = map[string]string{lwsWorkerIndexLabel: tc.lbl}
		}
		if got := podOrdinal(p); got != tc.want {
			t.Errorf("%q/%q: %d want %d", tc.name, tc.lbl, got, tc.want)
		}
	}
}
