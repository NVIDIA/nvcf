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
package com.nvidia.icms.service.internal;

import static com.nvidia.icms.util.InstanceServiceUtil.getStringValue;

import com.nvidia.icms.errors.IcmsBadRequestException;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestAction;
import com.nvidia.icms.inbound.rest.model.swagger.schema.SpotInstanceRequestSchema;
import jakarta.validation.constraints.NotNull;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;

/**
 * Validates non-BYOC-specific and NVCT task request fields before instance creation.
 */
@Service
@Slf4j
public class InstanceValidationService {

    public static final String TASK_VALIDATION_ERROR =
            "%s must be provided when TaskDetails.TaskId is provided";

    public static final String TASK_DURATION_VALIDATION_ERROR =
            "LaunchSpecification.TerminationGracePeriodDuration "
                    + "must not be more than LaunchSpecification.MaxRuntimeDuration";

    /**
     * Validates required fields for NVCT task requests (those with a non-blank taskId).
     * No-op for non-task (function) requests.
     *
     * @param instanceRequest the incoming instance request
     * @throws com.nvidia.icms.errors.IcmsBadRequestException if a required field is absent
     *
     */
    public void validateTaskWorkload(@NotNull SpotInstanceRequestSchema spotRequest) {
        if (StringUtils.isBlank(getStringValue(spotRequest.getTaskId()))) {
            return;
        }

        if (spotRequest.getMaxQueuedDuration() == null) {
            throwValidationError("LaunchSpecification.MaxQueuedDuration");
        }
        if (spotRequest.getTerminationGracePeriodDuration() == null) {
            throwValidationError("LaunchSpecification.TerminationGracePeriodDuration");
        }
        if (spotRequest.getResultHandlingStrategy() != null) {
            throwValidationError("LaunchSpecification.ResultHandlingStrategy");
        }
        if (StringUtils.isBlank(spotRequest.getOwnerNcaIdForTask())) {
            throwValidationError("TaskDetails.OwnerNcaId");
        }
        if (StringUtils.isBlank(spotRequest.getAccountName())) {
            throwValidationError("TaskDetails.AccountName");
        }

        // Termination grace period must not exceed max runtime duration
        if (spotRequest.getMaxRuntimeDuration() != null &&
                spotRequest.getTerminationGracePeriodDuration()
                        .compareTo(spotRequest.getMaxRuntimeDuration()) > 0) {
            log.error(TASK_DURATION_VALIDATION_ERROR);
            throw new IcmsBadRequestException(TASK_DURATION_VALIDATION_ERROR);
        }

        spotRequest.setAction(SpotInstanceRequestAction.REQUEST_SPOT_INSTANCES_FOR_TASK);
    }

    private static void throwValidationError(String fieldName) {
        String errorMessage = String.format(TASK_VALIDATION_ERROR, fieldName);
        log.error(errorMessage);
        throw new IcmsBadRequestException(errorMessage);
    }
}
