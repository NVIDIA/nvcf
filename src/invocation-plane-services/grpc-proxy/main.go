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
	"reflect"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf-go/pkg/nvkit/config"
	"github.com/NVIDIA/nvcf-go/pkg/nvkit/logs"
	"github.com/NVIDIA/nvcf-go/pkg/nvkit/tracing"

	"nvcf-grpc-proxy/proxy"
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
	return zapLogger
}

func NewRootCommand(logger *logs.ZapLogger) *cobra.Command {
	var cfgFile string
	var p *proxy.NVCFProxy

	rootCmd := &cobra.Command{
		Use:          "proxy",
		Short:        "NVCF grpc proxy service",
		SilenceUsage: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			v, err := config.InitConfig(cmd, cfgFile, "", "")
			if err != nil {
				return err
			}
			cmd.Flags().VisitAll(func(flag *pflag.Flag) {
				v.MustBindEnv(flag.Name)
			})
			c := proxy.Config{}
			err = v.Unmarshal(&c)
			if err != nil {
				return err
			}
			p, err = proxy.NewNVCFProxy(logger, c)
			if err != nil {
				return err
			}
			return p.Setup()
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			err := p.Run()
			if err != nil {
				_ = p.Close()
				return err
			}
			return p.Close()
		},
	}
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/"+config.DefaultConfigPath+"/config.yaml)")

	configType := reflect.TypeOf(proxy.Config{})
	for i := 0; i < configType.NumField(); i++ {
		field := configType.Field(i)
		envName := field.Tag.Get("mapstructure")
		rootCmd.Flags().String(envName, "", "")
	}

	return rootCmd
}
