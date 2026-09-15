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
package com.nvidia.icms.service.extensions.impl;

import com.nvidia.icms.service.extensions.api.CloudHealthEventService;

import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.CloudHealthEntity;
import lombok.extern.slf4j.Slf4j;

@Slf4j
public class NoOpCloudHealthEventService implements CloudHealthEventService {

    @Override
    public void handleUnhealthyCloud(CloudHealthEntity entity) {
        log.debug("NoOpCloudHealthEventService.handleUnhealthyCloud called — no-op");
    }
}
