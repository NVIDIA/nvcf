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
	"bytes"
	"context"
	"fmt"
	"io"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/types"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/utils"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go/rand"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/localstack"
)

func TestArtifactsDownloader_DownloadArtifacts(t *testing.T) {
	// TODO why doesn't docker work in jenkins?
	func() {
		defer func() {
			err := recover()
			if err != nil {
				t.Skip("panic while setting up docker", err)
			}
		}()
		testcontainers.SkipIfProviderIsNotHealthy(t)
	}()
	container := setupLocalstack(t)

	ctx := context.Background()
	s3Client := lo.Must(newS3Client(ctx, container))
	bucketName := "test-bucket"
	_, err := s3Client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: &bucketName})
	if err != nil {
		t.Fatal(err)
	}

	var artifacts []types.Artifact
	var artifactContents [][]byte
	sizes := []int64{
		1024,             // smaller than 5MB chunks
		utils.OneMB * 5,  // same size as a chunk
		utils.OneMB * 20, // multiple chunks
		utils.OneMB * 24, // chunk at end is not an even chunk size
	}
	for _, size := range sizes {
		contents, artifact := generateArtifact(t, ctx, s3Client, bucketName, size)
		artifacts = append(artifacts, artifact)
		artifactContents = append(artifactContents, contents)
	}

	downloadDir := t.TempDir()
	downloader := NewArtifactDownloader(downloadDir, 2, 2, 5*utils.OneMB)
	err = downloader.DownloadArtifacts(ctx, artifacts)
	if err != nil {
		t.Fatal(err)
	}
	for i, artifact := range artifacts {
		downloadedFile, err := os.ReadFile(filepath.Join(downloadDir, artifact.Name, artifact.Path))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(downloadedFile, artifactContents[i]) {
			t.Fatal("downloaded file does not match original file")
		} else {
			t.Logf("validated artifact download of size %d", len(downloadedFile))
		}
	}
}

func TestNormalizeArtifactPath(t *testing.T) {
	baseRepo := "/config/models"
	testCases := []struct {
		name     string
		path     string
		expected string
	}{
		{
			name:     "strip mount segment",
			path:     "/config/models/a/model_a.txt",
			expected: "model_a.txt",
		},
		{
			name:     "no mount segment",
			path:     "/config/models/model_a.txt",
			expected: "model_a.txt",
		},
		{
			name:     "nested under mount",
			path:     "/config/models/b/1/model.graphdef",
			expected: "1/model.graphdef",
		},
		{
			name:     "relative path unchanged",
			path:     "model_a.txt",
			expected: "model_a.txt",
		},
		{
			name:     "absolute path outside base repo unchanged",
			path:     "/other/path/model_a.txt",
			expected: "/other/path/model_a.txt",
		},
		{
			name:     "pvc model path aa",
			path:     "/config/models/aa/model_a.txt",
			expected: "model_a.txt",
		},
		{
			name:     "pvc model path cc",
			path:     "/config/models/cc/sample_file_0.txt",
			expected: "sample_file_0.txt",
		},
		{
			name:     "qa model mount bb",
			path:     "/config/models/bb/1/model.graphdef",
			expected: "1/model.graphdef",
		},
		{
			name:     "qa resource mount",
			path:     "/config/resources/tadiathdfetp-resource_nvcf_qa_0.1/resource_a.txt",
			expected: "/config/resources/tadiathdfetp-resource_nvcf_qa_0.1/resource_a.txt",
		},
		{
			name:     "multi-segment mount under models",
			path:     "/config/models/aa/bb/foo.bin",
			expected: "bb/foo.bin",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			result := normalizeArtifactPath(baseRepo, testCase.path)
			if result != testCase.expected {
				t.Fatalf("normalizeArtifactPath(%q) = %q, expected %q", testCase.path, result, testCase.expected)
			}
		})
	}
}

func TestArtifactsDownloader_CacheFallbackManifest(t *testing.T) {
	ctx := context.Background()
	downloadDir := t.TempDir()
	downloader := NewArtifactDownloader(downloadDir, 1, 1, 5*utils.OneMB)

	artifact := types.Artifact{
		Name: "aa",
		Path: "sample_file_0.txt",
		Url:  "https://storage.example.com/org/test-org/models/a/versions/output_result_0_43d310f5-3b20-47db-9f8a-666a3e56b6e8/files/sample_file_0.txt?versionId=abc123&Signature=ignored",
	}

	artifactDir := filepath.Join(downloadDir, artifact.Name)
	if err := utils.CreateDirectory(artifactDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifactDir, "model_a.txt"), []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]string{
		"org/test-org/models/a/versions/output_result_0_43d310f5-3b20-47db-9f8a-666a3e56b6e8/files/sample_file_0.txt?versionId=abc123": filepath.Join("aa", "model_a.txt"),
	}
	if err := writeArtifactManifest(downloadDir, manifest); err != nil {
		t.Fatal(err)
	}

	isCached, err := downloader.isArtifactCached(ctx, artifact)
	if err != nil {
		t.Fatal(err)
	}
	if !isCached {
		t.Fatalf("expected cache fallback to treat artifact as cached via manifest mapping")
	}
}

