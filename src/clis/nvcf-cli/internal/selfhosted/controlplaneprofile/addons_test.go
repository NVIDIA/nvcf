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

package controlplaneprofile

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const addonsLLMLine = "requestRouterAddress: llm-request-router.nvcf-cp.internal:50071"

func TestAddonsLLMRequestRouterAddressParsed(t *testing.T) {
	res, err := ParseAndValidate([]byte(validControlPlaneProfileYAML()), ValidateOptions{Require: RequireBoth})
	require.NoError(t, err)
	require.Equal(t, "llm-request-router.nvcf-cp.internal:50071", res.Profile.ControlPlane.Addons.LLM.RequestRouterAddress)
}

func TestAddonsLLMRequestRouterAddressRejectsNonHostPort(t *testing.T) {
	doc := strings.Replace(validControlPlaneProfileYAML(), addonsLLMLine, "requestRouterAddress: not-a-host-port", 1)
	_, err := ParseAndValidate([]byte(doc), ValidateOptions{Require: RequireBoth})
	require.Error(t, err)
	require.Contains(t, err.Error(), "controlPlane.addons.llm.requestRouterAddress")
}

func TestAddonsAbsentIsValid(t *testing.T) {
	doc := strings.Replace(validControlPlaneProfileYAML(), `
  addons:
    llm:
      `+addonsLLMLine, "", 1)
	res, err := ParseAndValidate([]byte(doc), ValidateOptions{Require: RequireBoth})
	require.NoError(t, err)
	require.Empty(t, res.Profile.ControlPlane.Addons.LLM.RequestRouterAddress)
}
