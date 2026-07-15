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

package mockicmsservice

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/google/uuid"
	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"

	mocktokencache "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/mock/tokencache"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/mock/utils"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/queue"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

const (
	clusterIsNotRegisteredMsg = "cluster is not registered: "
	verPrefixV1Bart           = "/v1/bart"
	verPrefixV1NVCA           = "/v1/nvca"
)

func Run(ctx context.Context, addr, keyFile, queueEndpoint string) error {
	priv, err := utils.DecodePrivateKeyRSA(keyFile)
	if err != nil {
		return err
	}
	return RunWithKey(ctx, addr, queueEndpoint, priv)
}

func RunWithKey(ctx context.Context, addr, queueEndpoint string, priv *rsa.PrivateKey) error {
	log := core.GetLogger(ctx)

	s, err := newService(ctx, addr, queueEndpoint, priv)
	if err != nil {
		return err
	}

	log.WithFields(logrus.Fields{
		"queueEndpoint": s.queueEndpoint,
	}).Info("Starting mock ICMS service")

	_, err = s.srv.Start(ctx)
	return err
}

func newService(ctx context.Context, addr, queueEndpoint string, priv *rsa.PrivateKey) (*icmsService, error) {
	s := &icmsService{
		queueEndpoint: strings.TrimSuffix(queueEndpoint, "/"),
		key:           priv,
		ServiceState: ServiceState{
			BartClusters:        map[string]ClusterInfo{},
			QueueMetadata:       map[string]ICMSCredentialResponse{},
			QueueMetadataV1Bart: map[string]ICMSCredentialResponseV1Bart{},
			ICMSRequestStatuses: map[string]types.ICMSAcknowledgeRequest{},
			InstanceStatuses:    map[string]map[string]types.ICMSInstanceStatusUpdateRequest{},
		},
	}

	srv := core.NewHTTPService(addr)

	log := core.GetLogger(ctx)
	srv.Use(func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r = r.WithContext(core.WithLogger(r.Context(), log))
			h.ServeHTTP(w, r)
		})
	})
	lf := utils.NewCustomLogFormatter(log)
	srv.Use(func(h http.Handler) http.Handler {
		return handlers.CustomLoggingHandler(log.Logger.Out, h, lf)
	})

	srv.Path(verPrefixV1NVCA + "/clusters/{cluster_id}/register").HandlerFunc(s.handleV1NVCARegister).Methods(http.MethodPut)
	srv.Path(verPrefixV1NVCA + "/clusters/{cluster_id}/credentials").HandlerFunc(s.handleV1NVCAGetCreds).Methods(http.MethodGet)
	srv.Path(verPrefixV1NVCA + "/clusters/{cluster_id}/heartbeat").HandlerFunc(s.handleV1NVCAHeartbeat).Methods(http.MethodPost)

	// TODO: cluster management APIs

	srv.Path("/v1/sirs/{sr_id}").HandlerFunc(s.handleV1SirsAckRequest).Methods(http.MethodPut)
	srv.Path("/v1/sirs/{sr_id}/{inst_id}").HandlerFunc(s.handleV1SirsUpdateInstanceStatus).Methods(http.MethodPost)

	srv.Path("/dump").HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s.mu.RLock()
		b, _ := json.MarshalIndent(s.ServiceState, "", "  ")
		s.mu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(append(b, '\n'))
	}).Methods(http.MethodGet)

	srv.Path("/jwt").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log := core.GetLogger(r.Context())

		vars := mux.Vars(r)
		iss := vars["iss"]
		if iss == "" {
			iss = "icmsservice"
		}
		oauthClientID := vars["oauth_client_id"]

		jwtCache, _, err := mocktokencache.NewForKey(iss, oauthClientID, s.key)
		if err != nil {
			log.WithError(err).Error("New mock token cache")
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		tok, err := jwtCache.FetchToken(r.Context())
		if err != nil {
			log.WithError(err).Error("Fetch mock token from cache")
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(append([]byte(tok), '\n'))
	}).Methods(http.MethodGet)

	s.srv = srv

	return s, nil
}

type icmsService struct {
	srv *core.HTTPService
	key *rsa.PrivateKey

	queueEndpoint string

	mu sync.RWMutex
	ServiceState
}

type ClusterInfo struct {
	types.ICMSRegistrationRequest
	ClusterID string    `json:"clusterId"`
	Updated   time.Time `json:"updated"`
}

type ServiceState struct {
	// Map of OAuth client ID to a registered NVCA cluster.
	BartClusters map[string]ClusterInfo `json:"bart_clusters"`
	// Map of OAuth client ID to a registered cluster's queues.
	QueueMetadata       map[string]ICMSCredentialResponse                           `json:"queue_metadata"`
	QueueMetadataV1Bart map[string]ICMSCredentialResponseV1Bart                     `json:"queue_metadata_v1_bart"`
	ICMSRequestStatuses map[string]types.ICMSAcknowledgeRequest                     `json:"icms_request_status"`
	InstanceStatuses    map[string]map[string]types.ICMSInstanceStatusUpdateRequest `json:"instance_status"`
}

