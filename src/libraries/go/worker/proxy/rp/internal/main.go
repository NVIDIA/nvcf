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
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httputil"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/proxy/rp"
)

// echo ” | nghttp -v -H'content-type: application/grpc' -H'accept: application/grpc' -d- http://localhost:50052/com.nvidia.omniverse.core.platform.validation.v1alpha1.ValidationService/ForceInvalidArgument
func main() {
	proxy := &httputil.ReverseProxy{
		Transport: &http2.Transport{
			AllowHTTP: true,
			DialTLS: func(network string, addr string, cfg *tls.Config) (net.Conn, error) {
				return net.Dial("tcp", "localhost:50051")
			},
		},
		FlushInterval: -1,
		Director: func(req *http.Request) {
			req.URL.Scheme = "http"
		},
	}
	err := rp.InjectGrpcSupportToReverseProxy(proxy)
	if err != nil {
		panic(err)
	}
	handler := h2c.NewHandler(proxy, &http2.Server{})

	server := &http.Server{
		Addr:    "localhost:50052",
		Handler: handler,
	}
	defer server.Close()
	err = server.ListenAndServe()
	if err != nil {
		panic(err)
	}
}
