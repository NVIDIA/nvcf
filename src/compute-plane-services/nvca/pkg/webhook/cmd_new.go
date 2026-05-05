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

package webhook

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/version"
	"github.com/bombsimon/logrusr/v4"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/cmdutil"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures/sharedcluster"
)

func NewCobraCommand() *cobra.Command {
	var configFile string
	cmd := &cobra.Command{
		Use:     "webhook-server",
		Short:   "NVIDIA Cluster Agent webhook server",
		Version: version.ReleaseString(),
		RunE: func(cmd *cobra.Command, _ []string) (err error) {
			ctx := cmd.Context()

			if configFile == "" {
				return fmt.Errorf("config file is required")
			}

			cfg, err := nvcaconfig.Init(configFile)
			if err != nil {
				return err
			}

			cfg = setDefaults(cfg.Complete())

			log := core.GetLogger(ctx)
			if log.Logger.Level, err = logrus.ParseLevel(cfg.Agent.LogLevel); err != nil {
				return err
			}

			// Move logs from all client-go logs into
			// the default logrus logger
			k8sLogger := logrusr.New(log, logrusr.WithReportCaller())
			ctrllog.SetLogger(k8sLogger)
			klog.SetLogger(k8sLogger)
			ctx = ctrllog.IntoContext(ctx, k8sLogger)

			// Feature flag shim
			if err := (&featureflag.CLIFlag{}).Set(strings.Join(cfg.Agent.FeatureFlags, ",")); err != nil {
				return fmt.Errorf("set featureflag CLI flag for config: %v", err)
			}
			// Cluster attributes shim
			if err := (&featureflag.AttrCLIFlag{}).Set(strings.Join(cfg.Cluster.Attributes, ",")); err != nil {
				return fmt.Errorf("set attribute CLI flag for config: %v", err)
			}
			// check if map is nil
			if cfg.Webhook.DCGMAnnotations == nil {
				cfg.Webhook.DCGMAnnotations = make(map[string]string)
			}
			dcgmMetricsCfg, err := DCGMMetricsConfigFromAnnotations(cfg.Webhook.DCGMAnnotations)
			if err != nil {
				return err
			}

			k8sClient, err := newK8sClient(ctx, cfg.Agent.KubeconfigPath)
			if err != nil {
				log.WithError(err).Error("Failed to create k8s client")
				return err
			}

			if err := k8sutil.SetConfigDefaultResources(&cfg); err != nil {
				return err
			}

			m := &webhookManager{
				cfg:              cfg,
				namespace:        os.Getenv("POD_NAMESPACE"),
				k8sClient:        k8sClient,
				dcgmMetrics:      dcgmMetricsCfg,
				readTimeout:      5 * time.Second,
				writeTimeout:     10 * time.Second,
				attrFetcher:      featureflag.DefaultFetcher,
				metrics:          metrics.FromContext(ctx),
				addNodePublisher: sharedcluster.AddNodePublisher,
			}

			// Start shared cluster only once since the pod affinity webhook is a subscriber
			// to the returned atomic boolean.
			if err := m.startSharedClusterPubSub(ctx, resync); err != nil {
				return err
			}

			// Detect non-GPU Kata RuntimeClass existence.
			m.startKataRuntimeClassHandler(ctx)

			if err := m.run(ctx); err != nil {
				log.WithError(err).Error("failed to run webhook manager")
				return err
			}
			return nil
		},
	}

	cmd.PersistentFlags().StringVar(&configFile, "config", "", "Config file path")

	return cmd
}

func setDefaults(cfg nvcaconfig.Config) nvcaconfig.Config {
	cmdutil.SetEmptyValue(&cfg.Webhook.SvcAddress, "127.0.0.1:8443")
	return cfg
}
