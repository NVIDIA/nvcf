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

package dependency

import (
	"fmt"
	"net/http"

	"github.com/hashicorp/consul-template/nv"
	"github.com/hashicorp/vault/api"
	"github.com/pkg/errors"
)

type NvNoSecretAtPathError struct {
	path                     string
	errorOnMissingKeyEnabled bool
	err                      error
}

func NewNvNoSecretAtPathError(path string, errorOnMissingKeyEnabled bool) *NvNoSecretAtPathError {
	return &NvNoSecretAtPathError{
		path:                     path,
		errorOnMissingKeyEnabled: errorOnMissingKeyEnabled,
	}
}

func NewNvNoSecretAtPathErrorWithError(path string, errorOnMissingKeyEnabled bool, err error) *NvNoSecretAtPathError {
	return &NvNoSecretAtPathError{
		path:                     path,
		errorOnMissingKeyEnabled: errorOnMissingKeyEnabled,
		err:                      err,
	}
}

func (e *NvNoSecretAtPathError) Error() string {
	return fmt.Sprintf("no secret exists at %s", e.path)
}

func (e *NvNoSecretAtPathError) Unwrap() error {
	return e.err
}

func (e *NvNoSecretAtPathError) ErrorOnMissingKeyEnabled() bool {
	return e.errorOnMissingKeyEnabled
}

type NvClientError struct {
	path       string
	statusCode int
	err        error
}

func NewNvClientError(path string, statusCode int) *NvClientError {
	return &NvClientError{
		path:       path,
		statusCode: statusCode,
	}
}

func NewNvClientErrorWithError(path string, err error) *NvClientError {
	statusCode := -1
	var respErr *api.ResponseError
	var noSecretErr *NvNoSecretAtPathError
	if errors.As(err, &respErr) {
		statusCode = respErr.StatusCode
	} else if errors.As(err, &noSecretErr) {
		statusCode = http.StatusNotFound
	}
	return &NvClientError{
		path:       path,
		statusCode: statusCode,
		err:        err,
	}
}

func (e *NvClientError) Error() string {
	return fmt.Sprintf("received status code %d on path %s", e.statusCode, e.path)
}

func (e *NvClientError) Unwrap() error {
	return e.err
}

// NvStopProcessingError represents an error that should stop template processing but keep the agent running
type NvStopProcessingError struct {
	path       string
	statusCode int
	err        error
	templateID string
}

func NewNvStopProcessingErrorWithError(path string, err error) *NvStopProcessingError {
	statusCode := -1
	var respErr *api.ResponseError
	var noSecretErr *NvNoSecretAtPathError
	if errors.As(err, &respErr) {
		statusCode = respErr.StatusCode
	} else if errors.As(err, &noSecretErr) {
		statusCode = http.StatusNotFound
	}
	return &NvStopProcessingError{
		path:       path,
		statusCode: statusCode,
		err:        err,
		// will be set by shimNvClientError
		templateID: "",
	}
}

func (e *NvStopProcessingError) Error() string {
	return fmt.Sprintf("stop processing error: %s (status: %d)", e.path, e.statusCode)
}

func (e *NvStopProcessingError) Unwrap() error {
	return e.err
}

// TemplateID returns the template ID associated with this error
func (e *NvStopProcessingError) TemplateID() string {
	return e.templateID
}

// isNvClientError returns true if exitOnClientError is enabled and passes validations
func isNvClientError(nvq NvVaultQuery, err error) bool {
	// not a client error if flag isn't enabled
	if !nvq.exitOnClientError {
		return false
	}

	// added on nv
	// is client error if status code falls in 40[x] range, and first template render has not completed
	var respErr *api.ResponseError
	if errors.As(err, &respErr) && respErr.StatusCode >= 400 && respErr.StatusCode < 410 {
		// added for nv
		// template destination file check is not needed for ESS agent init container as we never expect the init
		// container to run when templates are already rendered
		if nv.IsInInitMode() || !fileExists(nvq.destination) {
			return true
		}
	}
	// is client error if NoSecretAtPathError is encountered
	var noSecretErr *NvNoSecretAtPathError
	if errors.As(err, &noSecretErr) {
		// added for nv
		if nv.IsInInitMode() || !fileExists(nvq.destination) {
			return true
		}
	}

	return false
}

// isNvStopProcessingError returns true if stopProcessingOnClientError is enabled and passes validations
func isNvStopProcessingError(nvq NvVaultQuery, err error) bool {
	// not a stop processing error if flag isn't enabled
	if !nvq.stopProcessingOnClientError {
		return false
	}

	// stop processing only if destination file doesn't exist
	if fileExists(nvq.destination) {
		return false
	}

	// stop processing if we get a 40x error
	var respErr *api.ResponseError
	if errors.As(err, &respErr) && respErr.StatusCode >= 400 && respErr.StatusCode < 410 {
		return true
	}

	// stop processing if NoSecretAtPathError is encountered (indicates 404)
	var noSecretErr *NvNoSecretAtPathError
	if errors.As(err, &noSecretErr) {
		return true
	}

	return false
}

// shimNvClientError wraps the given error in a NvClientError based on validations performed on it and then returns it,
// else it returns back the given error
func shimNvClientError(nvq NvVaultQuery, path string, err error) error {
	if isNvStopProcessingError(nvq, err) {
		stopErr := NewNvStopProcessingErrorWithError(path, err)
		stopErr.templateID = nvq.templateID
		return stopErr
	}
	if isNvClientError(nvq, err) {
		return NewNvClientErrorWithError(path, err)
	}
	return err
}
