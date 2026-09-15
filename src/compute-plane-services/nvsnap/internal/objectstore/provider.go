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

package objectstore

import (
	"context"
	"errors"
	"fmt"
)

// ErrDisabled is returned by NewBucket when the provider is empty, i.e.
// replication is disabled. Callers treat this as "no object store; skip the
// push/pull paths" rather than a hard error.
var ErrDisabled = errors.New("objectstore: provider not configured")

// NewBucket opens a provider-neutral object-store client and returns a
// handle to the named bucket plus a closer (call on shutdown to release the
// shared client). provider selects the backend:
//
//	"gcs" → Google Cloud Storage (ADC / Workload Identity)
//	"s3"  → not yet implemented
//	""    → disabled (returns ErrDisabled)
//
// The returned closer is non-nil only on success; on any error it is nil.
func NewBucket(ctx context.Context, provider, name string) (Bucket, func() error, error) {
	switch provider {
	case "":
		return nil, nil, ErrDisabled
	case "gcs":
		cl, closeFn, err := NewGCS(ctx)
		if err != nil {
			return nil, nil, err
		}
		return cl.Bucket(name), closeFn, nil
	case "s3":
		return nil, nil, errors.New("objectstore: s3 provider not yet implemented")
	default:
		return nil, nil, fmt.Errorf("objectstore: unknown provider %q", provider)
	}
}
