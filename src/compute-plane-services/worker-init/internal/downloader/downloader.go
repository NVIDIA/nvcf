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

package downloader

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/cenkalti/backoff/v4"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"

	initMetrics "github.com/NVIDIA/nvcf/src/compute-plane-services/worker-init/internal/metrics"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/tracing"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/types"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/utils"
)

const DefaultChunkSize = 16 * utils.OneMB
const retryCount = 10

type ArtifactsDownloader struct {
	baseRepo                 string
	client                   *http.Client
	maxMultiFileConcurrency  int
	maxSingleFileConcurrency int
	chunkSize                int
	manifestLock             sync.Mutex
	repoWritableOnce         sync.Once
	repoWritable             bool
}

func NewArtifactDownloader(repo string, maxMultiFileConcurrency int, maxSingleFileConcurrency int, chunkSize int) *ArtifactsDownloader {
	client := http.Client{
		Transport: tracing.NewOtelTransport(&http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   5 * time.Second,
				KeepAlive: 15 * time.Second,
			}).DialContext,
			MaxIdleConnsPerHost: maxMultiFileConcurrency * maxSingleFileConcurrency,
			IdleConnTimeout:     30 * time.Second,
		}),
	}

	return &ArtifactsDownloader{
		baseRepo:                 repo,
		client:                   &client,
		maxMultiFileConcurrency:  maxMultiFileConcurrency,
		maxSingleFileConcurrency: maxSingleFileConcurrency,
		chunkSize:                chunkSize,
	}
}

