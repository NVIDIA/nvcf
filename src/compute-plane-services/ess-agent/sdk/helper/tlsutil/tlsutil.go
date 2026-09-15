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

// DEPRECATED: this has been moved to go-secure-stdlib and will be removed
package tlsutil

import (
	"crypto/tls"

	exttlsutil "github.com/hashicorp/go-secure-stdlib/tlsutil"
)

var ErrInvalidCertParams = exttlsutil.ErrInvalidCertParams

var TLSLookup = exttlsutil.TLSLookup

func ParseCiphers(cipherStr string) ([]uint16, error) {
	return exttlsutil.ParseCiphers(cipherStr)
}

func GetCipherName(cipher uint16) (string, error) {
	return exttlsutil.GetCipherName(cipher)
}

func ClientTLSConfig(caCert []byte, clientCert []byte, clientKey []byte) (*tls.Config, error) {
	return exttlsutil.ClientTLSConfig(caCert, clientCert, clientKey)
}

func LoadClientTLSConfig(caCert, clientCert, clientKey string) (*tls.Config, error) {
	return exttlsutil.LoadClientTLSConfig(caCert, clientCert, clientKey)
}

func SetupTLSConfig(conf map[string]string, address string) (*tls.Config, error) {
	return exttlsutil.SetupTLSConfig(conf, address)
}
