// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/modelvolume"
)

// Compile-cache volume for Helm functions on block storage (mechanism 5 of
// docs/proposals/helm-shared-model-volume.md). Each pod compiles into its
// local cachedir emptyDir; the first pod of a given engine configuration
// and ordinal that becomes Ready has its cache subtree captured by the
// agent on its node into a sized read-only volume (the model-volume
// machinery with KindCache). Every later pod with the same key gets the
// volume mounted read-only and a seed init copies it into its writable
// cachedir before the engine starts, so torch.compile, Inductor, Triton
// and FlashInfer find their artifacts.
//
// Why per ordinal: in a tensor-parallel group every rank writes rank-
// specific artifacts, and shared paths (autotune results) differ in
// content between ranks (measured 2026-09-29), so caches are never merged.
// A single-pod TP group is ordinal 0 and holds every rank's directory.

// Stamps on a pod whose cachedir the agent captures once it is Ready.
const (
	// CacheCaptureLabel opts the pod into capture.
	CacheCaptureLabel = "nvsnap.io/cache-capture"
	// CacheURIAnnotation carries the cache identity (config hash, ordinal).
	CacheURIAnnotation = "nvsnap.io/cache-uri"
	// CacheVolumeAnnotation names the emptyDir volume holding the cache.
	CacheVolumeAnnotation = "nvsnap.io/cache-volume"
	// CacheSubpathAnnotation is the cache subtree inside that volume.
	CacheSubpathAnnotation = "nvsnap.io/cache-subpath"
)

const (
	cacheSeedVolumeName = "nvsnap-cache-seed"
	cacheSeedInitName   = "nvsnap-seed-cache"
	cacheSeedMount      = "/nvsnap-cache-seed"
	cacheSubdir         = "cache"
	lwsWorkerIndexLabel = "leaderworkerset.sigs.k8s.io/worker-index"
)

var ordinalSuffix = regexp.MustCompile(`-(\d+)$`)

// podOrdinal is the pod's index in its group: the LeaderWorkerSet worker
// index, else a StatefulSet-style numeric name suffix, else 0.
func podOrdinal(pod *corev1.Pod) int {
	if v, ok := pod.Labels[lwsWorkerIndexLabel]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	if m := ordinalSuffix.FindStringSubmatch(pod.Name); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			return n
		}
	}
	return 0
}

// cacheURI is the cache identity: engine configuration hash plus ordinal.
func (m *Mutator) cacheURI(pod *corev1.Pod) (string, bool) {
	if m.Composer == nil {
		return "", false
	}
	key := checkpointstore.ShortHash(checkpointstore.ComputeHash(m.Composer.Compose(pod, m.MainContainer)))[:16]
	return fmt.Sprintf("cache://%s/%d", key, podOrdinal(pod)), true
}

