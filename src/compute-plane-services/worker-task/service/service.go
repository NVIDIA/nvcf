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
	"fmt"
	"os"
	"os/signal"
	"reflect"
	"strings"
	"syscall"
	"time"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/config"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/logs"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/tracing"
	sharedTypes "github.com/NVIDIA/nvcf/src/libraries/go/worker/types"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/utils"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-task/configs"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-task/internal/types"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-task/internal/worker"
)

// ------------------------------------------------------------------------

const (
	withHttpServer    = true
	withoutHttpServer = false
)

// ------------------------------------------------------------------------

// Run is the entrypoint for the NVCT worker service.
func Run() {
	startupTime := time.Now().UTC()
	logger := utils.NewProductionLogger()
	defer logger.Close()
	defer tracing.Shutdown()

	rootCmd := NewRootCommand(context.Background(), logger, startupTime)
	err := rootCmd.Execute()
	// err is returned as a string by nv-kit. type assertion is not possible.
	if err != nil && strings.ToLower(err.Error()) != "received signal interrupt" {
		utils.ExitReason(err)
		zap.S().Panic(err)
	}
}

// ------------------------------------------------------------------------

func NewRootCommand(ctx context.Context, logger *logs.ZapLogger, startupTime time.Time) *cobra.Command {
	var cfgFile string
	var workerConfig configs.Config
	var w *worker.NVCTWorker
	var terminationGracePeriod time.Duration

	rootCmd := &cobra.Command{
		Use:          "worker",
		Short:        "NVCT Worker Service",
		Long:         `NVIDIA Cloud Task Worker Container`,
		SilenceUsage: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			v, err := config.InitConfig(cmd, cfgFile, "", "")
			if err != nil {
				return sharedTypes.NewInternalError(err)
			}
			cmd.Flags().VisitAll(func(flag *pflag.Flag) {
				v.MustBindEnv(flag.Name)
			})

			v.SetDefault("NVCT_RESULT_HANDLING_STRATEGY", "NONE")

			err = v.Unmarshal(&workerConfig, viperDecoderConfig())
			if err != nil {
				return sharedTypes.NewInternalError(err)
			}

			terminationGracePeriod, err = utils.ConvertISO8601Duration(workerConfig.TerminationGracePeriod)
			if err != nil {
				zap.L().Warn("failed to calculate termination grace period, defaulting to no grace period", zap.Error(err))
			}

			w, err = worker.NewNVCTWorker(ctx, logger, workerConfig)
			if err != nil {
				return err
			}

			return w.Setup(withHttpServer)
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
				if err := w.PreStopCheck(ctx); err == nil {
					zap.L().Info("Pre stop check passed, wait for graceful termination")
					workerTerminationPeriod := min(20*terminationGracePeriod/100, configs.MaxTerminationPeriod)
					// Wait for wrapping up ongoing uploads and sending final task results
					// before cancel child goroutines and terminate.
					time.Sleep(terminationGracePeriod - workerTerminationPeriod)
				} else {
					zap.L().Error("Pre stop check failed, terminating", zap.Error(err))
				}
				cancel()
			}()

			return w.Run(ctx, withHttpServer)
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

func viperDecoderConfig() viper.DecoderConfigOption {
	return func(dc *mapstructure.DecoderConfig) {
		dc.DecodeHook = mapstructure.ComposeDecodeHookFunc(
			stringToResultStrategyHookFunc(),
			utils.StringToDurationHookFunc(),
		)
	}
}

func stringToResultStrategyHookFunc() mapstructure.DecodeHookFunc {
	resultHandlingStrategyMapping := map[string]types.ResultHandlingStrategy{
		"UPLOAD": types.UPLOAD_STRATEGY,
		"NONE":   types.NO_STRATEGY,
	}
	return func(f reflect.Type, t reflect.Type, data interface{}) (interface{}, error) {
		if f.Kind() == reflect.String && t == reflect.TypeOf(types.ResultHandlingStrategy(0)) {
			strategyStr := data.(string)
			if strategy, ok := resultHandlingStrategyMapping[strategyStr]; ok {
				return strategy, nil
			}
			return types.UNKNOWN_STRATEGY, fmt.Errorf("invalid result handling strategy: %s", strategyStr)
		}
		return data, nil
	}
}