type ICMSRegistrationResponseV1 struct {
	ClusterID      string                 `json:"clusterId,omitempty"`
	ClusterGroupID string                 `json:"clusterGroupId,omitempty"`
	Credentials    QueueCredentialsV1Bart `json:"credentials,omitempty"`
}

type ICMSCredentialResponseV1Bart struct {
	Credentials QueueCredentialsV1Bart `json:"credentials,omitempty"`
}

type QueueCredentialsV1Bart struct {
	CreationQueue    queue.MessageQueueInfo `json:"creationQueue,omitempty"`
	TerminationQueue queue.MessageQueueInfo `json:"terminationQueue,omitempty"`
}

type ICMSRegistrationResponse struct {
	ClusterID      string           `json:"clusterId,omitempty"`
	ClusterGroupID string           `json:"clusterGroupId,omitempty"`
	Credentials    QueueCredentials `json:"credentials,omitempty"`
}

type ICMSCredentialResponse struct {
	QueueCredentials
}

type QueueCredentials struct {
	CreationQueues   map[string]queue.MessageQueueInfo `json:"creationQueue,omitempty"`
	TerminationQueue queue.MessageQueueInfo            `json:"terminationQueue,omitempty"`
}

func (s *icmsService) getBARTCluster(oauthClientID string) (ClusterInfo, bool) {
	s.mu.RLock()
	c, ok := s.BartClusters[oauthClientID]
	s.mu.RUnlock()
	return c, ok
}

func (s *icmsService) getQueueMetadata(oauthClientID string) (ICMSCredentialResponse, bool) {
	s.mu.RLock()
	c, ok := s.QueueMetadata[oauthClientID]
	s.mu.RUnlock()
	return c, ok
}

func (s *icmsService) setBARTCluster(oauthClientID string, in ClusterInfo) {
	s.mu.Lock()
	s.BartClusters[oauthClientID] = in
	s.mu.Unlock()
}

func (s *icmsService) setQueueMetadata(oauthClientID string, in ICMSCredentialResponse) {
	s.mu.Lock()
	s.QueueMetadata[oauthClientID] = in
	s.mu.Unlock()
}

