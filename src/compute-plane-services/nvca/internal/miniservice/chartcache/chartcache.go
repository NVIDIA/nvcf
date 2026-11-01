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

package chartcache

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	lru "github.com/hashicorp/golang-lru/v2"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

type Cache interface {
	manager.Runnable
	Get(in ChartCacheInput, w io.Writer) (bool, error)
	Put(in ChartCacheInput, r io.ReadSeeker, size int64) (string, error)
	Delete(in ChartCacheInput) error
}

func New(dir string) Cache {
	return &localChartCache{
		dir: dir,
		putFile: func(fp string) (writerFile, error) {
			return os.OpenFile(fp, os.O_TRUNC|os.O_CREATE|os.O_WRONLY, 0600)
		},
	}
}

type ChartCacheInput struct {
	HelmChartURL         string
	HelmChartServicePort *int32
	HelmChartServiceName string
	Values               json.RawMessage
	K8sVersion           string
	APIVersions          []string
	// Namespace is the target namespace for the Helm release.
	// This MUST be included in the cache key because Helm templates using
	// .Release.Namespace will render namespace-specific values that cannot
	// be shared across different namespaces.
	Namespace string
}

const renderedFileName = "rendered.json.gz"

func (in ChartCacheInput) makeItemFilePath(baseDir, cacheKey string) string {
	return filepath.Join(baseDir, cacheKey, renderedFileName)
}

func (in ChartCacheInput) makeCacheKey() (string, error) {
	sort.Strings(in.APIVersions)
	inBytes, err := json.Marshal(in)
	if err != nil {
		return "", err
	}
	keyBytes := sha256.Sum256(inBytes)
	// The first 20 characters are sufficient randomness for uniqueness.
	// Don't want to make file names too long, which can cause issues on some systems.
	return hex.EncodeToString(keyBytes[:])[:20], nil
}

// Make LRU cache size arbitrarily large since the only constraint is disk space.
const cacheSize = 100_000

type localChartCache struct {
	dir string
	// Since the local cache is not heavily concurrent,
	// a simple mutex will prevent data races.
	mu    sync.RWMutex
	cache *lru.Cache[string, int64]

	// Mocked in test.
	putFile func(fp string) (writerFile, error)
}

type writerFile interface {
	fs.File
	io.Writer
}

func (c *localChartCache) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	log := logf.FromContext(ctx)

	if err := os.MkdirAll(c.dir, 0700); err != nil {
		// If dir's mount is not configured correctly, try using a tempdir.
		tmpDir, terr := os.MkdirTemp("", "nvca-chartcache.*")
		if terr != nil {
			err = fmt.Errorf("%w (unable to create temp chartcache: %s)", err, terr)
			return err
		}
		log.Info("Using tempdir for chartcache on mkdir error", "error", err, "dir", tmpDir)
		c.dir = tmpDir
		// Clean up dir in between restarts.
		go func() {
			<-ctx.Done()
			if err := os.RemoveAll(tmpDir); err != nil {
				log.Error(err, "Failed to remove tempdir on cleanup", "dir", tmpDir)
			}
		}()
	}

	var err error
	if c.cache, err = lru.New[string, int64](cacheSize); err != nil {
		return err
	}

	// Build cache by looking for rendered files and using their parent dirs as cache keys.
	if err := filepath.WalkDir(c.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || filepath.Base(path) != renderedFileName {
			return err
		}
		di, err := d.Info()
		if err != nil {
			return err
		}
		cacheKey := strings.TrimPrefix(filepath.Dir(path), c.dir+string(filepath.Separator))
		c.cache.Add(cacheKey, di.Size())
		return nil
	}); err != nil {
		return err
	}

	return nil
}

