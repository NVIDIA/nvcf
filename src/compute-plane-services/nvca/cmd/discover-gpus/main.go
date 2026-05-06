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
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	internalutil "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/cmd/internal"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

type options struct {
	out             io.Writer
	allGPUTypes     bool
	clusterProvider string
}

func main() {
	ctx := context.Background()

	currContext := internalutil.NewContextFlag()
	opts := options{
		out: os.Stdout,
	}
	flag.BoolVar(&opts.allGPUTypes, "all-gpus", false,
		"Return all GPU types, not just the alphanumerically first")
	flag.StringVar(&opts.clusterProvider, "provider", "",
		"The k8s cluster provider, ex. \"ON-PREM\", \"AWS\" (from 'cluster.provider' in NVCA values)")
	flag.Parse()

	k8sClient, _, err := internalutil.NewK8sClient(ctx, *currContext)
	if err != nil {
		log.Fatal(err)
	}

	normalizedClusterProvider, err := types.NormalizeClusterProvider(opts.clusterProvider)
	if err != nil {
		log.Fatalf("normalize cluster provider: %v", err)
	}
	opts.clusterProvider = normalizedClusterProvider

	if err := run(ctx, k8sClient, opts); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, client kubernetes.Interface, opts options) error {
	nfcOpts := nodefeatures.DynamicClientOptions{
		AttributeFetcher: featureflag.DefaultFetcher,
	}

	f := informers.NewSharedInformerFactoryWithOptions(
		client,
		1*time.Second,
		nodefeatures.NewNodeInformerOptions(nfcOpts.AttributeFetcher)...,
	)

	ni := f.Core().V1().Nodes()
	pi := f.Core().V1().Pods()

	f.Start(ctx.Done())

	// Since the GetAllBackendGPUs will error if MultipleGPUTypesAllowed=true,
	// always get all GPUs then parse them here.
	nfc := nodefeatures.NewDynamicClient(nil, ni.Lister(), opts.clusterProvider, nfcOpts)

	{
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if !cache.WaitForCacheSync(cctx.Done(), ni.Informer().HasSynced) {
			return fmt.Errorf("node cache sync failed")
		}
		cctx, cancel = context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if !cache.WaitForCacheSync(cctx.Done(), pi.Informer().HasSynced) {
			return fmt.Errorf("pod cache sync failed")
		}
	}

	backendGPUs, err := nfc.GetAllBackendGPUs(ctx)
	if err != nil {
		return err
	}

	backendGPUs = parseBackendGPUs(backendGPUs, opts.allGPUTypes)

	bgJSON, err := json.MarshalIndent(backendGPUs, "", "  ")
	if err != nil {
		return err
	}

	fmt.Fprintln(opts.out, string(bgJSON))
	return nil
}

func parseBackendGPUs(backendGPUs []types.BackendGPU, allGPUTypes bool) []types.BackendGPU {
	if allGPUTypes || len(backendGPUs) == 0 {
		return backendGPUs
	}
	// These should already be sorted, so just return the first one.
	return backendGPUs[:1]
}
