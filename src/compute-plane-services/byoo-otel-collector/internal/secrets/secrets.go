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

package secrets

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/google/go-cmp/cmp"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/byoo-otel-collector/internal/logger"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/byoo-otel-collector/internal/metrics"
)

const (
	SecretsFileCheckInterval = 30 * time.Second
	secretsFileCheckTimeout  = 180 * time.Second
)

// validateAndReadSecretFile checks if the secret file exists and is a valid JSON file.
func validateAndReadSecretFile(secretsFile string, maxElapsedTime time.Duration) ([]byte, error) {

	operation := func() error {
		fileContent, err := os.ReadFile(secretsFile)
		if err != nil {
			if os.IsNotExist(err) {
				logger.Logger.Info("Secret file not found, retrying...")
				return err
			}
			return backoff.Permanent(fmt.Errorf("failed to read secret file: %w", err))
		}

		// Validate if the file content is valid JSON
		var jsonValidation interface{}
		if err := json.Unmarshal(fileContent, &jsonValidation); err != nil {
			return backoff.Permanent(fmt.Errorf("file is not a valid JSON: %w", err))
		}

		logger.Logger.Debugf("Secret file %s is valid JSON", secretsFile)
		return nil
	}

	expBackOff := backoff.NewExponentialBackOff()
	expBackOff.MaxElapsedTime = maxElapsedTime

	err := backoff.Retry(operation, expBackOff)
	if err != nil {
		return nil, fmt.Errorf("timeout waiting for accounts secrets file to be created: %w", err)
	}

	fileContent, _ := os.ReadFile(secretsFile)
	return fileContent, nil
}

// RunSecretsExtractor reads a JSON file containing secrets and creates individual files for each secret.
// - For string values, it creates a single file with the key name as the filename.
// - For dictionary (map) values, it flattens the dictionary and creates multiple files with filenames formatted as "<key>-<subkey>".
func RunSecretsExtractor(secretsFile, outputDir string) error {
	start := time.Now()
	status := metrics.StatusSuccess

	defer func() {
		duration := time.Since(start)
		metrics.RecordOperationDuration(metrics.RunSecretsExtractor, status, duration)
		metrics.IncrementOperationStatus(metrics.RunSecretsExtractor, status)
	}()

	// Read the JSON file containing secrets
	fileContent, err := validateAndReadSecretFile(secretsFile, secretsFileCheckTimeout)
	if err != nil {
		status = metrics.StatusError
		return fmt.Errorf("failed to read input file: %w", err)
	}

	var secrets map[string]interface{}
	err = json.Unmarshal(fileContent, &secrets)
	if err != nil {
		status = metrics.StatusError
		return fmt.Errorf("failed to parse JSON: %w", err)
	}

	// Ensure the output directory exists
	_, err = os.Stat(outputDir)
	if err != nil {
		status = metrics.StatusError
		return fmt.Errorf("output directory does not exist: %w", err)
	}

	for secretName, secretValue := range secrets {
		switch value := secretValue.(type) {
		case string:
			// Write string values directly to a file named after the key
			secretFilePath := filepath.Join(outputDir, secretName)
			logger.Logger.Infof("Writing secret to file: %s", secretFilePath)
			err = os.WriteFile(secretFilePath, []byte(value), 0644)
			if err != nil {
				status = metrics.StatusError
				return fmt.Errorf("failed to write file for secret %s: %w", secretName, err)
			}
		case map[string]interface{}:
			// Flatten dictionary values and create files for each subkey
			for subKey, subValue := range value {

				secretName := fmt.Sprintf("%s-%s", secretName, subKey)
				subValueStr, ok := subValue.(string)
				if !ok {
					status = metrics.StatusError
					return fmt.Errorf("unsupported value type for key %s-%s", secretName, subKey)
				}

				secretFilePath := filepath.Join(outputDir, secretName)
				logger.Logger.Infof("Writing secret to file: %s", secretFilePath)
				err = os.WriteFile(secretFilePath, []byte(subValueStr), 0644)
				if err != nil {
					status = metrics.StatusError
					return fmt.Errorf("failed to write file for secret %s: %w", secretName, err)
				}
			}
		default:
			status = metrics.StatusError
			return fmt.Errorf("unsupported value type for key %s", secretName)
		}
	}

	logger.Logger.Infof("Secrets extracted to %s", outputDir)
	return nil
}

func CheckSecretsChanges(secretFilePath string, lastContent []byte, lastModTime time.Time) (bool, []byte, time.Time, error) {
	logger.Logger.Debugf("Checking for changes in secrets file: %s", secretFilePath)
	fileInfo, err := os.Stat(secretFilePath)
	if err != nil {
		return false, lastContent, lastModTime, fmt.Errorf("failed to stat secrets file: %w", err)
	}

	if fileInfo.ModTime().Before(lastModTime) {
		logger.Logger.Info("No changes detected in secrets file.")
		return false, lastContent, fileInfo.ModTime(), nil
	}

	content, err := os.ReadFile(secretFilePath)
	if err != nil {
		return false, lastContent, lastModTime, fmt.Errorf("failed to read secrets file: %w", err)
	}

	var newSecrets map[string]interface{}
	if err := json.Unmarshal(content, &newSecrets); err != nil {
		return false, lastContent, lastModTime, fmt.Errorf("failed to unmarshal new secrets file: %w", err)
	}

	var oldSecrets map[string]interface{}
	if len(lastContent) > 0 {
		if err := json.Unmarshal(lastContent, &oldSecrets); err != nil {

			return false, lastContent, lastModTime, fmt.Errorf("failed to unmarshal old secrets file: %w", err)
		}
	}

	if !cmp.Equal(newSecrets, oldSecrets) {
		logger.Logger.Info("Secrets file content has changed.")
		return true, content, fileInfo.ModTime(), nil
	}

	logger.Logger.Debug("No changes in secrets file content.")
	return false, lastContent, lastModTime, nil
}