// cacheVolumePatches decorates a Block-mode model-volume reader with the
// compile-cache volume: complete, the read-only claim is minted in the
// pod's namespace and a seed init fills the cachedir from it; otherwise
// the pod is marked for capture. initCreated says whether an earlier
// patch already created /spec/initContainers.
func (m *Mutator) cacheVolumePatches(ctx context.Context, pod *corev1.Pod, main *corev1.Container, log logrus.FieldLogger, initCreated bool) []PatchOp {
	if m.CacheVolume == nil || m.CacheDir == "" {
		return nil
	}
	uri, ok := m.cacheURI(pod)
	if !ok {
		return nil
	}
	log = log.WithField("cache", uri)
	st, err := m.CacheVolume.Lookup(ctx, uri)
	if err != nil {
		log.WithError(err).Warn("cache volume: lookup failed; pod compiles locally")
		return nil
	}
	mp := newMetaPatcher(pod)
	switch {
	case st.Complete && m.CacheSeedMode == modelvolume.SeedModeAgent && m.CacheSeedImage != "":
		// Agent-served seed: no claim, no second block attach. The init
		// fetches the mirror from the agent on its own node, signed for
		// this namespace and key; a miss means the engine compiles
		// locally, exactly as without nvsnap.
		if err := m.CacheVolume.TouchLastUsed(ctx, st.PrimaryPV); err != nil {
			log.WithError(err).Warn("cache volume: record last use failed")
		}
		patches := mp.annotation(CacheURIAnnotation, uri)
		if pod.Spec.InitContainers == nil && !initCreated {
			patches = append(patches, PatchOp{Op: "add", Path: "/spec/initContainers", Value: []any{}})
		}
		patches = append(patches, PatchOp{Op: "add", Path: "/spec/initContainers/-", Value: m.agentSeedInit(pod, main, uri)})
		log.Info("cache volume: complete; seeding the cachedir from the node agent")
		return patches
	case st.Complete:
		if m.ReadOnlyMinter == nil {
			return nil
		}
		claim := m.CacheVolume.Cfg.ReadOnlyClaimName(uri)
		if err := m.ReadOnlyMinter.MintReadOnlyFromPVLabels(ctx, st.PrimaryPV, m.CacheVolume.Cfg.ReadOnlyPVName(uri, pod.Namespace), claim, pod.Namespace, m.CacheVolume.Cfg.ReadOnlyLabels(uri)); err != nil {
			log.WithError(err).Warn("cache volume: mint read-only claim failed; pod compiles locally")
			return nil
		}
		if err := m.CacheVolume.TouchLastUsed(ctx, st.PrimaryPV); err != nil {
			log.WithError(err).Warn("cache volume: record last use failed")
		}
		patches := mp.annotation(CacheURIAnnotation, uri)
		if pod.Spec.InitContainers == nil && !initCreated {
			patches = append(patches, PatchOp{Op: "add", Path: "/spec/initContainers", Value: []any{}})
		}
		dst := path.Join(m.CacheDir, cacheSubdir)
		seed := corev1.Container{
			Name:    cacheSeedInitName,
			Image:   main.Image,
			Command: []string{"/bin/sh", "-c"},
			// Best effort by construction: a failed copy leaves an empty
			// cache and the engine compiles as it would have anyway.
			Args: []string{fmt.Sprintf("cp -a %s/. %s/ 2>/dev/null && chmod -R a+rwX %s 2>/dev/null; echo \"nvsnap: cache seeded $(find %s -type f | wc -l) files\"; exit 0", cacheSeedMount, dst, dst, dst)},
			VolumeMounts: []corev1.VolumeMount{
				{Name: cacheSeedVolumeName, MountPath: cacheSeedMount, ReadOnly: true},
				{Name: cacheDirVolumeName, MountPath: m.CacheDir},
			},
		}
		modelvolume.Harden(&seed, modelvolume.HoldResources, main.SecurityContext)
		patches = append(patches,
			PatchOp{Op: "add", Path: "/spec/volumes/-", Value: corev1.Volume{Name: cacheSeedVolumeName, VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim, ReadOnly: true}}}},
			PatchOp{Op: "add", Path: "/spec/initContainers/-", Value: seed},
		)
		log.WithField("claim", claim).Info("cache volume: complete; seeding the cachedir from the read-only claim")
		return patches
	case st.Failed:
		log.Info("cache volume: recent capture failure recorded; pod compiles locally")
		return nil
	default:
		patches := mp.label(CacheCaptureLabel, "true")
		patches = append(patches, mp.annotation(CacheURIAnnotation, uri)...)
		patches = append(patches, mp.annotation(CacheVolumeAnnotation, cacheDirVolumeName)...)
		patches = append(patches, mp.annotation(CacheSubpathAnnotation, cacheSubdir)...)
		log.Info("cache volume: none yet; pod marked for capture after Ready")
		return patches
	}
}

