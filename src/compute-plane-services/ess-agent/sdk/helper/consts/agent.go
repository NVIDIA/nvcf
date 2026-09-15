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

package consts

// AgentPathCacheClear is the path that the agent will use as its cache-clear
// endpoint.
const AgentPathCacheClear = "/agent/v1/cache-clear"

// AgentPathMetrics is the path the agent will use to expose its internal
// metrics.
const AgentPathMetrics = "/agent/v1/metrics"

// AgentPathQuit is the path that the agent will use to trigger stopping it.
const AgentPathQuit = "/agent/v1/quit"
