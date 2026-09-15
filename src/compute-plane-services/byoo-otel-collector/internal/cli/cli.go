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

package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/byoo-otel-collector/internal/logger"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/byoo-otel-collector/internal/metrics"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/byoo-otel-collector/internal/otelcollector"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/byoo-otel-collector/internal/otelconfig"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/byoo-otel-collector/internal/secrets"
)

// processWaiter is the subset of *os.Process used by runSecretsCheckLoop, extracted so tests
// can exercise the wait-error logging without spawning a real OS process.
type processWaiter interface {
	Wait() (*os.ProcessState, error)
}

// waitAndLogProcessExit waits for otelcol-contrib to exit and logs a non-nil error instead of
// discarding it.
func waitAndLogProcessExit(proc processWaiter) {
	if _, waitErr := proc.Wait(); waitErr != nil {
		logger.Logger.Errorf("error waiting for otelcol-contrib to exit: %v", waitErr)
	}
}

func runSecretsCheckLoop(ctx context.Context, otelCollectorProc *os.Process, args []string, accountsSecrets, secretsFolder string, lastContent []byte, lastModTime time.Time) error {
	restartCh := make(chan struct{}, 1)

	// Start secret change monitor goroutine
	go func() {
		ticker := time.NewTicker(secrets.SecretsFileCheckInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				changed, newContent, newModTime, err := secrets.CheckSecretsChanges(accountsSecrets, lastContent, lastModTime)
				if err != nil {
					logger.Logger.Errorf("error checking accounts secrets file changes: %v", err)
					metrics.IncrementOperationStatus(metrics.RunSecretsCheckLoop, metrics.StatusError)
					continue
				}
				if changed {
					restartCh <- struct{}{}
					lastContent = newContent
					lastModTime = newModTime
				}
				metrics.IncrementOperationStatus(metrics.RunSecretsCheckLoop, metrics.StatusSuccess)
			}
		}
	}()

	var err error
	for {
		select {
		case <-ctx.Done():
			logger.Logger.Info("Received interrupt signal, terminating otelcol-contrib and exiting.")
			otelcollector.GracefulShutdown(otelCollectorProc)
			// Wait for the process to actually exit
			waitAndLogProcessExit(otelCollectorProc)
			return nil
		case <-restartCh:
			logger.Logger.Info("Regenerating the secret files and restarting otelcol-contrib due to the secret file changes.")

			// increment service restart
			metrics.IncrementServiceRestart()

			// Regenerate the secret files
			if err = secrets.RunSecretsExtractor(accountsSecrets, secretsFolder); err != nil {
				logger.Logger.Errorf("error running secrets-extractor: %v", err)
			}

			// shutdown the current otelcol-contrib process
			otelcollector.GracefulShutdown(otelCollectorProc)
			// Wait for it to exit
			waitAndLogProcessExit(otelCollectorProc)

			// run the otelcol-contrib process again
			if otelCollectorProc, err = otelcollector.RunOtelCollector(args); err != nil {
				return fmt.Errorf("error running otelcol-contrib: %w", err)
			}
		case state := <-waitProcessExit(otelCollectorProc):
			if state != nil && state.ExitCode() != 0 {
				metrics.IncrementOperationStatus(metrics.RunOtelCollector, metrics.StatusError)
				logger.Logger.Errorf("otelcol-contrib exited with error: %v", state)
			}
			// Process exited, return to stop the loop
			return fmt.Errorf("otelcol-contrib process exited unexpectedly")
		}
	}
}

func waitProcessExit(proc *os.Process) <-chan *os.ProcessState {
	ch := make(chan *os.ProcessState, 1)
	go func() {
		state, _ := proc.Wait()
		ch <- state
	}()
	return ch
}

func NewCommand() *cobra.Command {
	var accountsSecrets string
	var secretsFolder string
	var otelConfigPath string
	var telemetries string
	var enableMetricService bool

	cmd := &cobra.Command{
		Use:   "byoo-otel-collector",
		Short: "CLI tool for running byoo-otelconfig-generator, byoo-secrets-extractor and otel-collector-contrib",
		RunE: func(cmd *cobra.Command, args []string) error {
			logger.Logger.Infof("Running byoo-otel-collector with flags and args:")
			cmd.Flags().VisitAll(func(f *pflag.Flag) {
				logger.Logger.Infof("Flag: --%s=%v", f.Name, f.Value)
			})
			logger.Logger.Infof("Args: %v", args)

			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			var wg sync.WaitGroup

			// if the metrics service is enabled, start it
			if enableMetricService {
				metricsService := metrics.NewMetricsService(metrics.MetricsPort)
				wg.Add(1)
				go func() {
					defer wg.Done()
					if err := metricsService.Start(ctx); err != nil {
						logger.Logger.Errorf("Metrics service error: %v", err)
					}
				}()
				logger.Logger.Infof("Metrics service enabled on port %d", metrics.MetricsPort)
			}

			// set service to running state
			metrics.SetServiceUp(true)

			// generate the secrets by extracting them from the accounts-secrets.json file
			if err := secrets.RunSecretsExtractor(accountsSecrets, secretsFolder); err != nil {
				return fmt.Errorf("failed to run byoo-secrets-extractor: %w", err)
			}

			// generate the otel-collector config
			if telemetries != "" {
				if err := otelconfig.GenerateConfig(otelConfigPath, telemetries); err != nil {
					return fmt.Errorf("failed to run byoo-otelconfig-generator: %w", err)
				}
			}

			// run the otel-collector
			// check if the config flag is already present in the args
			configFlag := "--config"
			configFlagExists := slices.Contains(args, configFlag)
			if !configFlagExists {
				args = append(args, configFlag, otelConfigPath)
			}
			otelCollectorProc, err := otelcollector.RunOtelCollector(args)
			if err != nil {
				return err
			}

			// Start the secret file check loop
			lastContent, _ := os.ReadFile(accountsSecrets)
			secretFileInfo, _ := os.Stat(accountsSecrets)
			lastModTime := secretFileInfo.ModTime()
			loopErr := runSecretsCheckLoop(ctx, otelCollectorProc, args, accountsSecrets, secretsFolder, lastContent, lastModTime)

			// set service to stopped state
			metrics.SetServiceUp(false)

			// wait for all goroutines to finish
			wg.Wait()

			// Graceful shutdown (loopErr == nil) is success, not an error
			if loopErr != nil {
				logger.Logger.Errorf("Secrets check loop finished with error: %v", loopErr)
				return fmt.Errorf("error while running byoo-otel-collector: %w", loopErr)
			}

			logger.Logger.Info("byoo-otel-collector shutdown gracefully")
			return nil
		},
	}

	cmd.Flags().StringVar(&accountsSecrets, "byoo-accounts-secrets", "/var/secrets/accounts-secrets.json", "Path to the JSON file containing secrets")
	cmd.Flags().StringVar(&secretsFolder, "byoo-secrets-folder", "/etc/byoo-otel-collector/secrets/", "Path to the directory where secret files will be saved")
	cmd.Flags().StringVar(&otelConfigPath, "otel-config-path", "/etc/byoo-otel-collector/config.yaml", "Path to the BYOO Otel Collector configuration file")
	cmd.Flags().StringVar(&telemetries, "telemetries", "", "Telemetries configured for function/task, encoded in base64")
	cmd.Flags().BoolVar(&enableMetricService, "enable-metric-service", true, "Enabled by default. Set to false to disable the Prometheus metrics service")

	// TODO: NVCF-4850, NVCF-4849 Uncomment the following line when the migration is complete
	// cmd.MarkFlagRequired("telemetries")

	return cmd
}
