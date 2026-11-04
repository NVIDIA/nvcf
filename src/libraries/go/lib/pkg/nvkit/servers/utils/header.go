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

package utils

import (
	"context"
	"strings"

	grpctransport "github.com/go-kit/kit/transport/grpc"
	"github.com/google/uuid"
	"google.golang.org/grpc/metadata"
)

var (
	StandardHeaders = []string{
		HeaderKeyRequestID,
		HeaderKeyAuditID,
		HeaderETag,
	}
)

const HeaderNVPrefix = "nvkit-"

type headerKey struct{}
type headers map[string]string

// HeaderAppender adds header information to the response
// NOTE: This function is only called in the success path of go-kit's framework.
// In the error-path, error-handler handles populating the right header
func HeaderAppender() grpctransport.ServerResponseFunc {
	return func(ctx context.Context, header *metadata.MD, trailer *metadata.MD) context.Context {
		// Since the API executed successfully, we can pick the new etag header
		// and pass it in the response header
		headersVal := ctx.Value(headerKey{})
		if headersVal != nil && headersVal.(headers) != nil {
			for k, v := range headersVal.(headers) {
				origHdr := strings.TrimPrefix(k, HeaderNVPrefix)
				if origHdr == HeaderETag {
					continue
				} else if origHdr == HeaderNewETag {
					k = HeaderNVPrefix + HeaderETag
				}
				*header = metadata.Join(*header, metadata.Pairs(k, v))
			}
		}
		return ctx
	}
}

// HeaderProvider provides a way to append headers to the request context.
func HeaderProvider() grpctransport.ServerRequestFunc {
	return func(ctx context.Context, md metadata.MD) context.Context {
		h := headers{}
		for _, hdr := range StandardHeaders {
			val := md.Get(hdr)
			if len(val) > 0 && len(val[0]) > 0 {
				h[HeaderNVPrefix+hdr] = val[0]
			}
		}

		newEtag := uuid.New().String()
		newEtag = strings.ReplaceAll(newEtag, "-", "")
		h[HeaderNVPrefix+HeaderNewETag] = newEtag

		ctx = context.WithValue(ctx, headerKey{}, h)
		return ctx
	}
}
