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
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"reflect"
	"syscall"

	"github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/logging"
	olricConfig "github.com/olric-data/olric/config"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf-go/pkg/nvkit/config"
	"github.com/NVIDIA/nvcf-go/pkg/nvkit/logs"
	"github.com/NVIDIA/nvcf-go/pkg/nvkit/tracing"

	"ratelimiter"
)

func main() {
	setupLogger()
	setupMetrics()
	setupPprof()
	rootCmd := NewRootCommand()
	err := rootCmd.Execute()
	if err != nil {
		return
	}
}

func setupLogger() *logs.ZapLogger {
	zapLogger := logs.NewZapLogger(zap.NewAtomicLevelAt(zap.InfoLevel))
	zap.ReplaceGlobals(zapLogger.GetZapLogger())
	zap.RedirectStdLog(zapLogger.GetZapLogger())
	return zapLogger
}

func setupMetrics() {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	go func() {
		err := http.ListenAndServe("0.0.0.0:7776", mux)
		if err != nil {

		}
	}()
}

func setupPprof() {
	pprofMux := http.NewServeMux()
	pprofMux.HandleFunc("/debug/pprof/", http.DefaultServeMux.ServeHTTP)
	go func() {
		zap.L().Info("Starting pprof server on :6060")
		err := http.ListenAndServe("0.0.0.0:6060", pprofMux)
		if err != nil {
			zap.L().Error("pprof server failed", zap.Error(err))
		}
	}()
}

func setupOlricStats(rateLimiter *ratelimiter.RateLimiter) {
	// Get the DMap from the store for on-demand stats
	store, ok := rateLimiter.GetStore().(*ratelimiter.Store)
	if !ok {
		zap.L().Error("Failed to cast store to *ratelimiter.Store for Olric stats")
		return
	}

	olricMux := http.NewServeMux()
	olricMux.HandleFunc("/debug/olric/keys", ratelimiter.OlricStatsHandler(store.GetDMap(), "limiter"))

	go func() {
		zap.L().Info("Starting Olric stats server on :6061")
		err := http.ListenAndServe("0.0.0.0:6061", olricMux)
		if err != nil {
			zap.L().Error("Olric stats server failed", zap.Error(err))
		}
	}()
}

func InterceptorLogger(l *zap.Logger) logging.Logger {
	return logging.LoggerFunc(func(ctx context.Context, lvl logging.Level, msg string, fields ...any) {
		f := make([]zap.Field, 0, len(fields)/2)

		for i := 0; i < len(fields); i += 2 {
			key := fields[i]
			value := fields[i+1]

			switch v := value.(type) {
			case string:
				f = append(f, zap.String(key.(string), v))
			case int:
				f = append(f, zap.Int(key.(string), v))
			case bool:
				f = append(f, zap.Bool(key.(string), v))
			default:
				f = append(f, zap.Any(key.(string), v))
			}
		}

		logger := l.WithOptions(zap.AddCallerSkip(1)).With(f...)

		switch lvl {
		case logging.LevelDebug:
			logger.Debug(msg)
		case logging.LevelInfo:
			logger.Info(msg)
		case logging.LevelWarn:
			logger.Warn(msg)
		case logging.LevelError:
			logger.Error(msg)
		}
	})
}

func NewRootCommand() *cobra.Command {
	var cfgFile string
	var rateLimiterConfig *ratelimiter.Config
	rootCmd := &cobra.Command{
		Use:          "server",
		Short:        "NVCF Rate Limiter server command",
		Long:         `NVCF Rate Limiter server command.`,
		SilenceUsage: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			v, err := config.InitConfig(cmd, cfgFile, "", "")
			if err != nil {
				return err
			}
			cmd.Flags().VisitAll(func(flag *pflag.Flag) {
				v.MustBindEnv(flag.Name)
			})
			c := ratelimiter.Config{}
			err = v.Unmarshal(&c)
			if err != nil {
				return err
			}
			if c.SecretsPath == "" {
				c.SecretsPath = "vault/secrets.json"
			}
			if c.OAuth2Issuer == "" {
				return fmt.Errorf("missing required OAUTH2_ISSUER (OAuth2 issuer URL for inbound JWT validation)")
			}
			if c.Audience == "" {
				return fmt.Errorf("missing required audience")
			}
			if c.TracingAccessToken == "" {
				secrets, _ := os.ReadFile(c.SecretsPath)
				var secretsMap map[string]any
				_ = json.Unmarshal(secrets, &secretsMap)
				if token, ok := secretsMap["tracingAccessToken"].(string); ok {
					c.TracingAccessToken = token
				}
			}
			if c.NvcfApiUrl == "" {
				return fmt.Errorf("no nvcf api url is set")
			}
			rateLimiterConfig = &c
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			defer tracing.Shutdown()

			grpcListener, err := net.Listen("tcp", "0.0.0.0:7777")
			if err != nil {
				return err
			}
			defer grpcListener.Close()

			k8sDisc, err := ratelimiter.NewK8sDiscovery(context.Background())
			if err != nil {
				return err
			}
			cfg := olricConfig.New("lan")
			cfg.ServiceDiscovery = map[string]interface{}{
				"plugin": k8sDisc,
			}
			cfg.DMaps.EvictionPolicy = olricConfig.LRUEviction
			cfg.DMaps.MaxInuse = 100_000_000 // 100 MB
			rateLimiter, err := ratelimiter.NewRateLimiter(*rateLimiterConfig, cfg)
			if err != nil {
				return err
			}
			defer rateLimiter.Close()

			// Setup Olric stats endpoint for debugging
			setupOlricStats(rateLimiter)
			// make a http health endpoint since astro doesn't support gRPC health endpoint
			healthServer := http.NewServeMux()
			healthServer.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
				err := rateLimiter.Health()
				if err != nil {
					zap.L().Error("rate limiter error", zap.Error(err))
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				w.WriteHeader(http.StatusOK)
			})
			healthErrChan := make(chan error, 1)
			go func() {
				if err := http.ListenAndServe(":8080", healthServer); err != nil {
					zap.L().Error("error starting health server", zap.Error(err))
					healthErrChan <- err
				}
			}()

			baseServer, err := ratelimiter.MakeGrpcServer(rateLimiter, grpcListener, InterceptorLogger(zap.L()))
			if err != nil {
				return err
			}
			defer baseServer.GracefulStop()
			grpcErrChan := make(chan error, 1)
			go func() {
				if err := baseServer.Serve(grpcListener); err != nil {
					zap.L().Error("failed to serve", zap.Error(err))
					grpcErrChan <- err
				}
			}()

			sigChan := make(chan os.Signal, 1)
			signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

			select {
			case sig := <-sigChan:
				zap.L().Info("Received signal, shutting down...", zap.String("signal", sig.String()))
				return nil
			case err := <-grpcErrChan:
				zap.L().Error("grpc server error, shutting down...", zap.Error(err))
				return err
			case err := <-healthErrChan:
				zap.L().Error("health server error, shutting down...", zap.Error(err))
				return err
			}
		},
	}

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/"+config.DefaultConfigPath+"/config.yaml)")
	configType := reflect.TypeOf(ratelimiter.Config{})
	for i := 0; i < configType.NumField(); i++ {
		field := configType.Field(i)
		envName := field.Tag.Get("mapstructure")
		rootCmd.Flags().String(envName, "", "")
	}

	return rootCmd
}
