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
	"errors"
	"os"
	"os/signal"
	"reflect"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/config"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/tracing"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/types"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/utils"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-init/configs"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-init/internal/workerinit"
)

func Run() {
	logger := utils.NewProductionLogger()
	defer logger.Close()
	defer tracing.Shutdown()

	rootCmd := NewRootCommand(context.Background())
	err := rootCmd.Execute()
	// A SIGINT/SIGTERM cancels the run context, which surfaces as
	// context.Canceled (deadline -> DeadlineExceeded) wrapped in a
	// WorkerError. That is a normal graceful shutdown, not a panic.
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		utils.ExitReason(err)
		zap.S().Panic(err)
	}
}

func NewRootCommand(ctx context.Context) *cobra.Command {
	var cfgFile string
	var initializer workerinit.Initializer

	rootCmd := &cobra.Command{
		Use:          "init",
		Short:        "NVCF Worker Init Service",
		Long:         `NVIDIA Cloud Worker Init Container`,
		SilenceUsage: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			v, err := config.InitConfig(cmd, cfgFile, "", "")
			if err != nil {
				return types.NewInternalError(err)
			}
			cmd.Flags().VisitAll(func(flag *pflag.Flag) {
				v.MustBindEnv(flag.Name)
			})

			initConfig := configs.InitConfig{}
			err = v.Unmarshal(&initConfig)
			if err != nil {
				return types.NewInternalError(err)
			}

			initializer, err = workerinit.NewInitializer(initConfig)
			if err != nil {
				return types.NewInternalError(err)
			}

			if err := initializer.Setup(); err != nil {
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

			signalChan := make(chan os.Signal, 1)
			done := make(chan struct{})
			signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM)
			defer func() {
				signal.Stop(signalChan)
				close(done)
			}()
			go func() {
				select {
				case <-signalChan:
					cancel()
				case <-done:
				}
			}()

			if err := initializer.Run(ctx); err != nil {
				return types.NewInternalError(err)
			}
			return nil
		},
	}

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default: $HOME/"+config.DefaultConfigPath+"/config.yaml)")

	configType := reflect.TypeOf(configs.InitConfig{})
	for i := 0; i < configType.NumField(); i++ {
		field := configType.Field(i)
		if field.Type.Kind() == reflect.Struct {
			embedType := field.Type
			for j := 0; j < embedType.NumField(); j++ {
				field := embedType.Field(j)
				envName := field.Tag.Get("mapstructure")
				rootCmd.Flags().String(envName, "", "")
			}
			continue
		}
		envName := field.Tag.Get("mapstructure")
		rootCmd.Flags().String(envName, "", "")
	}

	return rootCmd
}
