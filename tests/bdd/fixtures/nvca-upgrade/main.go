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

// This lab fixture supplies an ephemeral identity provider, an NGC API route
// adapter, and a CPU-only agent probe. All cluster state belongs to real ICMS.
package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var agentVersion = "development"

const account = "bdd-account"

type gateway struct {
	key       *ecdsa.PrivateKey
	issuer    string
	icms      string
	probeRepo string
	probeTag  string
	client    *http.Client
}

func main() {
	if strings.Contains(filepath.Base(os.Args[0]), "credential") {
		select {}
	}
	if len(os.Args) > 1 && os.Args[1] == "gateway" {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			log.Fatal(err)
		}
		g := &gateway{
			key: key, issuer: os.Getenv("BDD_ISSUER"), icms: os.Getenv("BDD_ICMS_URL"),
			probeRepo: os.Getenv("BDD_PROBE_REPOSITORY"), probeTag: os.Getenv("BDD_PROBE_TAG"),
			client: &http.Client{Timeout: 30 * time.Second},
		}
		log.Fatal((&http.Server{Addr: ":8080", Handler: g, ReadHeaderTimeout: 5 * time.Second}).ListenAndServe())
	}
	if err := runProbe(); err != nil {
		log.Fatal(err)
	}
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("write JSON response: %v", err)
	}
}

func encode(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(data)
}

func (g *gateway) token(subject, scope string) (string, error) {
	now := time.Now()
	unsigned := encode(map[string]string{"alg": "ES256", "kid": "lab-key", "typ": "JWT"}) + "." +
		encode(map[string]any{
			"sub": subject, "iss": g.issuer, "iat": now.Unix(),
			"exp": now.Add(time.Hour).Unix(), "scopes": []string{scope},
		})
	digest := sha256.Sum256([]byte(unsigned))
	r, s, err := ecdsa.Sign(rand.Reader, g.key, digest[:])
	if err != nil {
		return "", err
	}
	signature := append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func (g *gateway) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	switch request.URL.Path {
	case "/health":
		writeJSON(w, map[string]string{"status": "ok"})
		return
	case "/.well-known/jwks.json":
		writeJSON(w, map[string]any{"keys": []any{map[string]string{
			"kty": "EC", "crv": "P-256", "alg": "ES256", "use": "sig", "kid": "lab-key",
			"x": base64.RawURLEncoding.EncodeToString(g.key.X.FillBytes(make([]byte, 32))),
			"y": base64.RawURLEncoding.EncodeToString(g.key.Y.FillBytes(make([]byte, 32))),
		}}})
		return
	case "/token":
		token, err := g.token("lab-service", "cluster-management")
		if err != nil {
			http.Error(w, "sign token", http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"access_token": token, "token_type": "Bearer", "expires_in": 3600})
		return
	}
	path := request.URL.Path
	ngc := strings.HasPrefix(path, "/v2/sis/clusters/")
	if ngc {
		path = "/v1/accounts/" + account + "/clusters/" + strings.TrimPrefix(path, "/v2/sis/clusters/")
	}
	if !strings.HasPrefix(path, "/v1/accounts/") && !strings.HasPrefix(path, "/v1/nvca/clusters/") && path != "/v1/health" {
		http.NotFound(w, request)
		return
	}
	subject, scope := "lab-admin", "cluster-management"
	if strings.HasPrefix(path, "/v1/nvca/clusters/") {
		subject = strings.Split(strings.TrimPrefix(path, "/v1/nvca/clusters/"), "/")[0]
		scope = "nvca-cluster"
	}
	token, err := g.token(subject, scope)
	if err != nil {
		http.Error(w, "sign token", http.StatusInternalServerError)
		return
	}
	upstream, err := http.NewRequestWithContext(request.Context(), request.Method, g.icms+path, request.Body)
	if err != nil {
		http.Error(w, "construct upstream request", http.StatusBadGateway)
		return
	}
	upstream.Header.Set("Authorization", "Bearer "+token)
	upstream.Header.Set("Content-Type", "application/json")
	response, err := g.client.Do(upstream)
	if err != nil {
		http.Error(w, "ICMS unavailable", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	if ngc && response.StatusCode == http.StatusOK {
		var cluster map[string]any
		if err := json.NewDecoder(response.Body).Decode(&cluster); err != nil {
			http.Error(w, "decode ICMS response", http.StatusBadGateway)
			return
		}
		// These are lab routing/image settings supplied by the NGC API frontend.
		// In particular, nvcaVersion is always the unmodified ICMS response value.
		cluster["icmsConfig"] = map[string]string{"icmsServiceURL": g.issuer, "tokenURL": g.issuer + "/token"}
		cluster["webhookConfig"] = map[string]any{"imageConfig": map[string]string{
			"repository": g.probeRepo, "tag": g.probeTag,
		}}
		writeJSON(w, cluster)
		return
	}
	w.Header().Set("Content-Type", response.Header.Get("Content-Type"))
	w.WriteHeader(response.StatusCode)
	if _, err := io.Copy(w, response.Body); err != nil {
		log.Printf("copy ICMS response: %v", err)
	}
}

func runProbe() error {
	var configPath string
	for i, value := range os.Args[1:] {
		if value == "--config" && i+2 < len(os.Args) {
			configPath = os.Args[i+2]
		}
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("read agent configuration: %w", err)
	}
	var config struct {
		Cluster struct {
			ID string `yaml:"id"`
		} `yaml:"cluster"`
		Agent struct {
			ICMSURL string `yaml:"icmsurl"`
		} `yaml:"agent"`
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("decode agent configuration: %w", err)
	}
	if config.Cluster.ID == "" || config.Agent.ICMSURL == "" {
		return fmt.Errorf("agent configuration requires cluster.id and agent.icmsurl")
	}
	payload, err := json.Marshal(map[string]any{
		"status": "READY", "k8sVersion": "v1.30.0", "nvcaVersion": agentVersion,
		"gpus": []any{map[string]any{
			"name": "bdd-probe", "capacity": 0,
			"instanceTypes": []any{map[string]any{
				"name": "bdd-probe", "value": "bdd-probe", "default": true,
				"cpuCores": 1, "systemMemory": "1Gi", "gpuMemory": "1Gi", "gpuCount": 1,
			}},
		}},
	})
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 10 * time.Second}
	deadline := time.Now().Add(2 * time.Minute)
	for {
		request, err := http.NewRequest(http.MethodPut,
			config.Agent.ICMSURL+"/v1/nvca/clusters/"+config.Cluster.ID+"/register", bytes.NewReader(payload))
		if err != nil {
			return err
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				break
			}
			log.Printf("startup registration version=%s HTTP=%d", agentVersion, response.StatusCode)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("startup registration did not succeed within two minutes")
		}
		time.Sleep(time.Second)
	}
	log.Printf("startup registration accepted version=%s", agentVersion)
	health := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"status": "Healthy", "k8sVersion": "v1.30.0", "gpuUsage": map[string]any{},
			"agentVersion": agentVersion,
		})
	})
	go func() {
		if err := (&http.Server{Addr: ":8002", Handler: health, ReadHeaderTimeout: 5 * time.Second}).ListenAndServe(); err != nil {
			log.Print(err)
		}
	}()
	return (&http.Server{Addr: ":8000", Handler: health, ReadHeaderTimeout: 5 * time.Second}).ListenAndServe()
}
