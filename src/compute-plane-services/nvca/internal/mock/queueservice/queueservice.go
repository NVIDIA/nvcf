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

package mockqueueservice

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
	"github.com/google/uuid"
	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/mock/utils"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/queue"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

const (
	queueDoesNotExistMsg = "queue does not exist: "
)

func RunInCluster(ctx context.Context, addr string) error {
	log := core.GetLogger(ctx)

	hostname, err := os.Hostname()
	if err != nil {
		return err
	}
	namespace := os.Getenv("POD_NAMESPACE")
	if namespace == "" {
		return fmt.Errorf("env POD_NAMESPACE is empty")
	}
	queueURLBase := fmt.Sprintf("http://%s.%s.svc.cluster.local", hostname, namespace)

	s, err := newService(ctx, addr, queueURLBase)
	if err != nil {
		return err
	}

	log.WithFields(logrus.Fields{
		"queueURLBase": s.queueURLBase,
	}).Info("Starting mock queue service")

	_, err = s.srv.Start(ctx)
	return err
}

func Run(ctx context.Context, addr string) error {
	log := core.GetLogger(ctx)

	s, err := newService(ctx, addr, "http://"+addr)
	if err != nil {
		return err
	}

	log.WithFields(logrus.Fields{
		"queueURLBase": s.queueURLBase,
	}).Info("Starting mock queue service")

	_, err = s.srv.Start(ctx)
	return err
}

func newService(ctx context.Context, addr, queueURLBase string) (*queueService, error) {
	s := &queueService{
		queueURLBase: queueURLBase,
		ServiceState: ServiceState{
			QueueMetadata:     map[string]ICMSCredentialResponse{},
			CreationQueues:    map[string]map[string][]QueueMessage{},
			TerminationQueues: map[string][]QueueMessage{},
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

	srv.Path("/queue").HandlerFunc(s.handleCreateQueue).Methods(http.MethodPost)
	srv.Path("/queue/{oauth_client_id}").HandlerFunc(s.handleDeleteQueues).Methods(http.MethodDelete)
	srv.Path("/create/{oauth_client_id}/{gpu_name}").HandlerFunc(s.handleCreateMessage).
		Methods(http.MethodPost, http.MethodGet)
	srv.Path("/create/{oauth_client_id}/{gpu_name}/{recp_hdl}").HandlerFunc(s.handleUpdateOrDeleteCreateMessage).
		Methods(http.MethodPut, http.MethodDelete)
	srv.Path("/terminate/{oauth_client_id}").HandlerFunc(s.handleTerminateMessage).
		Methods(http.MethodPost, http.MethodGet)
	srv.Path("/terminate/{oauth_client_id}/{recp_hdl}").HandlerFunc(s.handleUpdateOrDeleteTerminateMessage).
		Methods(http.MethodPut, http.MethodDelete)

	srv.Path("/dump").HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s.mu.RLock()
		b, _ := json.MarshalIndent(s.ServiceState, "", "  ")
		s.mu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(append(b, '\n'))
	}).Methods(http.MethodGet)

	srv.Path("/clear").HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		for id := range s.ServiceState.CreationQueues {
			s.ServiceState.CreationQueues[id] = map[string][]QueueMessage{}
		}
		for id := range s.ServiceState.TerminationQueues {
			s.ServiceState.TerminationQueues[id] = []QueueMessage{}
		}
		s.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}).Methods(http.MethodGet)

	s.srv = srv

	return s, nil
}

type queueService struct {
	srv *core.HTTPService

	queueURLBase string

	mu sync.RWMutex
	ServiceState
}

type ServiceState struct {
	// Map of OAuth client ID to a registered cluster's queue credentials.
	QueueMetadata map[string]ICMSCredentialResponse `json:"queue_metadata"`
	// Map of OAuth client ID to cluster's queues.
	CreationQueues    map[string]map[string][]QueueMessage `json:"creation_queues"`
	TerminationQueues map[string][]QueueMessage            `json:"termination_queues"`
}

const AllGPUsName = "__all_gpus"

type QueueMessage struct {
	Body          any       `json:"body"`
	RequestID     string    `json:"request_id"`
	MessageID     string    `json:"message_id"`
	ReceiptHandle string    `json:"receipt_handle"`
	VisTimeout    time.Time `json:"vis_timeout"`
}

func (m QueueMessage) isVisible() bool { return !time.Now().Before(m.VisTimeout) }

