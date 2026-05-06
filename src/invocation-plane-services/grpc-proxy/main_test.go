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
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/samber/lo"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httputil"
	"os"
	"strings"
	"testing"
	"time"
)

const functionId = "4f463643-db0d-47a3-9f1d-5de3c1febc79"

// const functionVersionId = "75abbbf2-1632-416e-8cbf-a857dc7b3d03" // .7
// const functionVersionId = "436561ba-d879-41d5-8e7f-3b523a41c746" // .3
const functionVersionId = "2497e7da-f037-42a2-9646-e1882ba55d18" // .9

func TestRealGrpcProxyMultipleConnections(t *testing.T) {
	t.SkipNow() // comment me to test locally.

	logger := lo.Must(zap.NewDevelopment())
	zap.ReplaceGlobals(logger)

	ctx := context.Background()

	apiKey := os.Getenv("API_KEY")

	cookieJar, err := cookiejar.New(nil)
	if err != nil {
		panic(err)
	}
	client := &http.Client{Jar: cookieJar, Transport: &http.Transport{}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://stg.grpc.nvcf.nvidia.com/echo", bytes.NewBufferString(`{"message": "123"}`))
	if err != nil {
		panic(err)
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("function-id", functionId)
	request.Header.Set("function-version-id", functionVersionId)
	request.Close = false
	zap.L().Info("sending first request")
	response, err := client.Do(request)
	if err != nil {
		panic(err)
	}
	defer response.Body.Close()
	dumpResponse, err := httputil.DumpResponse(response, true)
	if err != nil {
		panic(err)
	}
	zap.L().Info("first response", zap.ByteString("content", dumpResponse))
	if response.StatusCode != http.StatusOK {
		panic("invalid response status")
	}

	secondClient := &http.Client{Jar: cookieJar, Transport: &http.Transport{}}
	secondRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://stg.grpc.nvcf.nvidia.com/echo", bytes.NewBufferString(`{"message": "123"}`))
	if err != nil {
		panic(err)
	}
	secondRequest.Header.Set("Authorization", "Bearer "+apiKey)
	secondRequest.Header.Set("function-id", functionId)
	request.Header.Set("function-version-id", functionVersionId)
	secondRequest.Close = false
	zap.L().Info("sending second request")
	secondResponse, err := secondClient.Do(secondRequest)
	if err != nil {
		panic(err)
	}
	defer secondResponse.Body.Close()
	dumpSecondResponse, err := httputil.DumpResponse(secondResponse, true)
	if err != nil {
		panic(err)
	}
	zap.L().Info("second response", zap.ByteString("content", dumpSecondResponse))
	if secondResponse.StatusCode != http.StatusOK {
		panic("invalid response status")
	}
}

func TestRealGrpcProxyLong(t *testing.T) {
	t.SkipNow() // comment me to test locally.

	logger := lo.Must(zap.NewDevelopment())
	zap.ReplaceGlobals(logger)

	ctx := context.Background()
	//functionId := "6c15cde0-d06a-4bbf-a4e1-e587c4ca9a11"
	apiKey := os.Getenv("API_KEY")

	cookieJar, err := cookiejar.New(nil)
	if err != nil {
		panic(err)
	}
	client := &http.Client{Jar: cookieJar, Transport: &http.Transport{}}
	tries := 0
	for {
		func() {
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://stg.grpc.nvcf.nvidia.com/echo", bytes.NewBufferString(`{"message": "123"}`))
			if err != nil {
				panic(err)
			}
			request.Header.Set("Authorization", "Bearer "+apiKey)
			request.Header.Set("function-id", functionId)
			request.Header.Set("function-version-id", functionVersionId)
			request.Close = false
			response, err := client.Do(request)
			if err != nil {
				panic(err)
			}
			defer response.Body.Close()
			dumpResponse, err := httputil.DumpResponse(response, true)
			if err != nil {
				panic(err)
			}
			zap.L().Info("response", zap.ByteString("content", dumpResponse))
			if response.StatusCode != http.StatusOK {
				if tries == 0 && response.StatusCode == http.StatusNotFound {
					tries++
					return
				}
				panic("invalid response status")
			}
			tries = 0
		}()
		if tries == 0 {
			time.Sleep(15 * time.Second)
		}
	}
}

func TestRealGrpcProxyConcurrentConnections(t *testing.T) {
	t.SkipNow() // comment me to test locally.

	logger := lo.Must(zap.NewDevelopment())
	zap.ReplaceGlobals(logger)

	apiKey := os.Getenv("API_KEY")

	group, ctx := errgroup.WithContext(t.Context())
	for i := 0; i < 1; i++ {
		group.Go(func() error {
			client := &http.Client{Transport: &http.Transport{}}
			for j := 0; j < 100; j++ {
				err := func() error {
					request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://stg.grpc.nvcf.nvidia.com/echo", bytes.NewBufferString(`{"message": "123"}`))
					if err != nil {
						return err
					}
					request.Header.Set("Authorization", "Bearer "+apiKey)
					request.Header.Set("function-id", functionId)
					request.Header.Set("function-version-id", functionVersionId)
					request.Close = false
					zap.L().Info("sending request", zap.Int("num", j), zap.Int("connection", i))
					response, err := client.Do(request)
					if err != nil {
						return err
					}
					defer response.Body.Close()
					dumpResponse, err := httputil.DumpResponse(response, true)
					if err != nil {
						return err
					}
					zap.L().Info("response", zap.Int("num", j), zap.Int("connection", i), zap.ByteString("content", dumpResponse))
					if response.StatusCode != http.StatusOK {
						return errors.New("invalid response status")
					}
					return nil
				}()
				if err != nil {
					return err
				}
			}
			return nil
		})
	}
	err := group.Wait()
	if err != nil {
		t.Fatal(err)
	}
}

func TestRealGrpcProxy(t *testing.T) {
	t.SkipNow() // comment me to test locally.

	logger := lo.Must(zap.NewDevelopment())
	zap.ReplaceGlobals(logger)

	ctx := context.Background()
	functionId := "f7f55667-0e91-47aa-90ec-451dcfb81790"
	apiKey := os.Getenv("API_KEY")

	// 4 req resp
	// 1 req -> streaming resp for the rest of the conn
	// 20 req resp, sleeping between

	client := &http.Client{Transport: &http.Transport{}}
	for i := 0; i < 4; i++ {
		sendReqResp(ctx, apiKey, functionId, client)
	}
	go sendStreamingRequest(ctx, apiKey, functionId, client)
	for i := 0; i < 20; i++ {
		sendReqResp(ctx, apiKey, functionId, client)
		time.Sleep(4 * time.Second)
	}
}

func sendStreamingRequest(ctx context.Context, apiKey string, functionId string, client *http.Client) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://stg.grpc.nvcf.nvidia.com/echo", bytes.NewBufferString(fmt.Sprintf(`{
        "message": "%s",
        "repeats": 500,
        "stream": true,
		"delay": 31
    }`, strings.Repeat("a", 1024*33))))
	if err != nil {
		panic(err)
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("function-id", functionId)
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		panic(err)
	}
	defer response.Body.Close()
	dumpResponse, err := httputil.DumpResponse(response, false)
	if err != nil {
		panic(err)
	}
	zap.L().Info("streaming response", zap.ByteString("content", dumpResponse))
	if response.StatusCode != http.StatusOK {
		panic("invalid response status")
	}
	_, err = io.Copy(os.Stdout, response.Body)
	if err != nil {
		panic(err)
	}
}

func sendReqResp(ctx context.Context, apiKey string, functionId string, client *http.Client) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://stg.grpc.nvcf.nvidia.com/echo", bytes.NewBufferString(`{
        "message": "randomString",
        "repeats": 1
    }`))
	if err != nil {
		panic(err)
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("function-id", functionId)
	response, err := client.Do(request)
	if err != nil {
		panic(err)
	}
	defer response.Body.Close()
	dumpResponse, err := httputil.DumpResponse(response, true)
	if err != nil {
		panic(err)
	}
	zap.L().Info("response", zap.ByteString("content", dumpResponse))
	if response.StatusCode != http.StatusOK {
		panic("invalid response status")
	}
}

func TestRealGrpcProxyGrpc(t *testing.T) {
	t.SkipNow() // comment me to test locally.

	logger := lo.Must(zap.NewDevelopment())
	zap.ReplaceGlobals(logger)

	ctx := context.Background()
	functionId := "f7f55667-0e91-47aa-90ec-451dcfb81790"
	apiKey := os.Getenv("API_KEY")

	// 4 req resp
	// 1 req -> streaming resp for the rest of the conn
	// 20 req resp, sleeping between

	client := &http.Client{Transport: &http.Transport{}}
	for i := 0; i < 4; i++ {
		sendReqResp(ctx, apiKey, functionId, client)
	}
	go sendStreamingRequest(ctx, apiKey, functionId, client)
	for i := 0; i < 20; i++ {
		sendReqResp(ctx, apiKey, functionId, client)
		time.Sleep(4 * time.Second)
	}
}