func (s *icmsService) handleV1NVCARegister(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	vars := mux.Vars(r)
	clusterID := vars["cluster_id"]

	log := core.GetLogger(r.Context()).WithField("cluster_id", clusterID)

	log.Info("Registering NVCA cluster")

	oauthClientID, authValid, err := s.parseAuth(r)
	if err != nil {
		log.WithError(err).Error("Parse auth")
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	} else if !authValid {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	log = log.WithField("oauth_client_id", oauthClientID)

	var regBody types.ICMSRegistrationRequest
	reqDec := json.NewDecoder(r.Body)
	reqDec.DisallowUnknownFields()
	if err := reqDec.Decode(&regBody); err != nil {
		log.WithError(err).Error("Decode body")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var gpuNames []string
	for _, gpu := range regBody.BackendGPUs {
		gpuNames = append(gpuNames, gpu.Name)
	}

	var queueMetadata ICMSCredentialResponse
	// Create the queue in the mock queue service.
	log.WithField("cluster_id", clusterID).Info("Creating new queues")

	createQueueReqBodyBytes, err := json.Marshal(struct {
		OAuthClientID string   `json:"oauth_client_id"`
		GPUNames      []string `json:"gpu_names"`
	}{
		OAuthClientID: oauthClientID,
		GPUNames:      gpuNames,
	})
	if err != nil {
		log.WithError(err).Error("Extract OAuth client ID")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	createQueueReqBody := bytes.NewReader(createQueueReqBodyBytes)

	createQueueReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
		s.queueEndpoint+"/queue", createQueueReqBody)
	if err != nil {
		log.WithError(err).Error("New queue creation request")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	createQueueRes, err := http.DefaultClient.Do(createQueueReq)
	if err != nil {
		log.WithError(err).Error("Do queue creation request")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer createQueueRes.Body.Close()

	if createQueueRes.StatusCode != http.StatusOK {
		log.WithField("status", createQueueRes.Status).Error("Unable to create queue")
		http.Error(w, "unable to create queue", http.StatusServiceUnavailable)
		return
	}

	queueResDec := json.NewDecoder(createQueueRes.Body)
	queueResDec.DisallowUnknownFields()
	if err := queueResDec.Decode(&queueMetadata); err != nil {
		createQueueResBodyBytes, rerr := io.ReadAll(createQueueRes.Body)
		if rerr != nil {
			log.WithError(rerr).Error("Read queue creation response")
			http.Error(w, rerr.Error(), http.StatusInternalServerError)
			return
		}
		log.WithError(err).WithField("body", string(createQueueResBodyBytes)).
			Error("Decode queue creation response")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.setBARTCluster(oauthClientID, ClusterInfo{
		ICMSRegistrationRequest: regBody,
		ClusterID:               clusterID,
		Updated:                 time.Now(),
	})
	s.setQueueMetadata(oauthClientID, queueMetadata)

	resBody := ICMSRegistrationResponse{
		ClusterID:      clusterID,
		ClusterGroupID: uuid.NewString(),
		Credentials:    queueMetadata.QueueCredentials,
	}
	rb, err := json.Marshal(resBody)
	if err != nil {
		log.WithError(err).Error("Encode response")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(rb)
}

func (s *icmsService) handleV1NVCAGetCreds(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	vars := mux.Vars(r)
	clusterID := vars["cluster_id"]

	log := core.GetLogger(r.Context()).WithField("cluster_id", clusterID)

	log.Info("Getting NVCA cluster credentials")

	oauthClientID, authValid, err := s.parseAuth(r)
	if err != nil {
		log.WithError(err).Error("Parse auth")
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	} else if !authValid {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	log = log.WithField("oauth_client_id", oauthClientID)

	cq, ok := s.getQueueMetadata(oauthClientID)
	if !ok {
		http.Error(w, clusterIsNotRegisteredMsg+oauthClientID, http.StatusBadRequest)
		return
	}

	b, err := json.Marshal(cq)
	if err != nil {
		log.WithError(err).Error("Encode creds response")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b)
}

func (s *icmsService) handleV1NVCAHeartbeat(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	vars := mux.Vars(r)
	clusterID := vars["cluster_id"]

	log := core.GetLogger(r.Context()).WithField("cluster_id", clusterID)

	log.Info("NVCA cluster heartbeat")

	oauthClientID, authValid, err := s.parseAuth(r)
	if err != nil {
		log.WithError(err).Error("Parse auth")
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	} else if !authValid {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	log = log.WithField("oauth_client_id", oauthClientID)

	if _, ok := s.getBARTCluster(oauthClientID); !ok {
		http.Error(w, clusterIsNotRegisteredMsg+oauthClientID, http.StatusBadRequest)
		return
	}

	var body types.HealthStatusRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		log.WithError(err).Error("Decode heartbeat request body")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	log.Debugf("Got OAuth client ID %s heartbeat: %s", oauthClientID, body.Status)

	w.WriteHeader(http.StatusOK)
}

func (s *icmsService) handleV1SirsAckRequest(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	log := core.GetLogger(r.Context())

	log.Info("NVCA cluster SIRS ack")

	oauthClientID, authValid, err := s.parseAuth(r)
	if err != nil {
		log.WithError(err).Error("Parse auth")
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	} else if !authValid {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	log = log.WithField("oauth_client_id", oauthClientID)

	if _, ok := s.getBARTCluster(oauthClientID); !ok {
		http.Error(w, clusterIsNotRegisteredMsg+oauthClientID, http.StatusBadRequest)
		return
	}

	var body types.ICMSAcknowledgeRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		log.WithError(err).Error("Decode sirs ack request body")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	vars := mux.Vars(r)
	srID := vars["sr_id"]

	s.mu.Lock()
	s.ICMSRequestStatuses[srID] = body
	s.mu.Unlock()

	log.Debugf("Got ICMS request %s status: %s", srID, body.Status)

	w.WriteHeader(http.StatusOK)
}

func (s *icmsService) handleV1SirsUpdateInstanceStatus(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	log := core.GetLogger(r.Context())

	log.Info("NVCA cluster SIRS message update")

	oauthClientID, authValid, err := s.parseAuth(r)
	if err != nil {
		log.WithError(err).Error("Parse auth")
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	} else if !authValid {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	log = log.WithField("oauth_client_id", oauthClientID)

	if _, ok := s.getBARTCluster(oauthClientID); !ok {
		http.Error(w, clusterIsNotRegisteredMsg+oauthClientID, http.StatusBadRequest)
		return
	}

	var body types.ICMSInstanceStatusUpdateRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		log.WithError(err).Error("Decode sirs update instance status request body")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	vars := mux.Vars(r)
	srID := vars["sr_id"]
	instID := vars["inst_id"]

	s.mu.Lock()
	instStatuses, ok := s.InstanceStatuses[srID]
	if !ok {
		instStatuses = map[string]types.ICMSInstanceStatusUpdateRequest{}
		s.InstanceStatuses[srID] = instStatuses
	}
	instStatuses[instID] = body
	s.mu.Unlock()

	log.Debugf("Got ICMS request %s instance %s status: %s", srID, instID, body.Status)

	w.WriteHeader(http.StatusOK)
}

func (s *icmsService) parseAuth(r *http.Request) (oauthClientID string, valid bool, err error) {
	log := core.GetLogger(r.Context())

	authHdrVal := r.Header.Get("Authorization")
	if authHdrVal == "" || !strings.HasPrefix(authHdrVal, "Bearer ") {
		return "", false, fmt.Errorf("bad auth header: %q", authHdrVal)
	}

	tokStr := strings.TrimPrefix(authHdrVal, "Bearer ")

	tok, err := jwt.ParseSigned(tokStr, []jose.SignatureAlgorithm{jose.RS256})
	if err != nil {
		return "", false, err
	}

	var cl jwt.Claims
	if err := tok.Claims(&s.key.PublicKey, &cl); err != nil {
		log.WithError(err).Error("Verify claims")
		return "", false, nil
	}

	return cl.Subject, true, nil
}