func (d *ArtifactsDownloader) checkArtifactCache(ctx context.Context, artifacts []types.Artifact) ([]types.Artifact, error) {
	eg := errgroup.Group{}
	eg.SetLimit(d.maxMultiFileConcurrency)

	listLock := sync.Mutex{}
	var filesToFetch []types.Artifact
	var cacheCheckErr error
	var cacheErrLock sync.Mutex

	for _, artifact := range artifacts {
		artifact := artifact
		eg.Go(func() error {
			isCached, err := d.isArtifactCached(ctx, artifact)
			if err != nil {
				cacheErrLock.Lock()
				if cacheCheckErr == nil {
					cacheCheckErr = err
				}
				cacheErrLock.Unlock()
				return err
			}
			if isCached {
				zap.L().Info(
					"Artifact already cached",
					zap.String("name", artifact.Name),
					zap.String("path", artifact.Path),
				)
			} else {
				zap.L().Info(
					"Artifact not found in cache",
					zap.String("name", artifact.Name),
					zap.String("path", artifact.Path),
				)
				listLock.Lock()
				filesToFetch = append(filesToFetch, artifact)
				listLock.Unlock()
			}
			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		zap.L().Error(err.Error())
		return nil, err
	}
	if cacheCheckErr != nil {
		return nil, cacheCheckErr
	}

	zap.L().Info("Artifact cache check finished!")
	return filesToFetch, nil
}

// DownloadArtifacts download and installs all artifacts in parallel
func (d *ArtifactsDownloader) DownloadArtifacts(ctx context.Context, artifacts []types.Artifact) error {
	ctx, span := otel.Tracer("nvcf-worker-init").Start(ctx, "DownloadArtifacts")
	defer span.End()

	initMetrics.WorkerInitArtifactCounter.Add(float64(len(artifacts)))

	// Check to see if data is already available in cache.
	filesToFetch, checkErr := d.checkArtifactCache(ctx, artifacts)
	if checkErr != nil {
		return fmt.Errorf("failed to check artifact cache: %s", checkErr)
	}

	span.SetAttributes(
		attribute.Int("downloader.files_cached", len(artifacts)-len(filesToFetch)),
		attribute.Int("downloader.files_to_fetch", len(filesToFetch)),
	)

	if len(filesToFetch) == 0 {
		zap.L().Info("All artifacts in cache. Exiting")
		return nil
	}

	if len(filesToFetch) != len(artifacts) {
		// Filesystem could be in read-only (or other os error) mode when we are using
		// a pvc to cache models.
		zap.L().Warn("found partial artifacts in cache. attempting to download the rest.")
	}

	zap.L().Info("Found artifacts to fetch", zap.Int("count", len(filesToFetch)))

	eg, ctx := errgroup.WithContext(ctx)
	eg.SetLimit(d.maxMultiFileConcurrency)
	for _, artifact := range filesToFetch {
		if ctx.Err() != nil {
			break
		}
		artifact := artifact
		eg.Go(func() error {
			if err := d.fetchAndInstallArtifact(ctx, artifact); err != nil {
				zap.L().Error(
					"download job failed",
					zap.String("name", artifact.Name),
					zap.String("path", artifact.Path),
					zap.Error(err),
				)
				initMetrics.WorkerInitArtifactDownloadFailCounter.Add(float64(1))
				return fmt.Errorf("failed to download %s of artifact %s: %w", artifact.Path, artifact.Name, err)
			}
			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		return tracing.RecordSpanError(span, err)
	}

	syscall.Sync() // does not return an error in go 1.24 that we're compiling with

	zap.L().Info("Artifacts download finished!")
	return nil

}

// Fetch a artifact from NGC using a pre-signed URL and move to the target directory
func (d *ArtifactsDownloader) fetchAndInstallArtifact(ctx context.Context, artifact types.Artifact) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	ctx, span := otel.GetTracerProvider().Tracer("worker-init").Start(ctx, "Fetch and Install Artifact",
		trace.WithAttributes(attribute.String("name", artifact.Name), attribute.String("path", artifact.Path)))
	defer span.End()

	artifactName := utils.SanitizePath(artifact.Name)
	destPath := normalizeArtifactPath(d.baseRepo, artifact.Path)
	artifactDir := filepath.Join(d.baseRepo, artifactName)

	logger := zap.L().With(zap.String("name", artifactName), zap.String("path", artifact.Path))

	logger.Info("Fetching artifact")

	targetFilePath := filepath.Join(artifactDir, destPath)
	tmpFilePath := targetFilePath + ".tmp"

	{
		ctx, span := otel.GetTracerProvider().Tracer("worker-init").Start(ctx, "Fetching Artifact",
			trace.WithAttributes(attribute.String("name", artifact.Name), attribute.String("path", artifact.Path)))
		logger.Info("Fetching artifact")
		if err := d.downloadFileFromUrl(ctx, artifact.Url, tmpFilePath); err != nil {
			err := tracing.RecordSpanError(span, fmt.Errorf("failed to download artifact: %w", err))
			span.End()
			return err
		}
		span.End()
	}

	{
		_, span := otel.GetTracerProvider().Tracer("worker-init").Start(ctx, "Installing artifact",
			trace.WithAttributes(attribute.String("name", artifact.Name), attribute.String("path", artifact.Path)))
		logger.Info("Install artifact")
		// skipping dir creation. tmp file is created in same dir as target file.
		if err := os.Rename(tmpFilePath, targetFilePath); err != nil {
			err := tracing.RecordSpanError(span, fmt.Errorf("failed to move artifact: %w", err))
			span.End()
			return err
		}
		if err := os.Chmod(targetFilePath, 0644); err != nil {
			err := tracing.RecordSpanError(span, fmt.Errorf("failed change file permission: %w", err))
			span.End()
			return err
		}
		if err := d.updateArtifactManifest(artifactName, artifact.Url, destPath); err != nil {
			zap.L().Warn(
				"Failed to update artifact manifest",
				zap.String("name", artifactName),
				zap.String("path", destPath),
				zap.Error(err),
			)
		}
		span.End()
	}

	logger.Info("Successfully fetch and install artifact")
	return nil
}

// Check if an artifact is cached in a supplied repository
func (d *ArtifactsDownloader) isArtifactCached(ctx context.Context, artifact types.Artifact) (bool, error) {
	_, span := otel.GetTracerProvider().Tracer("worker-init").Start(ctx, "Check Artifact Cache")
	defer span.End()

	destPath := normalizeArtifactPath(d.baseRepo, artifact.Path)
	if destPath == "" {
		return false, nil
	}

	artifactName := utils.SanitizePath(artifact.Name)
	artifactDir := filepath.Join(d.baseRepo, artifactName)
	artifactPath := filepath.Join(artifactDir, destPath)
	if _, err := os.Stat(artifactPath); err != nil {
		return d.isArtifactCachedByManifest(artifactName, artifact)
	}
	// the file may be there, but if there is still a .tmp file it didn't finish downloading
	if _, err := os.Stat(artifactPath + ".tmp"); err == nil {
		return false, nil
	}

	return true, nil
}

func (d *ArtifactsDownloader) isArtifactCachedByManifest(artifactName string, artifact types.Artifact) (bool, error) {
	sourceKey := sourceKeyFromURL(artifact.Url)
	if sourceKey == "" {
		return false, fmt.Errorf("artifact %s missing source url", artifactName)
	}

	manifest, exists, err := readArtifactManifest(d.baseRepo)
	if err != nil {
		return false, err
	}
	if !exists {
		return false, nil
	}
	relPath, ok := manifest[sourceKey]
	if !ok || relPath == "" {
		if !d.isRepoWritable() {
			return false, fmt.Errorf("manifest missing entry for %s (%s)", artifactName, sourceKey)
		}
		zap.L().Warn(
			"Manifest missing entry; will attempt download",
			zap.String("name", artifactName),
			zap.String("source", sourceKey),
		)
		return false, nil
	}

	relPath = utils.SanitizePath(relPath)
	relPath = strings.TrimPrefix(relPath, string(os.PathSeparator))
	if relPath == "" || relPath == "." {
		return false, fmt.Errorf("manifest entry invalid for %s (%s)", artifactName, sourceKey)
	}

	manifestPath := filepath.Join(d.baseRepo, relPath)
	if _, err := os.Stat(manifestPath); err != nil {
		return false, err
	}
	if _, err := os.Stat(manifestPath + ".tmp"); err == nil {
		return false, nil
	}

	zap.L().Info(
		"Manifest mapping",
		zap.String("name", artifact.Name),
		zap.String("path", artifact.Path),
		zap.String("source", sourceKey),
		zap.String("cache_path", relPath),
	)
	return true, nil
}

func (d *ArtifactsDownloader) updateArtifactManifest(artifactName, artifactUrl, relPath string) error {
	sourceKey := sourceKeyFromURL(artifactUrl)
	if sourceKey == "" {
		return nil
	}

	relPath = utils.SanitizePath(relPath)
	relPath = strings.TrimPrefix(relPath, string(os.PathSeparator))
	if relPath == "" || relPath == "." {
		return nil
	}

	manifestRelPath := filepath.Join(artifactName, relPath)

	d.manifestLock.Lock()
	defer d.manifestLock.Unlock()

	manifest, _, err := readArtifactManifest(d.baseRepo)
	if err != nil {
		return err
	}
	manifest[sourceKey] = manifestRelPath

	return writeArtifactManifest(d.baseRepo, manifest)
}

func sourceKeyFromURL(rawUrl string) string {
	if rawUrl == "" {
		return ""
	}
	parsedUrl, err := url.Parse(rawUrl)
	if err != nil {
		return ""
	}
	path := strings.TrimPrefix(parsedUrl.Path, "/")
	if path == "" {
		return ""
	}
	versionID := parsedUrl.Query().Get("versionId")
	if versionID == "" {
		return path
	}
	return path + "?versionId=" + versionID
}

func readArtifactManifest(artifactDir string) (map[string]string, bool, error) {
	manifestPath := filepath.Join(artifactDir, ".nvcf_manifest.json")
	data, err := os.ReadFile(manifestPath)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, false, nil
	}
	if err != nil {
		return nil, false, err
	}

	var manifest map[string]string
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, false, err
	}
	if manifest == nil {
		manifest = map[string]string{}
	}
	return manifest, true, nil
}

func writeArtifactManifest(artifactDir string, manifest map[string]string) error {
	if manifest == nil {
		manifest = map[string]string{}
	}
	manifestPath := filepath.Join(artifactDir, ".nvcf_manifest.json")
	tmpPath := manifestPath + ".tmp"
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmpPath, manifestPath)
}

