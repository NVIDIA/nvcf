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

package logging

import (
	"context"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/sirupsen/logrus"

	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
)

func MakeICMSRequestFields(r *nvcav2beta1.ICMSRequest) []any {
	// Allocate the slice with max number of fields to avoid append,
	// since this method is called often.
	fields := make([]any, 14)
	fields[0], fields[1] = "name", r.Name
	fields[2], fields[3] = "type", string(r.Spec.Action)
	fields[4], fields[5] = "icms_request_id", r.Spec.RequestID
	// Termination ICMS requests do not specify a function/version ID.
	switch r.Spec.Action {
	case common.FunctionCreationAction:
		if r.Spec.FunctionDetails.FunctionID != "" {
			fields[6], fields[7] = "function_id", r.Spec.FunctionDetails.FunctionID
			fields[8], fields[9] = "function_version_id", r.Spec.FunctionDetails.FunctionVersionID
			fields[10], fields[11] = "function_type", r.Spec.FunctionDetails.FunctionType
			fields[12], fields[13] = "deployment_id", r.Spec.CreationMsgInfo.DeploymentID //nolint:goconst
		} else {
			fields[6], fields[7] = "function_id", r.Spec.FunctionID                       //nolint:staticcheck
			fields[8], fields[9] = "function_version_id", r.Spec.FunctionVersionID        //nolint:staticcheck
			fields[10], fields[11] = "deployment_id", r.Spec.CreationMsgInfo.DeploymentID //nolint:goconst
			fields = fields[:12]
		}
	case common.TaskCreationAction:
		fields[6], fields[7] = "task_id", r.Spec.TaskDetails.TaskID
		fields[8], fields[9] = "task_type", r.Spec.TaskDetails.TaskType
		fields[10], fields[11] = "deployment_id", r.Spec.CreationMsgInfo.DeploymentID //nolint:goconst
		fields = fields[:12]
	default:
		fields = fields[:6]
	}
	return fields
}

func NewICMSRequestFieldLogger(r *nvcav2beta1.ICMSRequest, log *logrus.Entry) *logrus.Entry {
	fields := MakeICMSRequestFields(r)
	f := make(logrus.Fields, len(fields)/2)
	for i := 0; i < len(fields); i += 2 {
		f[fields[i].(string)] = fields[i+1]
	}
	return log.WithFields(f)
}

func WithICMSRequestFieldLogger(ctx context.Context, r *nvcav2beta1.ICMSRequest) context.Context {
	return core.WithLogger(ctx, NewICMSRequestFieldLogger(r, core.GetLogger(ctx)))
}
