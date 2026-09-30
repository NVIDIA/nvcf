/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package agent

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/modelvolume"
)

// CacheSeedServer serves compile-cache seeds to pods on this node from a
// node-local mirror, so a pod attaches only its model volume and not a
// second block volume for a cache of about a gigabyte.
//
// Mirror lifecycle: the first request for a key on this node starts a
// fill (mint the read-only cache claim in the nvsnap namespace, attach it
// through a mount-holder, copy the tree under Root, mark it complete,
// release the claim); requests during the fill get 503 with Retry-After
// and the seed init polls. Mirrors unused for Retention are removed by
// Sweep. Nothing here is load-bearing: a pod that cannot get its seed
// compiles as it would without nvsnap.
type CacheSeedServer struct {
	Kube   kubernetes.Interface
	Cache  *modelvolume.Provisioner
	Minter *checkpointstore.SharedVolumePromoter
	Copier interface {
		Copy(ctx context.Context, destRoot string, sources []checkpointstore.CaptureSource) (int64, int64, error)
	}
	NodeName          string
	HolderImage       string
	HolderPullSecrets []string
	// HostFSRoot is the agent's view of the node filesystem; Root is the
	// host directory that holds one mirror per key.
	HostFSRoot string
	Root       string
	// Secret signs seed tokens (the agent API token).
	Secret    string
	Retention time.Duration
	Log       logrus.FieldLogger

	// FillBackoff is how long a key waits after a failed fill before the
	// next request may start another; zero means one minute. Without it
	// every seed poll would spawn a new holder against a failing fill.
	FillBackoff time.Duration

	// fill is the seam tests replace; nil means fillMirror.
	fill func(ctx context.Context, uri string) error
	now  func() time.Time

	mu       sync.Mutex
	inflight map[string]bool
	lastFail map[string]time.Time
}

func (s *CacheSeedServer) log() logrus.FieldLogger {
	if s.Log != nil {
		return s.Log
	}
	return logrus.NewEntry(logrus.New()).WithField("subsys", "cacheseed")
}

func (s *CacheSeedServer) mirrorDir(key string) string {
	return filepath.Join(s.HostFSRoot, s.Root, key)
}

// Handler answers GET /v1/cache-seed/{key}?uri=<cache uri>&ns=<namespace>
// with a tar stream of the mirror, or 503 while the mirror is filling.
func (s *CacheSeedServer) Handler(w http.ResponseWriter, r *http.Request) {
	key := mux.Vars(r)["key"]
	uri := r.URL.Query().Get("uri")
	ns := r.URL.Query().Get("ns")
	if uri == "" || ns == "" || modelvolume.Key(uri) != key {
		http.Error(w, "key, uri and ns are required and must agree", http.StatusBadRequest)
		return
	}
	if !modelvolume.SeedTokenValid(s.Secret, ns, uri, r.Header.Get(modelvolume.SeedTokenHeader)) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	dir := s.mirrorDir(key)
	if _, err := os.Stat(filepath.Join(dir, modelvolume.SeedMirrorMarker)); err != nil {
		s.startFill(uri)
		w.Header().Set("Retry-After", "5")
		http.Error(w, "seed not on this node yet; filling", http.StatusServiceUnavailable)
		return
	}
	now := time.Now()
	_ = os.Chtimes(filepath.Join(dir, modelvolume.SeedLastUsedFile), now, now)
	w.Header().Set("Content-Type", "application/x-tar")
	if err := writeTar(w, dir); err != nil {
		s.log().WithError(err).WithField("key", key).Warn("cache seed: tar stream failed")
	}
}

