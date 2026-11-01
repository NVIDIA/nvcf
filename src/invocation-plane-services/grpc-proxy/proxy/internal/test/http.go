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
package test

import (
	"go.uber.org/zap"
	"net"
	"net/http"
	"net/http/httptest"
)

func NewHttpServer(URL string, handler http.HandlerFunc) (*httptest.Server, error) {
	ts := httptest.NewUnstartedServer(handler)
	if URL != "" {
		l, err := net.Listen("tcp", URL)
		if err != nil {
			return nil, err
		}
		_ = ts.Listener.Close()
		ts.Listener = l
	}
	zap.L().Info("Starting server", zap.String("url", URL))
	ts.Start()
	return ts, nil
}
