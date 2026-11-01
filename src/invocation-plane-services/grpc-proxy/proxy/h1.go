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
package proxy

import (
	"go.uber.org/zap"
	"net/http"
	"time"
)

func setupH1(director *StreamDirector) (*http.Server, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/proxy", director.HijackHandler)
	mux.HandleFunc("/health", func(writer http.ResponseWriter, request *http.Request) {
		zap.L().Info("responding to http1 health")
		writer.WriteHeader(http.StatusOK)
	})
	return &http.Server{
		Addr:        "0.0.0.0:10086",
		IdleTimeout: time.Minute,
		// not using middleware because it wraps the body and prevents us from casting and hijacking
		Handler: mux,
	}, nil
}
