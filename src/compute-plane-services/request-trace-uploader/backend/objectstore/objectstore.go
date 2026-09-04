// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package objectstore implements a generic S3-compatible export backend. It
// carries no NVIDIA-internal dependencies, so it links into any distribution.
package objectstore

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/config"
)

// maxObjectBytes is the S3 single-PutObject limit. A segment past this size
// needs multipart upload, which is out of scope for this increment.
const maxObjectBytes = 5 * 1024 * 1024 * 1024

func init() {
	backend.Register(config.BackendObjectStore, New)
}

// Client uploads segments to a generic S3-compatible object store with one
// synchronous PutObject call per segment.
type Client struct {
	s3     *s3.Client
	bucket string
	prefix string
}

// credentials is the secrets-file shape this backend reads. The chart never
// carries these values; they arrive through a mounted secret.
type credentials struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	SessionToken    string `json:"session_token"`
}

// New builds the object-store backend from the loaded configuration. It fails
// fast on a missing bucket, region, or credential so a wiring mistake reports
// at startup rather than at the first upload.
func New(cfg config.Config) (backend.Client, error) {
	if strings.TrimSpace(cfg.ObjectStore.Bucket) == "" {
		return nil, fmt.Errorf("%s is required for the objectstore backend", config.EnvObjectStoreBucket)
	}
	if strings.TrimSpace(cfg.ObjectStore.Region) == "" {
		return nil, fmt.Errorf("%s is required for the objectstore backend", config.EnvObjectStoreRegion)
	}
	creds, err := loadCredentials(cfg.SecretsFile)
	if err != nil {
		return nil, err
	}

	options := s3.Options{
		Region: cfg.ObjectStore.Region,
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{
				AccessKeyID:     creds.AccessKeyID,
				SecretAccessKey: creds.SecretAccessKey,
				SessionToken:    creds.SessionToken,
			}, nil
		}),
		UsePathStyle: cfg.ObjectStore.PathStyle,
	}
	if cfg.ObjectStore.Endpoint != "" {
		options.BaseEndpoint = aws.String(cfg.ObjectStore.Endpoint)
	}

	return &Client{
		s3:     s3.New(options),
		bucket: cfg.ObjectStore.Bucket,
		prefix: cfg.ObjectStore.KeyPrefix,
	}, nil
}

func loadCredentials(path string) (credentials, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return credentials{}, fmt.Errorf("objectstore backend: read credentials: %w", err)
	}
	var creds credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return credentials{}, fmt.Errorf("objectstore backend: parse credentials: %w", err)
	}
	if creds.AccessKeyID == "" || creds.SecretAccessKey == "" {
		return credentials{}, fmt.Errorf("objectstore backend: credentials require access_key_id and secret_access_key")
	}
	return creds, nil
}

// Submit uploads the segment at request.Path and returns once the store has
// durably accepted it. The returned id is the object key: Status looks
// nothing up because Capabilities declares TerminalOutcomeSync.
func (c *Client) Submit(ctx context.Context, request backend.SubmitRequest) (string, error) {
	info, err := os.Stat(request.Path)
	if err != nil {
		return "", fmt.Errorf("objectstore backend: stat segment: %w", err)
	}
	if info.Size() > maxObjectBytes {
		return "", fmt.Errorf("objectstore backend: segment is %d bytes, over the %d byte single-object limit", info.Size(), maxObjectBytes)
	}

	file, err := os.Open(request.Path)
	if err != nil {
		return "", fmt.Errorf("objectstore backend: open segment: %w", err)
	}
	defer file.Close()

	key := c.key(request.Path)
	if _, err := c.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(c.bucket),
		Key:           aws.String(key),
		Body:          file,
		ContentLength: aws.Int64(info.Size()),
		ContentType:   aws.String("application/gzip"),
	}); err != nil {
		return "", fmt.Errorf("objectstore backend: upload segment: %w", err)
	}
	return key, nil
}

// Status always reports success. PutObject in Submit already blocked until
// the store returned a durable response, so there is nothing left to poll.
func (c *Client) Status(context.Context, string) (backend.Status, error) {
	return backend.StatusSuccess, nil
}

// Capabilities declares that PutObject is a single synchronous, idempotent
// call: a Submit that returns success is already durable, resubmitting the
// same segment overwrites the same key, and segments are independent objects
// so nothing requires in-order delivery.
func (c *Client) Capabilities() backend.Capabilities {
	return backend.Capabilities{
		ResubmitSafe:        true,
		TerminalOutcomeSync: true,
		OutOfOrderTolerant:  true,
		AcceptedFormats:     []backend.Format{backend.FormatGzipJSONL},
		MaxObjectBytes:      maxObjectBytes,
		Exports:             true,
	}
}

func (c *Client) key(sourcePath string) string {
	name := filepath.Base(sourcePath)
	if c.prefix == "" {
		return name
	}
	return path.Join(c.prefix, name)
}
