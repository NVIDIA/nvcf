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
package com.nvidia.icms.service;

import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.inbound.rest.model.GetActiveInstanceInfoResponse;
import com.nvidia.icms.inbound.rest.model.InstanceInfo;
import com.nvidia.icms.outbound.cassandra.instance.InstancePerZoneRepository;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceByZoneEntity;
import java.util.ArrayList;
import java.util.List;

import io.micrometer.observation.annotation.Observed;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

@Service
@AllArgsConstructor
@Slf4j
public class InstanceInfoService {

    private InstancePerZoneRepository instancePerZoneRepository;

    @Observed
    public GetActiveInstanceInfoResponse getActiveInstancesForZone(String zoneName) {

        try {
            List<InstanceInfo> instanceInfoList = new ArrayList<>();

            List<InstanceByZoneEntity> zoneEntityList =
                    instancePerZoneRepository.findAllActiveInstancesByZone(zoneName);

            zoneEntityList.forEach(entity -> instanceInfoList.add(toInstanceInfo(entity)));

            return GetActiveInstanceInfoResponse.builder()
                    .instances(instanceInfoList)
                    .build();
        } catch (Exception exception) {
            String errMsg =
                    String.format("Failed to get active instances for %s zone, error: %s", zoneName,
                            exception.getMessage());
            log.error("error - {}, exception -", errMsg, exception);

            // rethrowing same exception
            throw exception;
        }
    }

    private InstanceInfo toInstanceInfo(InstanceByZoneEntity entity) {
        return InstanceInfo.builder()
                .instanceState(entity.getInstanceStateName().getStateName())
                .instanceId(entity.getKey().getInstanceId())
                .requestId(entity.getRequestId())
                .build();
    }
}
