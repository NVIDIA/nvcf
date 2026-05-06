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

package main

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/version"
	jose "github.com/go-jose/go-jose/v3"
	"github.com/urfave/cli/v2"
	"gopkg.in/yaml.v3"

	restclient "k8s.io/client-go/rest"

	internalutil "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/cmd/internal"
)

type options struct {
	out          io.Writer
	outputFormat string
	egressCIDRs  []string
	jwksURI      string
}

func main() {
	var (
		currContext string
	)
	opts := options{
		out: os.Stdout,
	}
	app := &cli.App{
		Name:    "export-cluster-pubkeys",
		Usage:   "Export cluster information for Vault integration",
		Version: version.ReleaseString(),
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        internalutil.ContextFlagName,
				Value:       "",
				Usage:       internalutil.ContextFlagUsage,
				Destination: &currContext,
			},
			&cli.StringFlag{
				Name:        "format",
				Aliases:     []string{"f"},
				Value:       "json",
				Usage:       `Output format, must be one of: json, yaml`,
				Destination: &opts.outputFormat,
			},
			&cli.MultiStringFlag{
				Target: &cli.StringSliceFlag{
					Name:     "egress-cidrs",
					Usage:    `A list of cluster egress CIDR's`,
					Required: true,
				},
				Value:       []string{},
				Destination: &opts.egressCIDRs,
			},
			&cli.StringFlag{
				Name:        "jwks-uri",
				Value:       "",
				Usage:       `Override the JWKS URI from the OIDC configuration`,
				Destination: &opts.jwksURI,
			},
		},
		Action: func(c *cli.Context) error {
			ctx := c.Context

			k8sClient, restCfg, err := internalutil.NewK8sClient(ctx, currContext)
			if err != nil {
				log.Fatal(err)
			}

			s := &k8sStreamer{
				k8sRESTClient: k8sClient.RESTClient(),
				k8sHTTPClient: k8sClient.RESTClient().(*restclient.RESTClient).Client,
			}

			return run(ctx, s, opts, restCfg.Host)
		},
	}

	ctx := core.NewDefaultContext(context.Background())
	log := core.GetLogger(ctx)
	if err := app.RunContext(ctx, os.Args); err != nil {
		log.Fatal(err)
	}
}

type oidcConfig struct {
	Issuer           string   `json:"issuer"`
	SupportedSigAlgs []string `json:"id_token_signing_alg_values_supported"`
	JWKSURI          string   `json:"jwks_uri"`
}

type vaultJWTMountYAML struct {
	BoundAudiences []string       `yaml:"bound_audiences"`
	BoundCIDRs     []string       `yaml:"bound_cidrs"`
	Config         vaultJWTConfig `yaml:"config"`
}

func newVaultJWTMount(cidrs []string, cfg vaultJWTConfig) vaultJWTMountYAML {
	return vaultJWTMountYAML{
		BoundAudiences: []string{"https://:443"},
		BoundCIDRs:     cidrs,
		Config:         cfg,
	}
}

type vaultJWTConfig struct {
	BoundCIDRs       []string `json:"bound_cidrs" yaml:"-"`
	Issuer           string   `json:"bound_issuer" yaml:"bound_issuer"`
	SupportedSigAlgs []string `json:"jwt_supported_algs" yaml:"jwt_supported_algs"`
	PubkeysPEM       []string `json:"jwt_validation_pubkeys" yaml:"jwt_validation_pubkeys"`
}

type streamer interface {
	streamGetURI(ctx context.Context, u *url.URL) (io.ReadCloser, error)
}

type k8sRESTClient interface {
	Get() *restclient.Request
}

type k8sStreamer struct {
	k8sRESTClient k8sRESTClient
	k8sHTTPClient *http.Client
}

func (s *k8sStreamer) streamGetURI(ctx context.Context, u *url.URL) (io.ReadCloser, error) {
	// This will occur if the JWKS URL is absolute/external but it should be unauthenticated
	if u.IsAbs() {
		// TODO(mcamp): kind of a dangerous cast here but we need the certs for this to work properly
		// so need to grab embedded http client
		httpClient := s.k8sHTTPClient
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, err
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			// The embedded client might fail due to missing CA certs (K8sClient seem to override
			// the CA store). Let's try using the default http.Transport.
			log.Printf("failed to retrieve URI %s: %s", req.URL, err)
			log.Printf("retrying with http.DefaultTransport...")
			oldTransport := httpClient.Transport
			httpClient.Transport = http.DefaultTransport
			defer func() {
				httpClient.Transport = oldTransport
			}()

			resp, err = httpClient.Do(req)
			if err != nil {
				return nil, err
			}
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("failed to retrieve URI %s: %s", req.URL, resp.Status)
		}
		return resp.Body, nil
	}

	return s.k8sRESTClient.Get().RequestURI(u.EscapedPath()).Stream(ctx)
}

const wkOIDCURI = "/.well-known/openid-configuration"

