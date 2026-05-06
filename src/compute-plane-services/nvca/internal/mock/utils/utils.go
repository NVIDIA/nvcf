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

package utils //nolint:revive

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"os"

	"github.com/gorilla/handlers"
	"github.com/sirupsen/logrus"
)

func DecodePrivateKeyRSA(kf string) (*rsa.PrivateKey, error) {
	pf, err := os.ReadFile(kf)
	if err != nil {
		return nil, err
	}
	b, _ := pem.Decode(pf)
	if b == nil {
		return nil, fmt.Errorf("could not decode %s", kf)
	}

	privKey, err := x509.ParsePKCS8PrivateKey(b.Bytes)
	if err != nil {
		return nil, err
	}

	priv, ok := privKey.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("expected private key to be RSA, got: %T", privKey)
	}

	return priv, nil
}

func NewCustomLogFormatter(log logrus.FieldLogger) handlers.LogFormatter {
	return func(_ io.Writer, params handlers.LogFormatterParams) {
		log.WithFields(logrus.Fields{
			"path":   params.Request.URL.EscapedPath(),
			"method": params.Request.Method,
			"code":   params.StatusCode,
			"size":   params.Size,
		}).Debug("Response")
	}
}