func (s *queueService) getQueueMetadata(oauthClientID string) (ICMSCredentialResponse, bool) {
	s.mu.RLock()
	c, ok := s.QueueMetadata[oauthClientID]
	s.mu.RUnlock()
	return c, ok
}

func (s *queueService) getCreationQueueMetadataForGPU(oauthClientID, gpuName string) (queue.MessageQueueInfo, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.QueueMetadata[oauthClientID]
	if ok {
		for _, qi := range c.CreationQueues {
			if qi.GPU == gpuName {
				return qi, true
			}
		}
	}
	return queue.MessageQueueInfo{}, false
}

func (s *queueService) setQueueMetadata(oauthClientID string, in ICMSCredentialResponse) {
	s.mu.Lock()
	s.QueueMetadata[oauthClientID] = in
	s.mu.Unlock()
}

type createQueueRequest struct {
	OAuthClientID string   `json:"oauth_client_id"`
	GPUNames      []string `json:"gpu_names,omitempty"`
}

type ICMSCredentialResponseV1 struct {
	Credentials struct {
		CreationQueue    queue.MessageQueueInfo `json:"creationQueue"`
		TerminationQueue queue.MessageQueueInfo `json:"terminationQueue"`
	} `json:"credentials"`
}

type ICMSCredentialResponse struct {
	CreationQueues   map[string]queue.MessageQueueInfo `json:"creationQueue"`
	TerminationQueue queue.MessageQueueInfo            `json:"terminationQueue"`
}

