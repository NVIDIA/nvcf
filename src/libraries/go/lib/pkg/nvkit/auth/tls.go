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

package auth

import (
	"crypto/tls"
	"crypto/x509"
	"os"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/errors"
)

type TLSConfigOptions struct {
	Enabled            bool     `mapstructure:"enabled"`
	CertFile           string   `mapstructure:"cert-file,omitempty"`
	KeyFile            string   `mapstructure:"key-file,omitempty"`
	ClientCACertFiles  []string `mapstructure:"client-ca-cert-file,omitempty"`
	RootCAFile         string   `mapstructure:"root-ca-file,omitempty"`
	InsecureSkipVerify bool     `mapstructure:"insecure-skip-verify"`

	ClientCAPool *x509.CertPool   `json:"-"`
	RootCAPool   *x509.CertPool   `json:"-"`
	Cert         *tls.Certificate `json:"-"`
}

func (opts *TLSConfigOptions) Certificate() (*tls.Certificate, error) {
	if opts.Enabled && opts.Cert == nil {
		if opts.CertFile == "" || opts.KeyFile == "" {
			return nil, errors.ErrCertAndKeyRequired
		}
		cert, err := tls.LoadX509KeyPair(opts.CertFile, opts.KeyFile)
		if err != nil {
			return nil, err
		}
		opts.Cert = &cert
	}
	return opts.Cert, nil
}

func (opts *TLSConfigOptions) LoadClientCAPool() (*x509.CertPool, error) {
	var err error
	if opts.Enabled && opts.ClientCAPool == nil {
		opts.ClientCAPool, err = x509.SystemCertPool()
		if err != nil {
			return nil, err
		}

		for _, clientCAFile := range opts.ClientCACertFiles {
			data, err := os.ReadFile(clientCAFile)
			if err != nil {
				return nil, err
			}
			opts.ClientCAPool.AppendCertsFromPEM(data)
		}
	}
	return opts.ClientCAPool, err
}

func (opts *TLSConfigOptions) LoadRootCAPool() (*x509.CertPool, error) {
	var err error
	if opts.Enabled && opts.RootCAPool == nil {
		if opts.RootCAFile != "" {
			opts.RootCAPool = x509.NewCertPool()
			data, err := os.ReadFile(opts.RootCAFile)
			if err != nil {
				return nil, err
			}
			opts.RootCAPool.AppendCertsFromPEM(data)
		} else {
			opts.RootCAPool, err = x509.SystemCertPool()
			if err != nil {
				return nil, err
			}
		}
	}
	return opts.RootCAPool, err
}

func (opts *TLSConfigOptions) ServerTLSConfig() (*tls.Config, error) {
	if !opts.Enabled {
		return nil, nil
	}

	cert, err := opts.Certificate()
	if err != nil {
		return nil, err
	}

	// Load the pool of CA certs used to check client certificates
	pool, err := opts.LoadClientCAPool()
	if err != nil {
		return nil, err
	}

	return &tls.Config{
		Certificates: []tls.Certificate{*cert},
		ClientCAs:    pool,
		MinVersion:   tls.VersionTLS12,
	}, nil
}

func (opts *TLSConfigOptions) ClientTLSConfig() (*tls.Config, error) {
	if !opts.Enabled {
		return nil, nil
	}

	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}

	tlsConfig.InsecureSkipVerify = opts.InsecureSkipVerify

	cert, err := opts.Certificate()
	if err != nil && err != errors.ErrCertAndKeyRequired {
		return nil, err
	}
	if err != errors.ErrCertAndKeyRequired {
		tlsConfig.Certificates = []tls.Certificate{*cert}
	}

	caPool, err := opts.LoadRootCAPool()
	if err != nil {
		return nil, err
	}
	tlsConfig.RootCAs = caPool

	return tlsConfig, nil
}