func (d *ArtifactsDownloader) isRepoWritable() bool {
	d.repoWritableOnce.Do(func() {
		if d.baseRepo == "" {
			d.repoWritable = false
			return
		}
		if _, err := os.Stat(d.baseRepo); err != nil {
			if err := os.MkdirAll(d.baseRepo, 0755); err != nil {
				d.repoWritable = false
				return
			}
		}
		tmpFile, err := os.CreateTemp(d.baseRepo, ".nvcf-writecheck-*")
		if err != nil {
			d.repoWritable = false
			return
		}
		_ = tmpFile.Close()
		_ = os.Remove(tmpFile.Name())
		d.repoWritable = true
	})
	return d.repoWritable
}

func normalizeArtifactPath(baseRepo, destPath string) string {
	if destPath == "" {
		return ""
	}

	sanitizedPath := utils.SanitizePath(destPath)
	if !filepath.IsAbs(sanitizedPath) {
		return sanitizedPath
	}

	if baseRepo == "" {
		return sanitizedPath
	}

	sanitizedBase := utils.SanitizePath(baseRepo)
	if sanitizedPath == sanitizedBase {
		return ""
	}

	prefix := sanitizedBase + string(os.PathSeparator)
	if !strings.HasPrefix(sanitizedPath, prefix) {
		return sanitizedPath
	}

	rel := strings.TrimPrefix(sanitizedPath, prefix)
	rel = filepath.Clean(rel)
	rel = strings.TrimPrefix(rel, string(os.PathSeparator))
	if rel == "." || rel == "" {
		return ""
	}

	parts := strings.Split(rel, string(os.PathSeparator))
	if len(parts) > 1 {
		return filepath.Join(parts[1:]...)
	}
	return rel
}

