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
package com.nvidia.icms.service.scheduled.gpuusage;

import tools.jackson.databind.ObjectMapper;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.inbound.rest.model.ClientRequestDataModel;
import io.micrometer.observation.annotation.Observed;
import java.time.Instant;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

@Service
@AllArgsConstructor
@Slf4j
public class GpuUsageEventServiceHelper {

    private final ObjectMapper objectMapper;

    // Use this method to get Instant.now(), we can mock this method in unit tests with custom Instant
    public Instant getInstantNow() {
        return Instant.now();
    }

    @Observed
    public ClientRequestDataModel parseRequestInfo(String request) {
        try {
            ClientRequestDataModel requestData;
            requestData =
                    objectMapper.readValue(request, ClientRequestDataModel.class);
            return requestData;
        } catch (Exception e) {
            String errMsg =
                    String.format("Failed to parse request information, error: %s", e.getMessage());
            log.error("error: {}", errMsg, e);
            throw new IcmsInternalServerException(errMsg, e);
        }
    }
}