func run(ctx context.Context, s streamer, opts options, k8sServerHost string) (err error) {
	u, err := url.Parse(wkOIDCURI)
	if err != nil {
		return fmt.Errorf("parse OIDC URL %s: %w", wkOIDCURI, err)
	}

	oidcConfigRC, err := s.streamGetURI(ctx, u)
	if err != nil {
		return fmt.Errorf("get OIDC config from cluster: %w", err)
	}
	defer oidcConfigRC.Close()

	var oidcCfg oidcConfig
	if err := decodeJSON(oidcConfigRC, &oidcCfg); err != nil {
		return fmt.Errorf("decode OIDC config: %w", err)
	}

	jwksURIStr := oidcCfg.JWKSURI
	if opts.jwksURI != "" {
		jwksURIStr = opts.jwksURI
	}

	jwksURL, err := url.Parse(jwksURIStr)
	if err != nil {
		return fmt.Errorf("parse JWKS URI: %w", err)
	}

	jwksRC, err := s.streamGetURI(ctx, jwksURL)
	if err != nil {
		if os.IsTimeout(err) || errors.Is(err, syscall.ECONNREFUSED) {
			// If the JWKS URL times out, try the /openid/v1/jwks endpoint from the KUBECONFIG
			retryJWKSURLStr := fmt.Sprintf("%s/openid/v1/jwks", k8sServerHost)
			log.Printf("get JWKS from cluster failed due to timeout: %v, "+
				"retrying with the cluster's server endpoint (from KUBECONFIG) --jwks-uri=\"%s\" flag\n",
				err,
				retryJWKSURLStr)
			jwksURL, err = url.Parse(retryJWKSURLStr)
			if err != nil {
				return fmt.Errorf("parse JWKS URI: %w", err)
			}
			jwksRC, err = s.streamGetURI(ctx, jwksURL)
			if err != nil {
				return fmt.Errorf("get JWKS from cluster: %w", err)
			}
		} else {
			return fmt.Errorf("get JWKS from cluster: %w", err)
		}
	}
	defer jwksRC.Close()

	pubkeyPEMStrs, err := jwksToPEMStrs(jwksRC)
	if err != nil {
		return fmt.Errorf("convert JWKS to PEM: %w", err)
	}

	switch opts.outputFormat {
	case "yaml":
		pubkeyPEMStrsBase64 := make([]string, len(pubkeyPEMStrs))
		for i, pkpem := range pubkeyPEMStrs {
			pubkeyPEMStrsBase64[i] = base64.StdEncoding.EncodeToString([]byte(pkpem))
		}

		vaultJWTCfg := vaultJWTConfig{
			Issuer:           oidcCfg.Issuer,
			SupportedSigAlgs: oidcCfg.SupportedSigAlgs,
			PubkeysPEM:       pubkeyPEMStrsBase64,
		}

		vaultJWTMount := newVaultJWTMount(
			opts.egressCIDRs,
			vaultJWTCfg,
		)
		if err := encodeYAML(opts.out, vaultJWTMount); err != nil {
			return fmt.Errorf("write Vault JWT mount: %w", err)
		}
	case "json":
		vaultJWTCfg := vaultJWTConfig{
			BoundCIDRs:       opts.egressCIDRs,
			Issuer:           oidcCfg.Issuer,
			SupportedSigAlgs: oidcCfg.SupportedSigAlgs,
			PubkeysPEM:       pubkeyPEMStrs,
		}
		if err := encodeJSON(opts.out, vaultJWTCfg); err != nil {
			return fmt.Errorf("write Vault JWT config: %w", err)
		}
	default:
		return fmt.Errorf("unknown output format: %s", opts.outputFormat)
	}

	return nil
}

func jwksToPEMStrs(in io.Reader) ([]string, error) {
	var jwks jose.JSONWebKeySet
	if err := decodeJSON(in, &jwks); err != nil {
		return nil, fmt.Errorf("decode JWKS: %v", err)
	}

	var pemStrs []string
	for _, jwk := range jwks.Keys {
		if !jwk.IsPublic() {
			log.Println("JWK is not public:", jwk.KeyID)
			continue
		}

		pubKeyBytes, err := x509.MarshalPKIXPublicKey(jwk.Key)
		if err != nil {
			return nil, fmt.Errorf("decode PKIX public key %q: %v", jwk.KeyID, err)
		}

		block := pem.Block{
			Type:  "PUBLIC KEY",
			Bytes: pubKeyBytes,
		}

		buf := bytes.Buffer{}
		if err := pem.Encode(&buf, &block); err != nil {
			return nil, fmt.Errorf("encode PEM block for key %q: %v", jwk.KeyID, err)
		}
		pemStrs = append(pemStrs, strings.TrimSpace(buf.String()))
	}

	return pemStrs, nil
}

func decodeJSON(r io.Reader, v any) error {
	dec := json.NewDecoder(r)
	return dec.Decode(v)
}

func encodeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func encodeYAML(w io.Writer, v any) error {
	enc := yaml.NewEncoder(w)
	enc.SetIndent(2)
	return enc.Encode(v)
}
