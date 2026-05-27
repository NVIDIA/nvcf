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
	"os"
	"reflect"

	"ai-api-gateway-service/gateway"

	"github.com/carlmjohnson/versioninfo"
	"github.com/go-logr/zapr"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"go.uber.org/zap"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/NVIDIA/nvcf-go/pkg/nvkit/config"
	"github.com/NVIDIA/nvcf-go/pkg/nvkit/logs"
	"github.com/NVIDIA/nvcf-go/pkg/nvkit/tracing"
)

func main() {
	logger := setupLogger()
	defer tracing.Shutdown()
	err := NewRootCommand(logger).Execute()
	if err != nil && err.Error() != "received signal interrupt" {
		zap.S().Panic(err)
	}
}

func setupLogger() *logs.ZapLogger {
	zapLogger := logs.NewZapLogger(zap.NewAtomicLevelAt(zap.InfoLevel))
	zap.ReplaceGlobals(zapLogger.GetZapLogger())
	zap.RedirectStdLog(zapLogger.GetZapLogger())
	ctrl.SetLogger(zapr.NewLogger(zapLogger.GetZapLogger()))
	return zapLogger
}

func NewRootCommand(zapLogger *logs.ZapLogger) *cobra.Command {
	var cfgFile string
	var w *gateway.NVCFGateway

	rootCmd := &cobra.Command{
		Use:          "gateway",
		Short:        "NVCF Vanity Gateway Service",
		Version:      getVersion(),
		SilenceUsage: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			v, err := config.InitConfig(cmd, cfgFile, "", "")
			if err != nil {
				return err
			}
			cmd.Flags().VisitAll(func(flag *pflag.Flag) {
				v.MustBindEnv(flag.Name)
			})
			c := gateway.Config{}
			err = v.Unmarshal(&c)
			if err != nil {
				return err
			}
			w, err = gateway.NewNVCFGateway(zapLogger, c)
			if err != nil {
				return err
			}
			return w.Setup()
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return w.Run()
		},
	}
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/"+config.DefaultConfigPath+"/config.yaml)")

	configType := reflect.TypeOf(gateway.Config{})
	for i := 0; i < configType.NumField(); i++ {
		field := configType.Field(i)
		envName := field.Tag.Get("mapstructure")
		rootCmd.Flags().String(envName, "", "")
	}

	return rootCmd
}

func getVersion() string {
	if version := os.Getenv("APP_VERSION"); version != "" {
		return version
	}
	return versioninfo.Revision
}
