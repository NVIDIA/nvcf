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

package cassandra

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/common/core/types"
)

func TestBuildEventsInsertUsesConditionalWrite(t *testing.T) {
	query, _, err := buildEventsInsert(types.StageTransitionEvent{})
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(query, "IF NOT EXISTS"))
}

func TestBuildDeploymentEventsInsertUsesConditionalWrite(t *testing.T) {
	query, _, err := buildDeploymentEventsInsert(types.DeploymentStageTransitionEvent{})
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(query, "IF NOT EXISTS"))
}
