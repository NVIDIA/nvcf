/*
 * SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */
package com.nvidia.icms.outbound.fnds;

import com.nvidia.icms.outbound.fnds.model.FndsMessageModel;
import com.nvidia.icms.outbound.fnds.model.FndsMessageV2Model;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.service.annotation.PostExchange;

public interface FndsStubService {

    @PostExchange("/v1/ledger/versions/{versionId}/instances/{instanceId}")
    ResponseEntity<Void> postDeploymentStageV1(
            @PathVariable String versionId,
            @PathVariable String instanceId,
            @RequestBody FndsMessageModel body);

    @PostExchange("/v2/ledger/versions/{versionId}/deployments/{deploymentId}/instances/{instanceId}")
    ResponseEntity<Void> postDeploymentStageV2(
            @PathVariable String versionId,
            @PathVariable String deploymentId,
            @PathVariable String instanceId,
            @RequestBody FndsMessageV2Model body);

    @PostExchange(value = "/v3/ledger/cloudevents", contentType = "application/cloudevents-batch+json")
    ResponseEntity<Void> postCloudEvents(@RequestBody byte[] payload);
}
