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

package rp

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
)

// The grpc spec requires that a response with a zero length body set the End Of Stream flag in the
// same http/2 frame as the headers frame.
//
// https://github.com/nghttp2/nghttp2/issues/588
// https://github.com/golang/go/issues/56317

var errNoBodyDetected = fmt.Errorf("sentinel value to detect if no body was sent")

type smuggledResponseBodyError struct {
	resp *http.Response
	error
}

func InjectGrpcSupportToReverseProxy(reverseProxy *httputil.ReverseProxy) error {
	mr := reverseProxy.ModifyResponse
	if mr != nil {
		reverseProxy.ModifyResponse = func(response *http.Response) error {
			if err := mr(response); err != nil {
				return err
			}
			return modifyResponse(response)
		}
	} else {
		reverseProxy.ModifyResponse = modifyResponse
	}
	if reverseProxy.ErrorHandler != nil {
		return fmt.Errorf("errorHandler must not be set in existing reverseProxy")
	}
	reverseProxy.ErrorHandler = errorHandler
	return nil
}

// modifyResponse will detect when there is no response body and return an error to be used with
// errorHandler to prevent premature flushing of the response.
func modifyResponse(resp *http.Response) error {
	// proto check to special case switching protocols responses which httputil.ReverseProxy handles differently
	if resp.ProtoMajor == 2 && resp.StatusCode != http.StatusSwitchingProtocols {
		// a nil result slice will not cause us to read anything but should
		// allow us to inspect if the body is done.
		// io.Readers are allowed to indefinitely return 0, nil so it's not foolproof and will be
		// implementation dependent.
		_, err := resp.Body.Read(nil)
		if err == io.EOF {
			return smuggledResponseBodyError{
				resp:  resp,
				error: errNoBodyDetected,
			}
		}
	}
	return nil
}

// errorHandler is meant to be used in conjunction with modifyResponse to prevent premature
// flushing of the response.
func errorHandler(rw http.ResponseWriter, req *http.Request, err error) {
	var smuggledBody smuggledResponseBodyError
	if errors.As(err, &smuggledBody) {
		// copy the response body regularly, without flushing early
		copyHeader(rw.Header(), smuggledBody.resp.Header)
		rw.WriteHeader(smuggledBody.resp.StatusCode)
		for k, vv := range smuggledBody.resp.Trailer {
			k = http.TrailerPrefix + k
			for _, v := range vv {
				rw.Header().Add(k, v)
			}
		}
		return
	}
	// this is a real error.
	rw.WriteHeader(http.StatusBadGateway)
}

func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}