func (c *localChartCache) Get(in ChartCacheInput, w io.Writer) (bool, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	cacheKey, err := in.makeCacheKey()
	if err != nil {
		return false, err
	}
	itemFilePath := in.makeItemFilePath(c.dir, cacheKey)

	if _, ok := c.cache.Get(cacheKey); !ok {
		return false, nil
	}

	switch _, err := os.Stat(itemFilePath); {
	case err == nil:
		f, err := os.Open(itemFilePath)
		if err != nil {
			return false, fmt.Errorf("open cache item file: %v", err)
		}
		defer f.Close()

		gzr, err := gzip.NewReader(f)
		if err != nil {
			return false, fmt.Errorf("create cache item file gzip reader: %v", err)
		}
		defer gzr.Close()

		n, err := io.Copy(w, gzr)
		if err != nil {
			return false, fmt.Errorf("copy cache item file: %v", err)
		}

		c.cache.Add(cacheKey, n)

		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		c.cache.Remove(cacheKey)
		return false, nil
	}
	return false, err
}

func (c *localChartCache) Put(in ChartCacheInput, r io.ReadSeeker, size int64) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	cacheKey, err := in.makeCacheKey()
	if err != nil {
		return "", err
	}
	itemFilePath := in.makeItemFilePath(c.dir, cacheKey)

	n, h, err := c.writeFile(itemFilePath, r, size)
	if err != nil {
		return "", fmt.Errorf("write cache item file: %v", err)
	}

	c.cache.Add(cacheKey, n)
	return h, nil
}

func (c *localChartCache) Delete(in ChartCacheInput) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	cacheKey, err := in.makeCacheKey()
	if err != nil {
		return err
	}
	itemFilePath := in.makeItemFilePath(c.dir, cacheKey)

	if err := os.RemoveAll(filepath.Dir(itemFilePath)); err != nil {
		return err
	}
	c.cache.Remove(cacheKey)
	return nil
}

func (c *localChartCache) writeFile(itemFilePath string, r io.ReadSeeker, size int64) (int64, string, error) {
	n, h, err := c.tryWriteFile(itemFilePath, r)
	if err == nil {
		return n, h, nil
	}
	// syscall.ENOSPC means there is no space left on the device.
	// When returned, eviction should occur then the file write reattempted.
	//
	// This error is not represented in a case in syscall.Errno.Is(),
	// use string comparison.
	// https://github.com/golang/go/issues/37627
	if !strings.HasSuffix(err.Error(), "no space left on device") {
		return n, h, err
	}
	if _, err := r.Seek(0, 0); err != nil {
		return n, h, fmt.Errorf("seek while writing cache file: %v", err)
	}
	// TODO: may want to re-attempt eviction if syscall.ENOSPC occurs again.
	if err := c.evictLRUs(size); err != nil {
		return n, h, err
	}
	return c.tryWriteFile(itemFilePath, r)
}

func (c *localChartCache) tryWriteFile(itemFilePath string, r io.Reader) (int64, string, error) {
	if err := os.MkdirAll(filepath.Dir(itemFilePath), 0700); err != nil {
		return 0, "", err
	}

	f, err := c.putFile(itemFilePath)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()

	gzw := gzip.NewWriter(f)
	h := sha256.New()
	mw := io.MultiWriter(gzw, h)

	n, err := io.Copy(mw, r)
	if err != nil {
		gzw.Close()
		return n, "", err
	}
	gzw.Close()

	// Stat after close to get total compressed file size.
	fi, err := f.Stat()
	if err != nil {
		return n, "", err
	}

	sum := h.Sum(nil)
	return fi.Size(), "sha256:" + hex.EncodeToString(sum[:]), nil
}

func (c *localChartCache) evictLRUs(size int64) error {
	for {
		cacheKey, itemSize, ok := c.cache.GetOldest()
		if !ok {
			return fmt.Errorf("cannot evict enough items for size %d", size)
		}
		if err := os.RemoveAll(filepath.Join(c.dir, cacheKey)); err != nil {
			return err
		}
		c.cache.Remove(cacheKey)
		if itemSize >= size {
			return nil
		}
		size -= itemSize
	}
}
