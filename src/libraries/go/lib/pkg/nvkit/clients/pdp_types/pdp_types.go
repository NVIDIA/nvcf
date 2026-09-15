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

package pdp_types

import (
	"encoding/json"

	"google.golang.org/protobuf/types/known/structpb"
)

type RuleRequest struct {
	// Namespace
	Namespace string `protobuf:"bytes,1,opt,name=namespace,proto3" json:"namespace,omitempty"`
	// Rule name/Policy FQDN - package_name.policy_name (Len: 256+1(.)+256)
	RuleName string `protobuf:"bytes,2,opt,name=rule_name,json=ruleName,proto3" json:"rule_name,omitempty"`
	// Input for evaluation
	Input map[string]*structpb.Value `protobuf:"bytes,3,rep,name=input,proto3" json:"input,omitempty" protobuf_key:"bytes,1,opt,name=key,proto3" protobuf_val:"bytes,2,opt,name=value,proto3"` //nolint:revive
}

func (rr *RuleRequest) String() string {
	jsonBytes, err := json.Marshal(rr)
	if err != nil {
		return ""
	}

	return string(jsonBytes)
}

type RuleResponse struct {
	Namespace string `protobuf:"bytes,1,opt,name=namespace,proto3" json:"namespace,omitempty"`
	// package_name.policy_name
	RuleName string          `protobuf:"bytes,2,opt,name=rule_name,json=ruleName,proto3" json:"rule_name,omitempty"`
	Result   *structpb.Value `protobuf:"bytes,3,opt,name=result,proto3" json:"result,omitempty"`
}

func (rr *RuleResponse) String() string {
	jsonBytes, err := json.Marshal(rr)
	if err != nil {
		return ""
	}

	return string(jsonBytes)
}
