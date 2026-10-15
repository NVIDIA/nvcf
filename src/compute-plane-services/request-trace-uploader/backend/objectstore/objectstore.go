// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package objectstore implements a generic S3-compatible export backend. It
// carries no NVIDIA-internal dependencies, so it links into any distribution.
package objectstore

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
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
	// s3 is nil in dry-run mode: Submit logs the computed bucket, key, and
	// size instead of calling the store.
	s3     *s3.Client
	bucket string
	prefix string
	// source namespaces the object key by uploader instance. Without it, two
	// instances sharing a bucket and prefix could produce the same key from
	// two different segments: segment.Discover indexes restart from zero
	// independently per instance, so a bare file name is not globally unique.
	source string
	// dryRun computes and logs what Submit would upload without calling the
	// store or requiring credentials. See Capabilities: a dry run never
	// exports, so its source segments are never deleted.
	dryRun bool
}

// hostname resolves the identifier used to namespace object keys per
// uploader instance. A var so a test can substitute it without depending on
// the real host. In production this backend always runs as a single
// container in its own pod, so the pod hostname is a stable per-instance
// value with no additional chart or config wiring.
var hostname = os.Hostname

// credentials is the secrets-file shape this backend reads. The chart never
// carries these values; they arrive through a mounted secret.
type credentials struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	SessionToken    string `json:"session_token"`
}

// New builds the object-store backend from the loaded configuration. It fails
// fast on a missing bucket, region, or credential, an unresolvable hostname,
// or a non-https endpoint, so a wiring mistake reports at startup rather than
// at the first upload. config.Load already rejects a non-https endpoint, but
// New enforces it again: a config.Config can also be constructed directly,
// bypassing Load.
//
// DryRun skips the credential requirement: it never calls the store, so it
// never authenticates to one. Bucket, region, and the endpoint scheme are
// still validated, so a dry run exercises the same configuration a real
// upload would.
func New(cfg config.Config) (backend.Client, error) {
	return newClient(cfg, nil)
}

// newClient builds the backend with an optional transport override, so a test
// can point it at an httptest.Server whose TLS certificate the default
// transport would not trust. A nil transport uses the SDK default. It is
// unused in dry-run mode, which never constructs an S3 client.
//
// The resulting HTTP client always rejects a redirect to a non-https URL,
// regardless of the transport: an https:// endpoint only bounds the first
// request, and a compromised or misconfigured store could otherwise use a
// 307 or 308 response to downgrade a PUT to cleartext.
func newClient(cfg config.Config, transport http.RoundTripper) (backend.Client, error) {
	if strings.TrimSpace(cfg.ObjectStore.Bucket) == "" {
		return nil, fmt.Errorf("%s is required for the objectstore backend", config.EnvObjectStoreBucket)
	}
	if strings.TrimSpace(cfg.ObjectStore.Region) == "" {
		return nil, fmt.Errorf("%s is required for the objectstore backend", config.EnvObjectStoreRegion)
	}
	if !config.ValidObjectStoreEndpoint(cfg.ObjectStore.Endpoint) {
		return nil, fmt.Errorf("%s must be an absolute https:// URL with a host; a non-https or hostless endpoint is invalid", config.EnvObjectStoreEndpoint)
	}
	source, err := hostname()
	if err != nil {
		return nil, fmt.Errorf("objectstore backend: read hostname to namespace object keys: %w", err)
	}

	if cfg.ObjectStore.DryRun {
		return &Client{
			bucket: cfg.ObjectStore.Bucket,
			prefix: cfg.ObjectStore.KeyPrefix,
			source: source,
			dryRun: true,
		}, nil
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
	options.HTTPClient = &http.Client{
		Transport:     transport,
		CheckRedirect: rejectNonHTTPSRedirect,
	}

	return &Client{
		s3:     s3.New(options),
		bucket: cfg.ObjectStore.Bucket,
		prefix: cfg.ObjectStore.KeyPrefix,
		source: source,
	}, nil
}

// rejectNonHTTPSRedirect replaces http.Client's default redirect policy.
// Setting a non-nil CheckRedirect drops that default's 10-redirect cap, so
// this replicates it in addition to the scheme check.
func rejectNonHTTPSRedirect(req *http.Request, via []*http.Request) error {
	if req.URL.Scheme != "https" {
		return fmt.Errorf("objectstore backend: refusing a redirect to a non-https URL: %s", req.URL)
	}
	if len(via) >= 10 {
		return fmt.Errorf("objectstore backend: stopped after %d redirects", len(via))
	}
	return nil
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
//
// In dry-run mode, Submit stats the segment, computes the key, logs both
// along with the bucket and size, and returns without opening the file or
// contacting a store.
func (c *Client) Submit(ctx context.Context, request backend.SubmitRequest) (string, error) {
	info, err := os.Stat(request.Path)
	if err != nil {
		return "", fmt.Errorf("objectstore backend: stat segment: %w", err)
	}
	if info.Size() > maxObjectBytes {
		return "", fmt.Errorf("objectstore backend: segment is %d bytes, over the %d byte single-object limit", info.Size(), maxObjectBytes)
	}

	key := c.key(request.Path)
	if c.dryRun {
		slog.Info("objectstore backend: dry run, not uploading",
			"segment", request.Segment.Index,
			"bucket", c.bucket,
			"key", key,
			"bytes", info.Size())
		return key, nil
	}

	file, err := os.Open(request.Path)
	if err != nil {
		return "", fmt.Errorf("objectstore backend: open segment: %w", err)
	}
	defer func() {
		if cerr := file.Close(); cerr != nil {
			slog.Warn("objectstore backend: close segment after upload", "segment", request.Segment.Index, "error", cerr)
		}
	}()

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
//
// Exports is false in dry-run mode: nothing was sent anywhere, so a caller
// must never delete a source segment on the strength of a dry-run Submit
// succeeding, the same rule debug's Capabilities enforces for its own reads.
func (c *Client) Capabilities() backend.Capabilities {
	return backend.Capabilities{
		ResubmitSafe:        true,
		TerminalOutcomeSync: true,
		OutOfOrderTolerant:  true,
		AcceptedFormats:     []backend.Format{backend.FormatGzipJSONL},
		MaxObjectBytes:      maxObjectBytes,
		Exports:             !c.dryRun,
	}
}

func (c *Client) key(sourcePath string) string {
	name := filepath.Base(sourcePath)
	if c.prefix == "" {
		return path.Join(c.source, name)
	}
	return path.Join(c.prefix, c.source, name)
}