// startFill runs one fill per key at a time, in the background.
func (s *CacheSeedServer) startFill(uri string) {
	key := modelvolume.Key(uri)
	now := time.Now
	if s.now != nil {
		now = s.now
	}
	backoff := s.FillBackoff
	if backoff <= 0 {
		backoff = time.Minute
	}
	s.mu.Lock()
	if s.inflight == nil {
		s.inflight = map[string]bool{}
		s.lastFail = map[string]time.Time{}
	}
	if s.inflight[key] || now().Sub(s.lastFail[key]) < backoff {
		s.mu.Unlock()
		return
	}
	s.inflight[key] = true
	s.mu.Unlock()
	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.inflight, key)
			s.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		fill := s.fill
		if fill == nil {
			fill = s.fillMirror
		}
		start := now()
		if err := fill(ctx, uri); err != nil {
			s.mu.Lock()
			s.lastFail[key] = now()
			s.mu.Unlock()
			s.log().WithError(err).WithField("cache", uri).Warn("cache seed: mirror fill failed; pods compile locally until it succeeds")
			return
		}
		s.log().WithFields(logrus.Fields{"cache": uri, "elapsed": time.Since(start).Round(time.Second).String()}).Info("cache seed: node mirror ready")
	}()
}

// fillMirror copies the complete cache volume for uri into the node
// mirror: one attach per node per key.
func (s *CacheSeedServer) fillMirror(ctx context.Context, uri string) error {
	if s.Cache == nil || s.Minter == nil || s.Copier == nil || s.Kube == nil {
		return fmt.Errorf("cache seed server not wired")
	}
	st, err := s.Cache.Lookup(ctx, uri)
	if err != nil {
		return err
	}
	if !st.Complete || st.PrimaryPV == "" {
		return fmt.Errorf("cache %s is not complete", uri)
	}
	sysNS := s.Cache.Cfg.SystemNamespace()
	claim := s.Cache.Cfg.ReadOnlyClaimName(uri)
	if err := s.Minter.MintReadOnlyFromPVLabels(ctx, st.PrimaryPV, s.Cache.Cfg.ReadOnlyPVName(uri, sysNS), claim, sysNS, s.Cache.Cfg.ReadOnlyLabels(uri)); err != nil {
		return fmt.Errorf("mint read-only cache claim: %w", err)
	}
	pvc, err := s.Kube.CoreV1().PersistentVolumeClaims(sysNS).Get(ctx, claim, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get claim %s/%s: %w", sysNS, claim, err)
	}
	entry, _ := s.log().(*logrus.Entry)
	if entry == nil {
		entry = logrus.NewEntry(logrus.New())
	}
	name := "nvsnap-cache-seed-" + modelvolume.Key(uri) + "-" + shortNode(s.NodeName)
	h := checkpointstore.NewMountHolder(s.Kube, entry, sysNS, name, s.NodeName, claim, pvc.UID, s.HolderImage, s.HostFSRoot, s.HolderPullSecrets)
	if err := h.Create(ctx); err != nil {
		return fmt.Errorf("create seed holder: %w", err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), checkpointstore.MountHolderDeleteTimeout+5*time.Second)
		defer cancel()
		if derr := h.Delete(cleanup); derr != nil {
			entry.WithError(derr).Warn("cache seed: holder delete failed")
		}
		// The read-only claim is only needed for the copy; without it the
		// read-only PV goes Released and the reaper removes it.
		if derr := s.Kube.CoreV1().PersistentVolumeClaims(sysNS).Delete(cleanup, claim, metav1.DeleteOptions{}); derr != nil && !apierrors.IsNotFound(derr) {
			entry.WithError(derr).Warn("cache seed: release read-only claim failed")
		}
	}()
	if err := h.WaitRunning(ctx); err != nil {
		return fmt.Errorf("seed holder not running: %w", err)
	}
	src, err := h.PVMountPath()
	if err != nil {
		return err
	}
	return s.copyMirror(ctx, uri, src)
}

