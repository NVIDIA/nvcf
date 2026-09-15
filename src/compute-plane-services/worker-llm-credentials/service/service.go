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

package service

import (
	"context"
	"os"
	"os/signal"
	"reflect"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/config"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/tracing"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/types"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/utils"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-llm-credentials/configs"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-llm-credentials/internal/worker"
)

func Run() {
	logger := utils.NewProductionLogger()
	defer logger.Close()
	defer tracing.Shutdown()

	rootCmd := NewRootCommand(context.Background())
	err := rootCmd.Execute()
	// err is returned as a string by nv-kit. type assertion is not possible.
	if err != nil && strings.ToLower(err.Error()) != "received signal interrupt" {
		utils.ExitReason(err)
		zap.S().Panic(err)
	}
}

func NewRootCommand(ctx context.Context) *cobra.Command {
	var cfgFile string
	var w *worker.Worker

	rootCmd := &cobra.Command{
		Use:          "llm-credentials",
		Short:        "NVCF Worker LLM Credentials Service",
		Long:         `NVIDIA Cloud Worker LLM Credentials Container`,
		SilenceUsage: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			v, err := config.InitConfig(cmd, cfgFile, "", "")
			if err != nil {
				return types.NewInternalError(err)
			}
			cmd.Flags().VisitAll(func(flag *pflag.Flag) {
				v.MustBindEnv(flag.Name)
			})

			cfg := configs.Config{}
			err = v.Unmarshal(&cfg)
			if err != nil {
				return types.NewInternalError(err)
			}

			w, err = worker.New(cfg)
			if err != nil {
				return types.NewInternalError(err)
			}

			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithCancel(ctx)
			defer func() {
				if ctx.Err() == nil {
					cancel()
				}
			}()

			// Wait for SIGTERM and terminate the worker gracefully
			signalChan := make(chan os.Signal, 1)
			signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM)
			go func() {
				<-signalChan
				cancel()
			}()

			if err := w.Run(ctx); err != nil {
				return types.NewInternalError(err)
			}
			return nil
		},
	}

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default: $HOME/"+config.DefaultConfigPath+"/config.yaml)")

	configType := reflect.TypeOf(configs.Config{})
	for i := 0; i < configType.NumField(); i++ {
		field := configType.Field(i)
		envName := field.Tag.Get("mapstructure")
		rootCmd.Flags().String(envName, "", "")
	}

	return rootCmd
}
