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

package response

import (
	"bytes"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvcf"
	"google.golang.org/protobuf/encoding/protojson"
	"io"
	"net/http"
	"strings"
)

func CreateErrorResponse(source string, code int, detail string) *http.Response {
	t := strings.ReplaceAll(strings.ToLower(http.StatusText(code)), " ", "-")
	errorDetails := &pb.ErrorDetails{
		Type:   "urn:" + source + ":problem-details:" + t,
		Title:  http.StatusText(code),
		Status: uint32(code),
		Detail: detail,
	}
	json, err := protojson.Marshal(errorDetails)
	if err != nil {
		json = []byte("{}") // this will never happen in practice but we'll cover the error case anyway
	}
	resp := &http.Response{
		StatusCode:    code,
		Header:        make(http.Header),
		Body:          io.NopCloser(bytes.NewReader(json)),
		ContentLength: int64(len(json)),
	}
	resp.Header.Set("Content-Type", "application/problem+json")
	return resp
}
