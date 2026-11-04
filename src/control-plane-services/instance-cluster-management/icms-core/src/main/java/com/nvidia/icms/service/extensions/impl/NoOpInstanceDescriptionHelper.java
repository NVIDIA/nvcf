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

import com.nvidia.icms.service.extensions.api.InstanceDescriptionHelper;

import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.outbound.cassandra.sqsmessage.entity.SqsMessageEntity;
import com.nvidia.icms.service.ZoneInfo;
import java.util.Optional;
import lombok.extern.slf4j.Slf4j;

/**
 * No-op implementation of {@link InstanceDescriptionHelper} that is registered only when no other
 * {@link InstanceDescriptionHelper} bean is present in the application context.
 *
 * <p>Returns safe defaults: empty zone info and non-expired batch status, so that describe
 * and cancel flows continue without error when non-BYOC-specific helpers are not configured.
 */
@Slf4j
public class NoOpInstanceDescriptionHelper implements InstanceDescriptionHelper {

    /**
     * In normal implementations, resolves the non-BYOC zone information for the given instance entity.
     * This no-op implementation performs no lookup and always returns {@link java.util.Optional#empty()}.
     *
     * @return always {@link java.util.Optional#empty()}
     */
    @Override
    public Optional<ZoneInfo> resolveZoneInfo(InstanceV2Entity entity) {
        log.debug("NoOpInstanceDescriptionHelper.resolveZoneInfo called — returning empty");
        return Optional.empty();
    }

    /**
     * In normal implementations, returns {@code true} when the non-BYOC validation feature is enabled
     * and the SQS message batch has exceeded its configured validity window.
     * This no-op implementation always returns {@code false} so that no batches are discarded
     * when non-BYOC-specific batch expiry checking is not configured.
     *
     * @return always {@code false}
     */
    @Override
    public boolean isNonByocBatchExpired(ResourceProvider resourceProvider,
                                     SqsMessageEntity sqsMessageEntity) {
        log.debug("NoOpInstanceDescriptionHelper.isNonByocBatchExpired called — returning false");
        return false;
    }
}