func TestArtifactsDownloader_CacheFallbackManifestMissingEntry(t *testing.T) {
	ctx := context.Background()
	downloadDir := t.TempDir()
	downloader := NewArtifactDownloader(downloadDir, 1, 1, 5*utils.OneMB)

	artifact := types.Artifact{
		Name: "aa",
		Path: "sample_file_0.txt",
		Url:  "https://storage.example.com/org/test-org/models/a/versions/output_result_0_43d310f5-3b20-47db-9f8a-666a3e56b6e8/files/sample_file_0.txt?versionId=abc123&Signature=ignored",
	}

	artifactDir := filepath.Join(downloadDir, artifact.Name)
	if err := utils.CreateDirectory(artifactDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifactDir, "model_a.txt"), []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := writeArtifactManifest(downloadDir, map[string]string{}); err != nil {
		t.Fatal(err)
	}

	isCached, err := downloader.isArtifactCached(ctx, artifact)
	if err != nil {
		t.Fatalf("expected no error when repo is writable, got %v", err)
	}
	if isCached {
		t.Fatalf("expected cache miss when manifest entry is missing on writable repo")
	}
}

func TestArtifactsDownloader_CacheFallbackManifestMissingEntryReadOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("read-only permission test not supported on windows")
	}

	ctx := context.Background()
	tmpDir := t.TempDir()
	baseRepoFile := filepath.Join(tmpDir, "base-repo-file")
	if err := os.WriteFile(baseRepoFile, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	downloader := NewArtifactDownloader(baseRepoFile, 1, 1, 5*utils.OneMB)

	artifact := types.Artifact{
		Name: "ee",
		Path: "resource_a.txt",
		Url:  "https://storage.example.com/org/test-org/recipes/resource_nvcf_qa/versions/0.1/files/resource_a.txt?versionId=abc123&Signature=ignored",
	}

	_, err := downloader.isArtifactCached(ctx, artifact)
	if err == nil {
		t.Fatalf("expected error when manifest entry is missing on read-only repo")
	}
}

func generateArtifact(t *testing.T, ctx context.Context, client *s3.Client, bucketName string, size int64) ([]byte, types.Artifact) {
	artifactContents := lo.Must(io.ReadAll(io.LimitReader(rand.Reader, size)))
	artifactName := uuid.New().String()
	_, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &bucketName,
		Key:    &artifactName,
		Body:   bytes.NewReader(artifactContents),
	})
	if err != nil {
		t.Fatal(err)
	}
	presignClient := s3.NewPresignClient(client)
	artifactUrl, err := presignClient.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: &bucketName,
		Key:    &artifactName,
	})
	if err != nil {
		t.Fatal(err)
	}
	artifact := types.Artifact{
		Name:    "model-1",
		Version: "123",
		Path:    artifactName,
		Url:     artifactUrl.URL,
	}
	return artifactContents, artifact
}

func setupLocalstack(t *testing.T) *localstack.LocalStackContainer {
	container, err := localstack.Run(context.Background(), "localstack/localstack:3.8.1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		err := container.Terminate(context.Background())
		if err != nil {
			t.Fatal(err)
		}
	})
	return container
}

func newS3Client(ctx context.Context, l *localstack.LocalStackContainer) (*s3.Client, error) {
	mappedPort, err := l.MappedPort(ctx, "4566/tcp")
	if err != nil {
		return nil, err
	}

	provider, err := testcontainers.NewDockerProvider()
	if err != nil {
		return nil, err
	}
	defer provider.Close()

	host, err := provider.DaemonHost(ctx)
	if err != nil {
		return nil, err
	}

	awsCfg, err := config.LoadDefaultConfig(context.TODO(),
		config.WithRegion("us-east-1"),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("abc", "def", "")),
	)
	if err != nil {
		return nil, err
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.UsePathStyle = true
		o.BaseEndpoint = aws.String(fmt.Sprintf("http://%s:%s", host, mappedPort.Port()))
	})

	return client, nil
}