func (d *ArtifactsDownloader) downloadFileFromUrl(ctx context.Context, url, targetFilePath string) error {
	// Make directory and target file
	targetFileDir := filepath.Dir(targetFilePath)
	_, err := os.Stat(targetFileDir)
	if errors.Is(err, os.ErrNotExist) {
		err = utils.CreateDirectory(targetFileDir, os.FileMode(0755))
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	targetFile, err := os.Create(targetFilePath)
	if err != nil {
		return err
	}
	defer utils.Close(targetFile.Close)

	chunks, err := d.fetchFirstChunkWithRetry(ctx, url, targetFile)
	if err != nil {
		return err
	}

	eg, ctx := errgroup.WithContext(ctx)
	eg.SetLimit(d.maxSingleFileConcurrency)
	for i := 1; i < int(chunks); i++ {
		if ctx.Err() != nil {
			break
		}
		chunkNumber := i
		eg.Go(func() error {
			return d.fetchChunkWithRetry(ctx, url, targetFile, chunkNumber)
		})
	}
	return eg.Wait()
}

func (d *ArtifactsDownloader) fetchFirstChunkWithRetry(ctx context.Context, url string, targetFile io.Writer) (int64, error) {
	var totalChunks int64
	err := backoff.Retry(func() error {
		// fetchFirstChunk writes via the file cursor, so a mid-stream
		// failure leaves it past offset 0. Reset before each attempt or
		// the retry appends fresh bytes after the partial garbage.
		if f, ok := targetFile.(*os.File); ok {
			if err := f.Truncate(0); err != nil {
				return backoff.Permanent(err)
			}
			if _, err := f.Seek(0, io.SeekStart); err != nil {
				return backoff.Permanent(err)
			}
		}
		tc, err := d.fetchFirstChunk(ctx, url, targetFile)
		totalChunks = tc
		return err
	}, backoff.WithContext(backoff.WithMaxRetries(backoff.NewExponentialBackOff(), retryCount), ctx))
	return totalChunks, err
}

// fetchFirstChunk returns the total chunk count of the file, or 0 if ranges were not supported and the first chunk was the whole file
func (d *ArtifactsDownloader) fetchFirstChunk(ctx context.Context, url string, targetFile io.Writer) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", d.chunkSize-1))
	resp, err := d.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer utils.Close(resp.Body.Close)

	var totalChunks int64
	switch resp.StatusCode {
	case http.StatusOK: // server doesn't support ranges. download the whole file.
		// leaving totalChunks 0 to cause no further chunks to be downloaded
		break
	case http.StatusPartialContent:
		totalSize, err := parseRangeTotalLength(resp)
		if err != nil {
			return 0, err
		}
		totalChunks = totalSize / int64(d.chunkSize)
		if totalSize%int64(d.chunkSize) > 0 {
			totalChunks++
		}
	case http.StatusRequestedRangeNotSatisfiable:
		// range didn't work for some reason, try again without sending the range header
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return 0, err
		}
		resp, err = d.client.Do(req)
		if err != nil {
			return 0, err
		}
		defer utils.Close(resp.Body.Close)
		if resp.StatusCode == http.StatusOK {
			break
		}
		fallthrough
	default:
		zap.L().Error("non-2xx response while downloading first chunk",
			zap.String("file path", redactURL(url)),
			zap.Int("status code", resp.StatusCode),
			zap.ByteString("response", errBody(resp)))
		d.dropConnection(resp)
		return totalChunks, fmt.Errorf("failed to fetch first chunk - status: %d", resp.StatusCode)
	}

	writtenBytes, err := io.Copy(targetFile, resp.Body)
	initMetrics.WorkerInitArtifactBytesCounter.Add(float64(writtenBytes))
	return totalChunks, err
}