func (s *queueService) handleCreateQueue(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	log := core.GetLogger(r.Context())

	log.Info("Create queue request")

	var body createQueueRequest
	if err := decode(r.Body, &body); err != nil {
		log.WithError(err).Error("Decode create queue request body")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	oauthClientID := body.OAuthClientID
	if oauthClientID == "" {
		http.Error(w, "OAuth client ID is empty", http.StatusBadRequest)
		return
	}

	if len(body.GPUNames) == 0 {
		if _, ok := s.getQueueMetadata(oauthClientID); ok {
			http.Error(w, "queue already exists: "+oauthClientID, http.StatusBadRequest)
			return
		}
		body.GPUNames = []string{AllGPUsName}
	}

	queuesRes := ICMSCredentialResponse{}
	queuesRes.CreationQueues = map[string]queue.MessageQueueInfo{}
	creationQueues := map[string][]QueueMessage{}
	for _, gpu := range body.GPUNames {
		var existing bool
		if gpu != AllGPUsName {
			if existingQI, ok := s.getCreationQueueMetadataForGPU(oauthClientID, gpu); ok {
				existing = true
				queuesRes.CreationQueues[gpu] = existingQI
				s.mu.RLock()
				creationQueues[gpu] = s.CreationQueues[oauthClientID][gpu]
				s.mu.RUnlock()
			}
		}
		if !existing {
			queuesRes.CreationQueues[gpu] = queue.MessageQueueInfo{
				GPU:       gpu,
				QueueURL:  s.queueURLBase + "/" + qtCreate + "/" + oauthClientID + "/" + gpu,
				QueueType: queue.CreationQueue,
				SecretKey: oauthClientID + "_create_secret_key",
			}
			creationQueues[gpu] = []QueueMessage{}
		}
	}
	queuesRes.TerminationQueue = queue.MessageQueueInfo{
		QueueURL:  s.queueURLBase + "/" + qtTerminate + "/" + oauthClientID,
		QueueType: queue.TerminationQueue,
		SecretKey: oauthClientID + "_terminate_secret_key",
	}

	s.setQueueMetadata(oauthClientID, queuesRes)

	s.mu.Lock()
	s.CreationQueues[oauthClientID] = creationQueues
	s.TerminationQueues[oauthClientID] = []QueueMessage{}
	s.mu.Unlock()

	b, err := json.Marshal(queuesRes)
	if err != nil {
		log.WithError(err).Error("Encode create queue response")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b)
}

func (s *queueService) handleDeleteQueues(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	log := core.GetLogger(r.Context())

	log.Info("Delete queue request")

	oauthClientID := mux.Vars(r)["oauth_client_id"]

	if _, ok := s.getQueueMetadata(oauthClientID); !ok {
		http.Error(w, queueDoesNotExistMsg+oauthClientID, http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	delete(s.QueueMetadata, oauthClientID)
	delete(s.CreationQueues, oauthClientID)
	delete(s.TerminationQueues, oauthClientID)
	s.mu.Unlock()

	w.WriteHeader(http.StatusOK)
}

const (
	qtCreate    = "create"
	qtTerminate = "terminate"
)

func (s *queueService) handleCreateMessage(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	log := core.GetLogger(r.Context())

	vars := mux.Vars(r)
	oauthClientID := vars["oauth_client_id"]
	gpuName := vars["gpu_name"]

	log = log.WithFields(logrus.Fields{
		"path":            r.URL.Path,
		"method":          r.Method,
		"gpu_name":        gpuName,
		"oauth_client_id": oauthClientID,
	})

	log.Infof("%s creation message request", r.Method)

	if oauthClientID == "" {
		http.Error(w, "OAuth client ID is empty", http.StatusBadRequest)
		return
	}

	queuesRes, ok := s.getQueueMetadata(oauthClientID)
	if !ok {
		http.Error(w, queueDoesNotExistMsg+oauthClientID, http.StatusNotFound)
		return
	}

	key, err := s.parseAuth(r)
	if err != nil {
		log.WithError(err).Error("Parse auth")
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	cqi, ok := queuesRes.CreationQueues[gpuName]
	if !ok {
		http.Error(w, queueDoesNotExistMsg+oauthClientID+"/"+gpuName, http.StatusNotFound)
		return
	}

	if key != cqi.SecretKey {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if r.Method == http.MethodGet {
		// Read the latest message(s).
		query := r.URL.Query()
		maxMsgsStr := query.Get("max_num_messages")
		maxMsgs, err := strconv.ParseInt(maxMsgsStr, 10, 64)
		if err != nil {
			log.WithError(err).Error("Parse max num messages")
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		visTimeoutSecondsStr := query.Get("vis_timeout_seconds")
		visTimeoutSeconds, err := strconv.ParseInt(visTimeoutSecondsStr, 10, 64)
		if err != nil {
			log.WithError(err).Error("Parse vis timeout seconds")
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		s.mu.Lock()

		q := s.CreationQueues[oauthClientID][gpuName]

		var msgs []receiveMessageResponseItem
		for qi, mi := 0, 0; qi < len(q) && mi < int(maxMsgs); qi++ {
			if qm := q[qi]; qm.isVisible() {
				msgs = append(msgs, receiveMessageResponseItem{
					MessageID:     qm.MessageID,
					ReceiptHandle: qm.ReceiptHandle,
					Body:          qm.Body,
				})
				if visTimeoutSeconds > 0 {
					q[qi].VisTimeout = time.Now().Add(time.Duration(visTimeoutSeconds) * time.Second)
				}
				mi++
			}
		}

		s.CreationQueues[oauthClientID][gpuName] = q

		s.mu.Unlock()

		res := receiveMessageResponse{
			Messages: msgs,
		}

		b, err := json.Marshal(res)
		if err != nil {
			log.WithError(err).Error("Encode get messages response")
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	} else {
		// Create a new message.
		body := &function.CreationQueueMessage{}
		if err := decode(r.Body, &body); err != nil {
			log.WithError(err).Error("Decode creation message request body")
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		newMsg := QueueMessage{
			MessageID:     uuid.New().String(),
			ReceiptHandle: uuid.New().String(),
			RequestID:     body.RequestID,
			Body:          body,
		}

		s.mu.Lock()

		for _, msg := range s.CreationQueues[oauthClientID][gpuName] {
			if msg.RequestID == newMsg.RequestID {
				s.mu.Unlock()
				http.Error(w, "request ID already exists: "+newMsg.RequestID, http.StatusBadRequest)
				return
			}
		}
		s.CreationQueues[oauthClientID][gpuName] = append(s.CreationQueues[oauthClientID][gpuName], newMsg)

		s.mu.Unlock()

		log.WithFields(logrus.Fields{
			"msg_id":         newMsg.MessageID,
			"receipt_handle": newMsg.ReceiptHandle,
			"req_id":         newMsg.RequestID,
			"is_visible":     newMsg.isVisible(),
		}).Info("Created new creation message")

		res := CreateMessageResponse{
			MessageID:     newMsg.MessageID,
			ReceiptHandle: newMsg.ReceiptHandle,
		}

		b, err := json.Marshal(res)
		if err != nil {
			log.WithError(err).Error("Encode create messages response")
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	}
}

func (s *queueService) handleTerminateMessage(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	log := core.GetLogger(r.Context())

	vars := mux.Vars(r)
	oauthClientID := vars["oauth_client_id"]

	log = log.WithFields(logrus.Fields{
		"path":            r.URL.Path,
		"method":          r.Method,
		"oauth_client_id": oauthClientID,
	})

	log.Infof("%s termination message request", r.Method)

	if oauthClientID == "" {
		http.Error(w, "OAuth client ID is empty", http.StatusBadRequest)
		return
	}

	queuesRes, ok := s.getQueueMetadata(oauthClientID)
	if !ok {
		http.Error(w, queueDoesNotExistMsg+oauthClientID, http.StatusNotFound)
		return
	}

	key, err := s.parseAuth(r)
	if err != nil {
		log.WithError(err).Error("Parse auth")
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	if key != queuesRes.TerminationQueue.SecretKey {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if r.Method == http.MethodGet {
		// Read the latest message(s).
		query := r.URL.Query()
		maxMsgsStr := query.Get("max_num_messages")
		maxMsgs, err := strconv.ParseInt(maxMsgsStr, 10, 64)
		if err != nil {
			log.WithError(err).Error("Parse max num messages")
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		visTimeoutSecondsStr := query.Get("vis_timeout_seconds")
		visTimeoutSeconds, err := strconv.ParseInt(visTimeoutSecondsStr, 10, 64)
		if err != nil {
			log.WithError(err).Error("Parse vis timeout seconds")
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		s.mu.Lock()

		q := s.TerminationQueues[oauthClientID]

		var msgs []receiveMessageResponseItem
		for qi, mi := 0, 0; qi < len(q) && mi < int(maxMsgs); qi++ {
			if qm := q[qi]; qm.isVisible() {
				msgs = append(msgs, receiveMessageResponseItem{
					MessageID:     qm.MessageID,
					ReceiptHandle: qm.ReceiptHandle,
					Body:          qm.Body,
				})
				if visTimeoutSeconds > 0 {
					q[qi].VisTimeout = time.Now().Add(time.Duration(visTimeoutSeconds) * time.Second)
				}
				mi++
			}
		}

		s.TerminationQueues[oauthClientID] = q

		s.mu.Unlock()

		res := receiveMessageResponse{
			Messages: msgs,
		}

		b, err := json.Marshal(res)
		if err != nil {
			log.WithError(err).Error("Encode get messages response")
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	} else {
		// Create a new message.
		body := &types.ICMSTerminationMessage{}
		if err := decode(r.Body, &body); err != nil {
			log.WithError(err).Error("Decode termination message request body")
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		newMsg := QueueMessage{
			MessageID:     uuid.New().String(),
			ReceiptHandle: uuid.New().String(),
			RequestID:     body.RequestID,
			Body:          body,
		}

		s.mu.Lock()

		for _, msg := range s.TerminationQueues[oauthClientID] {
			if msg.RequestID == newMsg.RequestID {
				s.mu.Unlock()
				http.Error(w, "request ID already exists: "+newMsg.RequestID, http.StatusBadRequest)
				return
			}
		}
		s.TerminationQueues[oauthClientID] = append(s.TerminationQueues[oauthClientID], newMsg)

		s.mu.Unlock()

		log.WithFields(logrus.Fields{
			"msg_id":         newMsg.MessageID,
			"receipt_handle": newMsg.ReceiptHandle,
			"req_id":         newMsg.RequestID,
			"is_visible":     newMsg.isVisible(),
		}).Info("Created new message")

		res := CreateMessageResponse{
			MessageID:     newMsg.MessageID,
			ReceiptHandle: newMsg.ReceiptHandle,
		}

		b, err := json.Marshal(res)
		if err != nil {
			log.WithError(err).Error("Encode create messages response")
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	}
}

func (s *queueService) handleUpdateOrDeleteCreateMessage(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	log := core.GetLogger(r.Context())

	vars := mux.Vars(r)
	oauthClientID := vars["oauth_client_id"]
	gpuName := vars["gpu_name"]
	receiptHandle := vars["recp_hdl"]

	log = log.WithFields(logrus.Fields{
		"path":            r.URL.Path,
		"method":          r.Method,
		"oauth_client_id": oauthClientID,
		"gpu_name":        gpuName,
		"recp_hdl":        receiptHandle,
	})

	log.Infof("%s existing creation message request", r.Method)

	if oauthClientID == "" {
		http.Error(w, "OAuth client ID is empty", http.StatusNotFound)
		return
	}
	if receiptHandle == "" {
		http.Error(w, "receipt handle is empty", http.StatusNotFound)
		return
	}
	queuesRes, ok := s.getQueueMetadata(oauthClientID)
	if !ok {
		http.Error(w, queueDoesNotExistMsg+oauthClientID, http.StatusNotFound)
		return
	}

	key, err := s.parseAuth(r)
	if err != nil {
		log.WithError(err).Error("Parse auth")
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	cqi, ok := queuesRes.CreationQueues[gpuName]
	if !ok {
		http.Error(w, queueDoesNotExistMsg+oauthClientID+"/"+gpuName, http.StatusNotFound)
		return
	}

	if key != cqi.SecretKey {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	var updateQS func(i int)
	if r.Method == http.MethodPut {
		log.Info("Updating creation message visibility")

		query := r.URL.Query()
		visTimeoutStr := query.Get("vis_timeout_seconds")
		visTimeoutSeconds, err := strconv.ParseInt(visTimeoutStr, 10, 64)
		if err != nil {
			log.WithError(err).Error("Parse vis timeout seconds")
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		updateQS = func(i int) {
			s.CreationQueues[oauthClientID][gpuName][i].VisTimeout = time.Now().
				Add(time.Duration(visTimeoutSeconds) * time.Second)
		}
	} else {
		log.Infoln("Deleting creation message:", receiptHandle)

		updateQS = func(i int) {
			q := s.CreationQueues[oauthClientID][gpuName]
			q = append(q[:i], q[i+1:]...)
			s.CreationQueues[oauthClientID][gpuName] = q
		}
	}

	s.mu.Lock()

	receiptHandleFound := false
	for i, qm := range s.CreationQueues[oauthClientID][gpuName] {
		if qm.ReceiptHandle == receiptHandle {
			receiptHandleFound = true
			updateQS(i)
			break
		}
	}

	s.mu.Unlock()

	if !receiptHandleFound {
		http.Error(w, "receipt handle not found: "+receiptHandle, http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (s *queueService) handleUpdateOrDeleteTerminateMessage(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	log := core.GetLogger(r.Context())

	vars := mux.Vars(r)
	oauthClientID := vars["oauth_client_id"]
	receiptHandle := vars["recp_hdl"]

	log = log.WithFields(logrus.Fields{
		"path":            r.URL.Path,
		"method":          r.Method,
		"oauth_client_id": oauthClientID,
		"recp_hdl":        receiptHandle,
	})

	log.Infof("%s existing termination message request", r.Method)

	if oauthClientID == "" {
		http.Error(w, "OAuth client ID is empty", http.StatusNotFound)
		return
	}
	if receiptHandle == "" {
		http.Error(w, "receipt handle is empty", http.StatusNotFound)
		return
	}
	queuesRes, ok := s.getQueueMetadata(oauthClientID)
	if !ok {
		http.Error(w, queueDoesNotExistMsg+oauthClientID, http.StatusNotFound)
		return
	}

	key, err := s.parseAuth(r)
	if err != nil {
		log.WithError(err).Error("Parse auth")
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	if key != queuesRes.TerminationQueue.SecretKey {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	var updateQS func(i int)
	if r.Method == http.MethodPut {
		log.Info("Updating termination message visibility")

		query := r.URL.Query()
		visTimeoutStr := query.Get("vis_timeout_seconds")
		visTimeoutSeconds, err := strconv.ParseInt(visTimeoutStr, 10, 64)
		if err != nil {
			log.WithError(err).Error("Parse vis timeout seconds")
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		updateQS = func(i int) {
			s.TerminationQueues[oauthClientID][i].VisTimeout = time.Now().
				Add(time.Duration(visTimeoutSeconds) * time.Second)
		}
	} else {
		log.Infoln("Deleting termination message:", receiptHandle)

		updateQS = func(i int) {
			q := s.TerminationQueues[oauthClientID]
			q = append(q[:i], q[i+1:]...)
			s.TerminationQueues[oauthClientID] = q
		}
	}

	s.mu.Lock()

	receiptHandleFound := false
	for i, qm := range s.TerminationQueues[oauthClientID] {
		if qm.ReceiptHandle == receiptHandle {
			receiptHandleFound = true
			updateQS(i)
			break
		}
	}

	s.mu.Unlock()

	if !receiptHandleFound {
		http.Error(w, "receipt handle not found: "+receiptHandle, http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (s *queueService) parseAuth(r *http.Request) (string, error) {
	authHdrVal := r.Header.Get("Authorization")
	if authHdrVal == "" || !strings.HasPrefix(authHdrVal, "Bearer ") {
		return "", fmt.Errorf("bad auth header: %q", authHdrVal)
	}

	key := strings.TrimPrefix(authHdrVal, "Bearer ")
	return key, nil
}

func decode(r io.Reader, v any) error {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