// agentSeedInit is the init container that fetches the cache seed from
// the node agent and unpacks it into the cachedir. It runs on the engine
// image, like the volume seed: a tenant pod only holds the function's own
// pull secrets, so an nvsnap image injected here would fail to pull
// (ct1, 2026-09-30). Python is what every engine image has; curl and tar
// are the fallback. Best effort by construction: every path exits 0, a
// missed seed only costs the compile the engine would have done anyway.
func (m *Mutator) agentSeedInit(pod *corev1.Pod, main *corev1.Container, uri string) corev1.Container {
	agentURL := fmt.Sprintf("http://$(NVSNAP_HOST_IP):%d", MountPrepDefaultAgentPort)
	if m.AgentBaseURL != "" {
		agentURL = strings.TrimRight(m.AgentBaseURL, "/")
	}
	seedURL := fmt.Sprintf("%s/v1/cache-seed/%s?uri=%s&ns=%s", agentURL, modelvolume.Key(uri), url.QueryEscape(uri), url.QueryEscape(pod.Namespace))
	dest := path.Join(m.CacheDir, cacheSubdir)
	c := corev1.Container{
		Name:    cacheSeedInitName,
		Image:   main.Image,
		Command: []string{"/bin/sh", "-c"},
		Args:    []string{agentSeedScript(dest)},
		Env: []corev1.EnvVar{
			{Name: "NVSNAP_HOST_IP", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "status.hostIP"}}},
			{Name: "NVSNAP_SEED_URL", Value: seedURL},
			{Name: "NVSNAP_SEED_TOKEN", Value: modelvolume.SeedToken(m.CacheSeedSecret, pod.Namespace, uri)},
			{Name: "NVSNAP_SEED_TIMEOUT", Value: "180"},
		},
		VolumeMounts: []corev1.VolumeMount{{Name: cacheDirVolumeName, MountPath: m.CacheDir}},
	}
	modelvolume.Harden(&c, modelvolume.HoldResources, main.SecurityContext)
	return c
}

// agentSeedScript polls the agent until the node mirror is ready (503),
// then unpacks the tar into dest world-writable. Python first, curl and
// tar as the fallback; any failure prints why and exits 0.
func agentSeedScript(dest string) string {
	py := `import os,sys,tarfile,time,urllib.request,urllib.error
url=os.environ["NVSNAP_SEED_URL"].replace("$(NVSNAP_HOST_IP)",os.environ.get("NVSNAP_HOST_IP",""))
dest=sys.argv[1]; deadline=time.time()+float(os.environ.get("NVSNAP_SEED_TIMEOUT","180")); t0=time.time()
while True:
    try:
        r=urllib.request.urlopen(urllib.request.Request(url,headers={"X-Nvsnap-Seed-Token":os.environ.get("NVSNAP_SEED_TOKEN","")}),timeout=600)
        n=0
        with tarfile.open(fileobj=r,mode="r|") as tf:
            for m in tf:
                p=os.path.normpath(m.name)
                if p.startswith("..") or os.path.isabs(p) or (m.issym() and (os.path.isabs(m.linkname) or os.path.normpath(os.path.join(os.path.dirname(p),m.linkname)).startswith(".."))): continue
                if m.isdir() or m.isreg() or m.issym(): tf.extract(m,dest); n+=1
        for root,ds,fs in os.walk(dest):
            for d in ds: os.chmod(os.path.join(root,d),0o777)
            for f in fs:
                fp=os.path.join(root,f)
                if not os.path.islink(fp): os.chmod(fp,0o777 if os.stat(fp).st_mode&0o111 else 0o666)
        print("nvsnap: cache seeded %d entries from the node agent in %.0fs"%(n,time.time()-t0)); break
    except urllib.error.HTTPError as e:
        if e.code!=503: print("nvsnap: seed refused (%d); engine compiles locally"%e.code); break
    except Exception as e:
        err=str(e)
        if time.time()>deadline: print("nvsnap: seed error (%s); engine compiles locally"%err); break
    if time.time()>deadline: print("nvsnap: seed not ready within deadline; engine compiles locally"); break
    time.sleep(5)
`
	return fmt.Sprintf(`mkdir -p %[1]s; if command -v python3 >/dev/null 2>&1; then python3 -c %[2]s %[1]s; else echo "nvsnap: no python3 in the engine image; engine compiles locally"; fi; exit 0`, dest, shellQuote(py))
}
