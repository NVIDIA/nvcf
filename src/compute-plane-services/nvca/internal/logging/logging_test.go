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
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/task"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
)

func TestMakeICMSRequestFields(t *testing.T) {
	assert.Equal(t, []any{
		"name", "",
		"type", "",
		"icms_request_id", "",
	}, MakeICMSRequestFields(&nvcav2beta1.ICMSRequest{}))

	assert.Equal(t, []any{
		"name", "foo",
		"type", string(common.FunctionCreationAction),
		"icms_request_id", "srid",
		"function_id", "funcid",
		"function_version_id", "funcverid",
		"function_type", "DEFAULT",
		"deployment_id", "deploy123",
	}, MakeICMSRequestFields(&nvcav2beta1.ICMSRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name: "foo",
		},
		Spec: nvcav2beta1.ICMSRequestSpec{
			Action:    common.FunctionCreationAction,
			RequestID: "srid",
			FunctionDetails: function.Details{
				FunctionID:        "funcid",
				FunctionVersionID: "funcverid",
				FunctionType:      "DEFAULT",
			},
			CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
				CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
					DeploymentID: "deploy123",
				},
			},
		},
	}))

	assert.Equal(t, []any{
		"name", "foo",
		"type", string(common.FunctionCreationAction),
		"icms_request_id", "srid",
		"function_id", "funcid",
		"function_version_id", "funcverid",
		"function_type", "DEFAULT",
		"deployment_id", "deploy123",
	}, MakeICMSRequestFields(&nvcav2beta1.ICMSRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name: "foo",
		},
		Spec: nvcav2beta1.ICMSRequestSpec{
			Action:    common.FunctionCreationAction,
			RequestID: "srid",
			FunctionDetails: function.Details{
				FunctionID:        "funcid",
				FunctionVersionID: "funcverid",
				FunctionType:      "DEFAULT",
			},
			FunctionID:        "funcid2",
			FunctionVersionID: "funcverid2",
			CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
				CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
					DeploymentID: "deploy123",
				},
			},
		},
	}))

	assert.Equal(t, []any{
		"name", "foo",
		"type", string(common.FunctionCreationAction),
		"icms_request_id", "srid",
		"function_id", "funcid2",
		"function_version_id", "funcverid2",
		"deployment_id", "deploy456",
	}, MakeICMSRequestFields(&nvcav2beta1.ICMSRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name: "foo",
		},
		Spec: nvcav2beta1.ICMSRequestSpec{
			Action:            common.FunctionCreationAction,
			RequestID:         "srid",
			FunctionID:        "funcid2",
			FunctionVersionID: "funcverid2",
			CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
				CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
					DeploymentID: "deploy456",
				},
			},
		},
	}))

	assert.Equal(t, []any{
		"name", "foo",
		"type", string(common.TaskCreationAction),
		"icms_request_id", "srid",
		"task_id", "taskid",
		"task_type", "DEFAULT",
		"deployment_id", "deploy789",
	}, MakeICMSRequestFields(&nvcav2beta1.ICMSRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name: "foo",
		},
		Spec: nvcav2beta1.ICMSRequestSpec{
			Action:    common.TaskCreationAction,
			RequestID: "srid",
			TaskDetails: task.Details{
				TaskID:   "taskid",
				TaskType: "DEFAULT",
			},
			CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
				CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
					DeploymentID: "deploy789",
				},
			},
		},
	}))
}

func TestNewICMSRequestFieldLogger(t *testing.T) {
	assert.Equal(t, logrus.NewEntry(logrus.StandardLogger()).WithFields(logrus.Fields{
		"name":                "foo",
		"type":                string(common.FunctionCreationAction),
		"icms_request_id":     "srid",
		"function_id":         "funcid",
		"function_version_id": "funcverid",
		"function_type":       "DEFAULT",
		"deployment_id":       "deploy123",
	}), NewICMSRequestFieldLogger(&nvcav2beta1.ICMSRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name: "foo",
		},
		Spec: nvcav2beta1.ICMSRequestSpec{
			Action:    common.FunctionCreationAction,
			RequestID: "srid",
			FunctionDetails: function.Details{
				FunctionID:        "funcid",
				FunctionVersionID: "funcverid",
				FunctionType:      "DEFAULT",
			},
			CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
				CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
					DeploymentID: "deploy123",
				},
			},
		},
	}, logrus.NewEntry(logrus.StandardLogger())))
}

func TestWithICMSRequestFieldLogger(t *testing.T) {
	ctx := WithICMSRequestFieldLogger(core.WithLogger(context.Background(), logrus.NewEntry(logrus.StandardLogger())), &nvcav2beta1.ICMSRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name: "foo",
		},
		Spec: nvcav2beta1.ICMSRequestSpec{
			Action:    common.FunctionCreationAction,
			RequestID: "srid",
			FunctionDetails: function.Details{
				FunctionID:        "funcid",
				FunctionVersionID: "funcverid",
				FunctionType:      "DEFAULT",
			},
			CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
				CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
					DeploymentID: "deploy123",
				},
			},
		},
	})

	assert.Equal(t, logrus.NewEntry(logrus.StandardLogger()).WithFields(logrus.Fields{
		"name":                "foo",
		"type":                string(common.FunctionCreationAction),
		"icms_request_id":     "srid",
		"function_id":         "funcid",
		"function_version_id": "funcverid",
		"function_type":       "DEFAULT",
		"deployment_id":       "deploy123",
	}), core.GetLogger(ctx))
}