// copyMirror copies the attached cache volume at agentSrc (an agent-view
// path under HostFSRoot, as the mount-holder reports it) into the mirror
// for uri, then marks it complete. The copier takes host paths and
// prefixes HostFSRoot itself, so the prefix is stripped here; passing the
// agent path through produced /host/host/... (ct1, 2026-09-30).
func (s *CacheSeedServer) copyMirror(ctx context.Context, uri, agentSrc string) error {
	if s.Copier == nil {
		return fmt.Errorf("cache seed server has no copier")
	}
	hostSrc := agentSrc
	if root := strings.TrimSuffix(s.HostFSRoot, "/"); root != "" {
		if !strings.HasPrefix(agentSrc, root+"/") {
			return fmt.Errorf("mount path %q is not under the host root %q", agentSrc, s.HostFSRoot)
		}
		hostSrc = strings.TrimPrefix(agentSrc, root)
	}
	key := modelvolume.Key(uri)
	final := s.mirrorDir(key)
	tmp := final + ".tmp"
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		return err
	}
	if _, _, err := s.Copier.Copy(ctx, tmp, []checkpointstore.CaptureSource{{Kind: checkpointstore.SourceKindRootfs, SrcPath: hostSrc}}); err != nil {
		return fmt.Errorf("copy cache into node mirror: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if err := os.WriteFile(filepath.Join(tmp, modelvolume.SeedLastUsedFile), []byte(now), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(tmp, modelvolume.SeedMirrorMarker), []byte(now), 0o644); err != nil {
		return err
	}
	_ = os.RemoveAll(final)
	return os.Rename(tmp, final)
}

// Sweep removes mirrors unused for Retention. Zero Retention keeps them.
func (s *CacheSeedServer) Sweep(now time.Time) int {
	if s.Retention <= 0 {
		return 0
	}
	entries, err := os.ReadDir(filepath.Join(s.HostFSRoot, s.Root))
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() || strings.HasSuffix(e.Name(), ".tmp") {
			continue
		}
		dir := filepath.Join(s.HostFSRoot, s.Root, e.Name())
		fi, err := os.Stat(filepath.Join(dir, modelvolume.SeedLastUsedFile))
		last := time.Time{}
		if err == nil {
			last = fi.ModTime()
		} else if di, derr := os.Stat(dir); derr == nil {
			last = di.ModTime()
		}
		if now.Sub(last) < s.Retention {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			s.log().WithError(err).WithField("mirror", dir).Warn("cache seed: remove stale mirror failed")
			continue
		}
		n++
		s.log().WithField("key", e.Name()).Info("cache seed: removed node mirror unused past retention")
	}
	return n
}

// Run sweeps at interval until ctx is done.
func (s *CacheSeedServer) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		s.Sweep(time.Now())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// writeTar streams dir as an uncompressed tar of relative paths, skipping
// the mirror's own marker files. Regular files, directories and relative
// symlinks only.
func writeTar(w io.Writer, dir string) error {
	tw := tar.NewWriter(w)
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil || rel == "." {
			return rerr
		}
		if rel == modelvolume.SeedMirrorMarker || rel == modelvolume.SeedLastUsedFile {
			return nil
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			target, lerr := os.Readlink(p)
			if lerr != nil || filepath.IsAbs(target) {
				return nil
			}
			link = target
		} else if !info.Mode().IsRegular() && !info.IsDir() {
			return nil
		}
		hdr, herr := tar.FileInfoHeader(info, link)
		if herr != nil {
			return herr
		}
		hdr.Name = filepath.ToSlash(rel)
		if info.IsDir() {
			hdr.Name += "/"
		}
		if werr := tw.WriteHeader(hdr); werr != nil {
			return werr
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		f, ferr := os.Open(p)
		if ferr != nil {
			return ferr
		}
		defer f.Close()
		_, cerr := io.Copy(tw, f)
		return cerr
	})
	if err != nil {
		return err
	}
	return tw.Close()
}

// cacheSeedHandler routes to the seed server once it is wired; before
// that (or without cache volumes) callers get 503 and their init falls
// back to compiling locally.
func (a *Agent) cacheSeedHandler(w http.ResponseWriter, r *http.Request) {
	if a.cacheSeeds == nil {
		w.Header().Set("Retry-After", "5")
		http.Error(w, "cache seeding not enabled on this agent", http.StatusServiceUnavailable)
		return
	}
	a.cacheSeeds.Handler(w, r)
}
