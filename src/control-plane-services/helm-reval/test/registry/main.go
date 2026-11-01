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
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/test/registry/registry"
)

func main() {
	golog := log.New(os.Stderr, "[test-registry] ", log.LstdFlags)

	helmRegPort := 8282
	helmRegAddr := fmt.Sprintf("127.0.0.1:%d", helmRegPort)
	helmRegLis, err := net.Listen("tcp", helmRegAddr)
	if err != nil {
		golog.Fatal(err)
	}

	imageRegPort := 8383
	imageRegAddr := fmt.Sprintf("127.0.0.1:%d", imageRegPort)
	imageRegLis, err := net.Listen("tcp", imageRegAddr)
	if err != nil {
		golog.Fatal(err)
	}

	helmRegSrv, err := registry.NewTestHelmRepoServer(golog, helmRegAddr, "test/testchart", "foobar")
	if err != nil {
		golog.Fatal(err)
	}

	imageRegSrv, err := registry.NewImageRegistryServer(golog, imageRegAddr)
	if err != nil {
		golog.Fatal(err)
	}

	go func() {
		golog.Println("Serving helm registry at", helmRegAddr)
		if err := helmRegSrv.Serve(helmRegLis); err != nil && err != http.ErrServerClosed {
			golog.Fatal(err)
		}
	}()
	go func() {
		golog.Println("Serving image registry at", imageRegAddr)
		if err := imageRegSrv.Serve(imageRegLis); err != nil && err != http.ErrServerClosed {
			golog.Fatal(err)
		}
	}()

	if err := registry.PushPublicImages(imageRegAddr, []string{"foo/bar:latest"}); err != nil {
		golog.Fatal(err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	<-ctx.Done()
	golog.Println("Shutting down")
	cancel()
	helmRegSrv.Close()
	imageRegSrv.Close()
}
