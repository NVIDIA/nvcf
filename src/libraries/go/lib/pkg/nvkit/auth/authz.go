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

package auth

import (
	"github.com/spf13/cobra"
)

// UAMPolicyConfig captures the config required for authz evaluation
type UAMPolicyConfig struct {
	Namespace  string `mapstructure:"namespace"`
	PolicyFQDN string `mapstructure:"name"`
}

// AddCommandFlags is a helper method to add config flags to cobra command
func (cfg *UAMPolicyConfig) AddCommandFlags(cmd *cobra.Command) bool {
	if cmd == nil {
		return false
	}
	cmd.Flags().StringVarP(&cfg.Namespace, "authz.policy.namespace", "", "", "policy namespace")
	cmd.Flags().StringVarP(&cfg.PolicyFQDN, "authz.policy.name", "", "", "policy fqdn")
	return true
}
