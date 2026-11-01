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

package clients

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/auth"
)

type ClientType string

const (
	ClientTypeGRPC ClientType = "grpc"
	ClientTypeHTTP ClientType = "http"
)

func clientTypes() string {
	return strings.Join([]string{string(ClientTypeGRPC), string(ClientTypeHTTP)}, ",")
}

type BaseClientConfig struct {
	// Type of client - grpc/http
	Type string `mapstructure:"type"`
	// Addr is the address of the server to send requests to.
	Addr string `mapstructure:"addr"`
	// TLS holds all TLS configuration options for this client.
	TLS auth.TLSConfigOptions `mapstructure:"tls,omitempty"`
	// AuthnCfg holds any authentication config to use for requests
	AuthnCfg *auth.AuthnConfig `mapstructure:"authn,omitempty"`
}

// AddClientFlags add the http client flags with the client prefix
func (cfg *BaseClientConfig) AddClientFlags(cmd *cobra.Command, clientName string) bool {
	if cmd == nil || cfg == nil || clientName == "" {
		return false
	}
	clientFlag := func(flag string) string {
		return fmt.Sprintf("%s.%s", clientName, flag)
	}

	cmd.Flags().StringVarP(&cfg.Type, clientFlag("type"), "", "grpc", fmt.Sprintf("client type (options - %s)", clientTypes()))
	cmd.Flags().StringVarP(&cfg.Addr, clientFlag("addr"), "", "", "address of the server to dial for this client")
	cmd.Flags().BoolVarP(&cfg.TLS.Enabled, clientFlag("tls.enabled"), "", false, "Enable TLS")
	cmd.Flags().StringVarP(&cfg.TLS.CertFile, clientFlag("tls.cert-file"), "", "", "path to the client TLS Cert file")
	cmd.Flags().StringVarP(&cfg.TLS.KeyFile, clientFlag("tls.key-file"), "", "", "path to the client TLS Key file")
	cmd.Flags().StringVarP(&cfg.TLS.RootCAFile, clientFlag("tls.root-ca-file"), "", "", "path to the root CA file, used to verify server certificate")
	cmd.Flags().BoolVarP(&cfg.TLS.InsecureSkipVerify, clientFlag("tls.insecure-skip-verify"), "", false, "if false, clients verify the server's certificate chain and host name")

	cfg.AuthnCfg = &auth.AuthnConfig{OIDCConfig: &auth.ProviderConfig{}}
	cfg.AuthnCfg.AddClientFlags(cmd, clientFlag("authn"))

	return true
}

type ClientConfigProvider interface {
	AddClientFlags(cmd *cobra.Command, clientName string) bool
	DialOptions() ([]grpc.DialOption, error)
	Dial() (*grpc.ClientConn, error)
}
