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
package com.nvidia.icms.service.byoc;


import com.nvidia.icms.configuration.byoc.ByocConfigurationProperties;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.inbound.rest.model.swagger.schema.SpotInstanceRequestSchema;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.UUID;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;

@Slf4j
@AllArgsConstructor
@Service
public class ByocServiceHelper {

    private final ByocConfigurationProperties byocConfigurationProperties;
    private final TelemetryEventClient telemetryEventClient;


    public static UUID getUuid() {
        return UUID.randomUUID();
    }

    long roundOfCacheSizeInGb(Long cacheSizeInBytes) {
        try {
            // Setting 1Gi as default value if provided size is Null
            if (cacheSizeInBytes == null || cacheSizeInBytes == 0) {
                log.debug("cacheSizeInBytes is not set hence setting PVC size as 1Gi");
                return 1;
            }

            long sizeGB =
                    (cacheSizeInBytes / byocConfigurationProperties.getCacheByteDivisionFactor());

            // We need to add a 5% space. xfs has 5% reserved space.
            sizeGB += (sizeGB * byocConfigurationProperties.getCacheReservedSpace()) / 100;

            // Adding extra buffer for small size models
            sizeGB += byocConfigurationProperties.getCacheBytesBuffer();

            return sizeGB;

        } catch (Exception exception) {
            String errMsg = String.format("Exception while getCacheSizeInGb, error: %s",
                                          exception.getMessage());
            log.error(errMsg, exception);

            Map<String, Object> metadata = new HashMap<>();
            metadata.put("ProvidedCacheSize", cacheSizeInBytes);
            metadata.put("DivisionFactor",
                         byocConfigurationProperties.getCacheByteDivisionFactor());
            metadata.put("CacheBuffer", byocConfigurationProperties.getCacheBytesBuffer());
            metadata.put("CacheReservedSpace", byocConfigurationProperties.getCacheReservedSpace());
            telemetryEventClient.triggerEvent(List.of(new GenericMetric()
                                                               .withEventName(
                                                                       Events.CACHE_BYTES_CONVERSION_FAILED.toString())
                                                               .withMetadata(metadata)
                                                               .withError(exception.getMessage())));

            throw new IcmsInternalServerException(errMsg);
        }
    }

    public String getRoundedOfCacheSizeInGi(Long cacheSizeInBytes) {
        return String.format("%sGi", roundOfCacheSizeInGb(cacheSizeInBytes));
    }

    public long getRoundedOfCacheSizeInBytes(Long cacheSizeInBytes) {
        return roundOfCacheSizeInGb(cacheSizeInBytes) << 30;
    }

    public boolean isModelCachingEnabled(SpotInstanceRequestSchema instanceRequest) {

        log.debug(
                "isEnabled {}, isHelmEmpty {}, isCacheArtifact {}, isCacheHandleEmpty {}",
                byocConfigurationProperties.isModelCachingEnabled(),
                StringUtils.isEmpty(instanceRequest.getHelmChart()),
                instanceRequest.isCacheArtifacts(),
                StringUtils.isEmpty(instanceRequest.getCacheHandle()));

        // If request is for helm then don't send model caching artifacts
        return byocConfigurationProperties.isModelCachingEnabled() &&
                StringUtils.isEmpty(instanceRequest.getHelmChart()) &&
                instanceRequest.isCacheArtifacts() &&
                !StringUtils.isEmpty(instanceRequest.getCacheHandle());
    }
}
