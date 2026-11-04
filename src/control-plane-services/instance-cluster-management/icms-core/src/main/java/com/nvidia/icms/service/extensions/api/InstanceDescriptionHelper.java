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
package com.nvidia.icms.service.extensions.api;

import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.outbound.cassandra.sqsmessage.entity.SqsMessageEntity;
import com.nvidia.icms.service.ZoneInfo;

import java.util.Optional;

/**
 * Provides backend-specific helper operations used during instance describe and cancel flows.
 *
 * <p>Responsibilities:
 * <ul>
 *   <li>Resolving zone information from an instance entity</li>
 *   <li>Determining whether an inbound SQS message batch has exceeded its validity window.</li>
 * </ul>
 */
public interface InstanceDescriptionHelper {

    /**
     * Resolves the backend information for the given instance entity.
     *
     * @param entity the instance entity to inspect; may be {@code null}
     * @return an {@link Optional} containing a {@link ZoneInfo} with {@code cloudProvider=<backend>}
     *         and the entity's zone name, or {@link Optional#empty()} if {@code entity} is
     *         {@code null} or its zone is {@code null}/blank
     */
    Optional<ZoneInfo> resolveZoneInfo(InstanceV2Entity entity);

    /**
     * ### TODO: Another isNonByocSomething() - That makes this really bad.
     *
     * Returns {@code true} when all of the following conditions hold:
     * <ol>
     *   <li>The per-message-batch backend validation feature flag is enabled.</li>
     *   <li>{@code resourceProvider} is the non-BYOC provider.</li>
     *   <li>{@code sqsMessageEntity} and its {@code creationTime} are non-{@code null}.</li>
     *   <li>The message's {@code creationTime} is older than the configured validation window.</li>
     * </ol>
     *
     * @param resourceProvider  the resource provider associated with the inbound message;
     * @param sqsMessageEntity  the SQS message entity to evaluate; {@code null} returns
     *                          {@code false}
     * @return {@code true} if the batch is considered expired, {@code false} otherwise
     */
    boolean isNonByocBatchExpired(ResourceProvider resourceProvider, SqsMessageEntity sqsMessageEntity);
}
