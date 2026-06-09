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
	"reflect"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/config"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/logs"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/tracing"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/types"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/utils"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-utils/worker"
)

func Run() {
	logger := utils.NewProductionLogger()
	defer logger.Close()
	defer tracing.Shutdown()
	if utils.LevelFromEnv().Level() == zap.DebugLevel {
		qlogsPath := os.Getenv("QLOGDIR")
		if qlogsPath == "" {
			qlogsPath = "/qlogs"
		}
		_, err := os.Stat(qlogsPath)
		if errors.Is(err, os.ErrNotExist) {
			err = utils.CreateDirectory(qlogsPath, os.FileMode(0755))
			if err != nil {
				zap.L().Warn("Failed to create qlogs directory", zap.Error(err))
			}
		} else if err != nil {
			zap.L().Warn("Failed to stat qlogs directory", zap.Error(err))
		}
	}
	err := NewRootCommand(context.Background(), logger).Execute()
	if err != nil && err.Error() != "received signal interrupt" {
		utils.ExitReason(err)
		zap.S().Panic(err)
	}
}

func NewRootCommand(ctx context.Context, zapLogger *logs.ZapLogger) *cobra.Command {
	var cfgFile string
	var w *worker.NVCFWorker

	rootCmd := &cobra.Command{
		Use:          "worker",
		Short:        "NVCF worker service",
		Version:      utils.Version,
		SilenceUsage: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			v, err := config.InitConfig(cmd, cfgFile, "", "")
			if err != nil {
				return types.NewInternalError(err)
			}
			cmd.Flags().VisitAll(func(flag *pflag.Flag) {
				v.MustBindEnv(flag.Name)
			})
			workerConfig := worker.Config{}
			err = v.Unmarshal(&workerConfig, viperDecoderConfig())
			if err != nil {
				return types.NewInternalError(err)
			}
			w, err = worker.NewNVCFWorker(ctx, zapLogger, workerConfig)
			if err != nil {
				return err
			}
			return w.Setup()
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return w.Run(true)
		},
	}
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/"+config.DefaultConfigPath+"/config.yaml)")

	configType := reflect.TypeOf(worker.Config{})
	for i := 0; i < configType.NumField(); i++ {
		field := configType.Field(i)
		envName := field.Tag.Get("mapstructure")
		rootCmd.Flags().String(envName, "", "")
	}

	return rootCmd
}

func viperDecoderConfig() viper.DecoderConfigOption {
	return func(dc *mapstructure.DecoderConfig) {
		dc.DecodeHook = mapstructure.ComposeDecodeHookFunc(
			utils.StringToDurationHookFunc(),
		)
	}
}
