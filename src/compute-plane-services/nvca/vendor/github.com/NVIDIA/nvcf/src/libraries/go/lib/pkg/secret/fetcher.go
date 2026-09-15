/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package secret

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
)

// TokenFetcher represents a struct that can fetch tokens for clients
type TokenFetcher interface {
	// FetchToken returns the token in the cache if it think the token is
	// valid, otherwise, it tries to fetch a new token and update cache
	// first.
	FetchToken(ctx context.Context) (string, error)
}

type KeyFileFetcherOption func(*KeyFileFetcher)

type KeyFileFetcher struct {
	inner *FileFetcher

	secretKey       string
	secretKeyEnvVar string
	secretKeyFile   string

	fileFetcherOpts []FileFetcherOption
}

// WithSecretKey provides a default secret key to use
// if the file is not specified
func WithSecretKey(secretKey string) KeyFileFetcherOption {
	return func(ff *KeyFileFetcher) {
		ff.secretKey = secretKey
	}
}

// WithSecretKeyEnvVar specifies the environment variable the secret
// key file may be found in for error messages
func WithSecretKeyEnvVar(secretKeyEnvVar string) KeyFileFetcherOption {
	return func(ff *KeyFileFetcher) {
		ff.secretKeyEnvVar = secretKeyEnvVar
	}
}

// WithSecretKeyFile specifies the secret key file to read from
func WithSecretKeyFile(secretKeyFile string) KeyFileFetcherOption {
	return func(ff *KeyFileFetcher) {
		ff.secretKeyFile = secretKeyFile
	}
}

// WithKeyFileFetcherOptions forwards FileFetcher options when key file mode is used.
func WithKeyFileFetcherOptions(opts ...FileFetcherOption) KeyFileFetcherOption {
	return func(ff *KeyFileFetcher) {
		ff.fileFetcherOpts = append(ff.fileFetcherOpts, opts...)
	}
}

// WithOnKeyFileUpdateListener sets a callback function to be called when the key file is updated
func WithOnKeyFileUpdateListener(f func(context.Context, io.Reader)) KeyFileFetcherOption {
	return WithKeyFileFetcherOptions(WithOnFileUpdateListener(f))
}

func NewKeyFileFetcher(ctx context.Context, opts ...KeyFileFetcherOption) (*KeyFileFetcher, error) {
	fetcher := &KeyFileFetcher{
		inner: newFileFetcher(),
	}
	// Set options on fetcher if any
	for _, o := range opts {
		o(fetcher)
	}

	// If any are empty fail
	if fetcher.secretKey == "" && fetcher.secretKeyEnvVar == "" && fetcher.secretKeyFile == "" {
		return nil, fmt.Errorf("one of secret key, env var, or file path must be provided")
	}

	// If the API Key file was specified start a goroutine to read from that file
	// if the ngcServiceAPIKey was specified it will be overwritten immediately
	if fetcher.secretKeyFile != "" {
		fetcher.secretKey = ""
		inner, err := NewFileFetcher(ctx, fetcher.secretKeyFile, fetcher.fileFetcherOpts...)
		if err != nil {
			return nil, err
		}
		fetcher.inner = inner
	}

	// TODO(mcamp) add verification that the key is a service key and not simply
	// a user API key

	return fetcher, nil
}

// FetchSecretKey fetches the token stored in the fetcher struct
func (fetcher *KeyFileFetcher) FetchSecretKey(ctx context.Context) (string, error) {
	secretKey := fetcher.secretKey
	var fetchErr error
	switch {
	case fetcher.secretKeyFile != "":
		if secretKeyBytes, err := fetcher.inner.FetchData(ctx); err == nil {
			secretKey = string(secretKeyBytes)
		} else {
			fetchErr = err
		}
	case fetcher.secretKeyEnvVar != "":
		secretKey = os.Getenv(fetcher.secretKeyEnvVar)
	}

	if secretKey == "" {
		if fetchErr == nil {
			fetchErr = errors.New("the provided Secret key is empty")
		}
		core.GetLogger(ctx).Error(fetchErr)
		return "", fetchErr
	}
	return secretKey, nil
}

// FetchToken is an adapter for downstream consumers to use this directly
func (fetcher *KeyFileFetcher) FetchToken(ctx context.Context) (string, error) {
	return fetcher.FetchSecretKey(ctx)
}