// best effort to not reuse this connection without having to drop the entire http client.
// dropping the http client would force all connections to be dropped rather than just this one.
//
// if this connection is already reused in-between Body.Close and CloseIdleConnections runs
// this function will silently fail.
func (d *ArtifactsDownloader) dropConnection(resp *http.Response) {
	_ = resp.Body.Close()
	d.client.CloseIdleConnections()
}

// best effort body, but capped in case of a misbehaving server
func errBody(resp *http.Response) []byte {
	resBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*4))
	return resBody
}

// redactURL strips credentials (query params and userinfo) from a pre-signed
// download URL so it is safe to log. Never returns the raw credential-bearing
// value: an unparseable URL collapses to a placeholder.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "REDACTED"
	}
	u.User = nil
	if u.RawQuery != "" {
		u.RawQuery = "REDACTED"
	}
	return u.String()
}

func (d *ArtifactsDownloader) fetchChunkWithRetry(ctx context.Context, url string, targetFile io.WriterAt, chunkNumber int) error {

	return backoff.Retry(func() error {
		return d.fetchChunk(ctx, url, targetFile, chunkNumber)
	}, backoff.WithContext(backoff.WithMaxRetries(backoff.NewExponentialBackOff(), retryCount), ctx))
}

func (d *ArtifactsDownloader) fetchChunk(ctx context.Context, url string, targetFile io.WriterAt, chunkNumber int) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	start := chunkNumber * d.chunkSize
	end := (chunkNumber+1)*d.chunkSize - 1
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))

	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer utils.Close(resp.Body.Close)

	if resp.StatusCode != http.StatusPartialContent {
		zap.L().Error("non-206 response while downloading chunk",
			zap.String("file path", redactURL(url)),
			zap.Int("status code", resp.StatusCode),
			zap.ByteString("response", errBody(resp)))
		d.dropConnection(resp)
		return fmt.Errorf("failed to fetch chunk - status: %d", resp.StatusCode)
	}

	writtenBytes, err := io.Copy(io.NewOffsetWriter(targetFile, int64(start)), resp.Body)
	initMetrics.WorkerInitArtifactBytesCounter.Add(float64(writtenBytes))
	return err
}

func parseRangeTotalLength(resp *http.Response) (int64, error) {
	contentLengthHeader := resp.Header.Get("Content-Range")
	if contentLengthHeader == "" {
		return 0, errors.New("content range not found")
	}
	parts := strings.Split(contentLengthHeader, "/")
	if len(parts) != 2 {
		return 0, errors.New("invalid content range")
	}
	return strconv.ParseInt(parts[1], 10, 64)
}
