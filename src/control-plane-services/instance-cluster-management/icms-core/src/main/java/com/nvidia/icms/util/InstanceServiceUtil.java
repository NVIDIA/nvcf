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
package com.nvidia.icms.util;

import static com.nvidia.icms.service.telemetry.TelemetryEventClient.AWS_REGION_ENV_KEY;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.POD_NAME_ENV_KEY;

import com.nvidia.icms.inbound.rest.model.swagger.schema.SpotInstanceRequestSchema;
import java.util.HashSet;
import java.util.List;
import java.util.Set;
import java.util.UUID;
import java.util.regex.Matcher;
import java.util.regex.Pattern;
import lombok.Builder;
import lombok.Data;
import lombok.experimental.UtilityClass;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;

@UtilityClass
@Slf4j
public class InstanceServiceUtil {

    public static final String INSTANCE_TYPE = "INSTANCE_TYPE";

    public static final String INSTANCE_ID = "INSTANCE_ID";

    // Example: BM.GPU.A100-v2.8_8x
    public static final String INSTANCE_TYPE_PARSING_PATTERN = "^(.*)_([^_]*)$";

    @Data
    @Builder
    public static class InstanceTypeInformation {

        String instanceName;
        Integer gpuCount;
    }

    public InstanceTypeInformation getInstanceTypeInformation(String instanceType) {

        instanceType = instanceType.trim();

        // Define the regex pattern
        Pattern pattern = Pattern.compile(INSTANCE_TYPE_PARSING_PATTERN);

        // Match the input string against the pattern
        Matcher matcher = pattern.matcher(instanceType);

        try {
            if (matcher.matches()) {
                String performanceClass = matcher.group(1);
                String gpuCount = matcher.group(2);
                gpuCount = gpuCount.replace("x", "");

                return InstanceTypeInformation.builder()
                        .gpuCount(Integer.valueOf(gpuCount))
                        .instanceName(performanceClass)
                        .build();
            }
        } catch (Exception exception) {
            // Ignoring the exception
            // Returning instanceType as instance name
            // Returning gpuCount as 1
            log.error("Failed to parse instance type to find gpu count and instance name," +
                              " provided instance-type {}, exception - {}", instanceType,
                      exception.getMessage(), exception);
        }

        return InstanceServiceUtil.InstanceTypeInformation.builder()
                .instanceName(instanceType)
                .gpuCount(1)
                .build();
    }


    /**
     * Generates a random UUID
     *
     * @return a random UUID
     */
    public static String generateRandomUUID() {
        return UUID.randomUUID().toString();
    }

    public static String getStringValueOfUuid(UUID uuid) {
        if (uuid != null) {
            return uuid.toString();
        }
        return null;
    }

    public static String getStringValue(Object obj) {
        return obj == null ? null : obj.toString();
    }

    public static boolean isSetEmptyOrNull(Set<?> set) {
        return set == null || set.isEmpty();
    }

    public static boolean isListEmptyOrNull(List<?> list) {
        return list == null || list.isEmpty();
    }

    public static Set<String> extractAttributes(SpotInstanceRequestSchema instanceRequest) {
        Set<String> attributes = new HashSet<>();
        if (instanceRequest != null && instanceRequest.getAttributes() != null) {
            attributes.addAll(instanceRequest.getAttributes());
        }
        return attributes;
    }

    public static boolean isModelCacheEnabled(Boolean modelCacheEnabled) {
        if (modelCacheEnabled != null) {
            return Boolean.TRUE.equals(modelCacheEnabled);
        }
        // If model cache field is not set that means request is of older (i.e before adding cacheEnabled field)
        // So we give benefit of doubt and consider it as model cache request
        return true;
    }

    public static double findStringSizeInKb(String input) {
        try {
            byte[] bytes = input.getBytes();
            return (double) bytes.length / 1024;
        } catch (Exception exception) {
            // Suppressing the error as this will be used for logging purpose
            log.error("Failed to find size of string, error {}", exception.getMessage(), exception);
        }
        return 0;
    }

    public static String getPodName() {
        return System.getenv(POD_NAME_ENV_KEY);
    }

    public static String getAwsRegion() {
        return System.getenv(AWS_REGION_ENV_KEY);
    }

    /**
     * Returns {@code true} if the request is for a task (i.e. has a non-blank task ID).
     *
     * @param instanceRequest the instance request to check
     * @return {@code true} if the request carries a task ID
     */
    public static boolean isRequestForTask(SpotInstanceRequestSchema instanceRequest) {
        if (instanceRequest == null) {
            return false;
        }
        return StringUtils.isNotBlank(getStringValue(instanceRequest.getTaskId()));
    }

    /**
     * Returns {@code true} if targeting is enabled for the request, meaning no specific backend
     * has been requested and the routing engine should select one.
     *
     * @param instanceRequest the instance request to check
     * @return {@code true} if the {@code backend} field is blank (targeting enabled)
     */
    public static boolean isTargetingEnabled(SpotInstanceRequestSchema instanceRequest) {
        return instanceRequest == null || StringUtils.isBlank(instanceRequest.getBackend());
    }
}
