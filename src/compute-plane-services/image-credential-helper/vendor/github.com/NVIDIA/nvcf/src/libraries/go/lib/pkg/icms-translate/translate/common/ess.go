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

package common

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

const (
	// Envs
	EssAgentInitEnv          = "ESS_AGENT_INIT"
	AccountsSecretsPathEnv   = "ACCOUNTS_SECRET_PATH"
	AccountsSecretsDestEnv   = "ACCOUNTS_SECRET_DEST"
	SecretsAssertionTokenEnv = "SECRETS_ASSERTION_TOKEN"

	EssSecretDataVolumeName = "secret-data"
	EssSecretsDir           = "/var/secrets"
	EssDataVolumeName       = "ess-data"
	EssConfigDir            = "/config/ess-agent"
	EssSecretsDest          = EssSecretsDir + "/secrets.json"
	EssAccountSecretsDest   = EssSecretsDir + "/accounts-secrets.json"
)

var (
	EssContainerArgs = []string{fmt.Sprintf("-config=%s", path.Join(EssConfigDir, "config.hcl"))}
)

func NewESSInitContainer(image string, envs []corev1.EnvVar) corev1.Container {
	container := corev1.Container{
		Name:            "ess-init",
		Image:           image,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Args:            EssContainerArgs,
		Env:             SortEnvs(envs),
		SecurityContext: NewInfraContainerSecurityContext(),
		Resources:       GetContainerResourcesESS(),
	}
	return container
}

func NewESSContainer(image string, envs []corev1.EnvVar) corev1.Container {
	container := corev1.Container{
		Name:            "ess",
		Image:           image,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Args:            EssContainerArgs,
		Env:             SortEnvs(envs),
		SecurityContext: NewInfraContainerSecurityContext(),
		Resources:       GetContainerResourcesESS(),
	}
	return container
}

var (
	errExpectedAccountSecrets = errors.New("expected account secrets but no secret paths given")
	errJWTParse               = errors.New("error parsing NVCF assertion token")
	errInvalidToken           = errors.New("invalid NVCF assertion token claims structure")
	errNoSecretPaths          = errors.New("no secret paths found")
)

// NvcfAssertionTokenClaims are the expected claims to parse from the secret
// assertion token, in order to find the list of secret paths.
type NvcfAssertionTokenClaims struct {
	Assertion struct {
		Namespace   string   `json:"namespace"`
		SecretPaths []string `json:"secretPaths"`
	} `json:"assertion"`
	jwt.RegisteredClaims
}

// NewESSSecretEnvs provides the env vars needed for function/task specific
// secrets.
func NewESSSecretEnvs(path string) []corev1.EnvVar {
	return []corev1.EnvVar{
		{
			Name:  "SECRET_PATH",
			Value: path,
		},
		{
			Name:  "SECRET_DEST",
			Value: EssSecretsDest,
		},
	}
}

// NewESSAccountSecretEnvs builds the env vars needed for account level secrets
// given the secrets path found in the assertion token. The secrets path list
// contains two types of paths: newAccountSecretPath(s) and
// functionSecretPath/taskSecretPath (called skipPath in function). Main
// scenarios to account for:
//  1. if newAccountSecretPath(s) + functionSecretPath in secretPaths, then
//     ACCOUNTS_SECRET_PATH=newAccountSecretPath(s).
//  2. if functionSecretPath in secretPaths, then do not inject ACCOUNTS_SECRET_PATH.
//     In this case, we return an error because this function should only be
//     called when we have account secrets.
func NewESSAccountsSecretEnvs(ncaID, assertionToken, skipPath string) ([]corev1.EnvVar, error) {
	accountsSecretPathArray, err := getSecretPathsFromAssertionToken(assertionToken)
	if err != nil {
		return nil, err
	}

	var (
		builder strings.Builder
	)
	for _, secretPath := range accountsSecretPathArray {
		if len(secretPath) == 0 || secretPath == skipPath {
			continue
		}

		if builder.Len() > 0 {
			builder.WriteString(",")
		}
		builder.WriteString(secretPath)
	}
	accountsSecretPath := builder.String()
	if len(accountsSecretPath) == 0 {
		// if there are no account secret paths, return an error.
		return nil, errExpectedAccountSecrets
	}

	return []corev1.EnvVar{
		{
			Name:  AccountsSecretsPathEnv,
			Value: accountsSecretPath,
		},
		{
			Name:  AccountsSecretsDestEnv,
			Value: EssAccountSecretsDest,
		},
	}, nil
}

func GetContainerResourcesESS() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    *resource.NewMilliQuantity(125, resource.DecimalSI), // 125m
			corev1.ResourceMemory: *resource.NewQuantity(64*1<<20, resource.BinarySI),  // 64Mi
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    *resource.NewMilliQuantity(250, resource.DecimalSI), // 250m
			corev1.ResourceMemory: *resource.NewQuantity(128*1<<20, resource.BinarySI), // 128Mi
		},
	}
}

func getSecretPathsFromAssertionToken(assertionToken string) ([]string, error) {
	// Parse the token without verifying its signature
	token, _, err := jwt.NewParser(jwt.WithoutClaimsValidation()).ParseUnverified(assertionToken, &NvcfAssertionTokenClaims{})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errJWTParse, err)
	}

	if claims, ok := token.Claims.(*NvcfAssertionTokenClaims); ok {
		if len(claims.Assertion.SecretPaths) == 0 {
			return nil, errNoSecretPaths
		}
		return claims.Assertion.SecretPaths, nil
	}
	return nil, errInvalidToken
}

// TaskSecretPath returns the task secret path given the task ID.
func TaskSecretPath(id string) string {
	return fmt.Sprintf("tasks/%s/secrets", id)
}

// FuncSecretPath returns the task secret path given the function ID and
// function version ID.
func FuncSecretPath(funcID, funcVersionID string) string {
	return fmt.Sprintf("functions/%s/versions/%s", funcID, funcVersionID)
}
