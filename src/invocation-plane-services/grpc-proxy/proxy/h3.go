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
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"go.uber.org/zap"
	"math"
	"net/http"
)

func setupH3(director *StreamDirector, config Config) (*http3.Server, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/proxy", director.HijackHandler)
	mux.HandleFunc("/health", func(writer http.ResponseWriter, request *http.Request) {
		zap.L().Info("responding to http3 health")
		writer.WriteHeader(http.StatusOK)
	})

	tlsConfig, err := setupTLS(config)
	if err != nil {
		return nil, err
	}

	http3Server := &http3.Server{
		Addr:      "0.0.0.0:10084",
		TLSConfig: tlsConfig,
		QUICConfig: &quic.Config{
			Allow0RTT:             true,
			MaxIncomingStreams:    math.MaxInt,
			MaxIncomingUniStreams: math.MaxInt,
		},
		// not using middleware because it wraps the body and prevents us from casting and hijacking
		Handler: mux,
	}
	return http3Server, nil
}
