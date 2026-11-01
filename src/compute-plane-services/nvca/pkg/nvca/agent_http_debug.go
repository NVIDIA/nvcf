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

package nvca

import (
	"context"
	"net"
	"net/http"
	"net/http/pprof"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
)

// The debug server exposes pprof endpoints under the /debug/pprof root.
func (a *Agent) startDebugServer(ctx context.Context) error {
	log := core.GetLogger(ctx)

	// Add profiling endpoints normally added to the default http server internally by pprof.
	debugMux := http.NewServeMux()
	debugMux.HandleFunc("/debug/pprof/", pprof.Index)
	debugMux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	debugMux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	debugMux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	server := &http.Server{
		Handler: debugMux,
	}
	listener, err := net.Listen("tcp", a.NVCADebugAddr)
	if err != nil {
		return err
	}

	go func() {
		log.Infof("Serving HTTP at: %v", listener.Addr())
		if err := server.Serve(listener); err != nil {
			log.Error(err)
		}
	}()

	go func() {
		<-ctx.Done()

		newCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		log.Infof("Shutting down debug server at %v", listener.Addr())
		err = server.Shutdown(newCtx)
		if err != nil {
			log.Infof("failed to terminate debug server at %v, err: %v", listener.Addr(), err)
			return
		}
		log.Infof("Terminated debug server at %v", listener.Addr())
	}()

	return nil
}
