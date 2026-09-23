// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// A CPU-only OpenAI backend and legacy worker-auth fixture for deployment tests.
// It is not a standalone authorizer and must not be shipped as one.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	pb "github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/nvcf/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type auth struct {
	pb.UnimplementedLlmGatewayServer
}

func (auth) AuthLlmWorker(_ context.Context, r *pb.AuthLlmWorkerRequest) (*pb.AuthLlmWorkerResponse, error) {
	if r.WorkerToken == "" || r.WorkerToken != os.Getenv("WORKER_TOKEN") {
		return nil, status.Error(codes.Unauthenticated, "invalid test worker token")
	}
	return &pb.AuthLlmWorkerResponse{RoutingKey: os.Getenv("ROUTING_KEY")}, nil
}

func main() {
	if os.Getenv("MODE") == "auth" {
		listener, err := net.Listen("tcp", ":50051")
		if err != nil {
			log.Fatal(err)
		}
		g := grpc.NewServer()
		pb.RegisterLlmGatewayServer(g, auth{})
		log.Fatal(g.Serve(listener))
	}
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, "ok") })
	http.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []any{map[string]any{"id": os.Getenv("MODEL"), "object": "model", "owned_by": "deployment-test"}}})
	})
	http.HandleFunc("/v1/chat/completions", chat)
	log.Fatal(http.ListenAndServe(":8090", nil))
}

func chat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if req.Model != os.Getenv("MODEL") {
		http.Error(w, "unknown model", http.StatusNotFound)
		return
	}
	base := map[string]any{"id": "chatcmpl-poc", "created": time.Now().Unix(), "model": req.Model}
	usage := map[string]int{"prompt_tokens": 4, "completion_tokens": 2, "total_tokens": 6}
	if !req.Stream {
		base["object"] = "chat.completion"
		base["choices"] = []any{map[string]any{"index": 0, "message": map[string]string{"role": "assistant", "content": "POC routing works"}, "finish_reason": "stop"}}
		base["usage"] = usage
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(base)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	base["object"] = "chat.completion.chunk"
	for _, token := range []string{"POC ", "routing works"} {
		base["choices"] = []any{map[string]any{"index": 0, "delta": map[string]string{"content": token}, "finish_reason": nil}}
		data, _ := json.Marshal(base)
		fmt.Fprintf(w, "data: %s\n\n", data)
		w.(http.Flusher).Flush()
	}
	base["choices"] = []any{map[string]any{"index": 0, "delta": map[string]string{}, "finish_reason": "stop"}}
	base["usage"] = usage
	data, _ := json.Marshal(base)
	fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", data)
	w.(http.Flusher).Flush()
}
