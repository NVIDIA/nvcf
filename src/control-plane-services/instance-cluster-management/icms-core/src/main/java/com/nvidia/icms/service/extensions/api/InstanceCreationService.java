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

import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2Entity;
import com.nvidia.icms.outbound.sqs.model.GdnLaunchSpecification;
import com.nvidia.icms.inbound.rest.model.swagger.schema.SpotInstanceRequestSchema;
import com.nvidia.icms.service.createInstances.RequestInstanceDestination;
import jakarta.annotation.Nullable;
import jakarta.validation.constraints.NotNull;
import java.util.Map;
import java.util.Set;
import java.util.UUID;

public interface InstanceCreationService {

    /**
     * Enqueues one SQS creation message per backend destination.
     *
     * <p>Each message is dispatched to the backend-specific queue associated with the destination.
     * This method executes <strong>synchronously</strong>: it blocks until all SQS sends have
     * completed (or failed). A failed SQS send may propagate a runtime exception from the
     * underlying SQS client.
     *
     * @param customer          the customer identifier used to attribute the request
     * @param nonByocDestinations   the resolved destinations to send messages for; must not be empty
     * @param instanceRequest   the original instance request containing GPU, instance type,
     *                          and other creation parameters
     * @param requestId         the unique identifier for this request, included in each message
     * @param envVars           environment variables to embed in the SQS message payload
     * @param gdnLaunchSpecification optional backend-specific launch parameters; {@code null} for
     *                               non-GDN requests
     */
    void sendSqsMessages(
            @NotNull String customer,
            @NotNull Set<RequestInstanceDestination> nonByocDestinations,
            @NotNull SpotInstanceRequestSchema instanceRequest,
            @NotNull UUID requestId,
            @NotNull Map<String, String> envVars,
            @Nullable GdnLaunchSpecification gdnLaunchSpecification);

    /**
     * Emits a telemetry event for each backend destination in the request.
     *
     * <p>For destinations without a region filter, only the first destination triggers an
     * event and iteration stops early. For all other cases every destination produces an event.
     * Events are dispatched via {@code TelemetryEventClient} as a side effect and no result
     * is returned. This method executes <strong>synchronously</strong> and blocks until all
     * telemetry events have been dispatched.
     *
     * @param nonByocDestinations   the resolved backend destinations for which to emit telemetry
     * @param instanceRequest   the original instance request, used to extract region and
     *                          other attributes for the telemetry payload
     * @param instanceRequestEntity the persisted request entity, used to populate request-level
     *                          metadata in the event
     * @param envVars           environment variables included in the telemetry payload
     * @param gdnLaunchSpecification optional GDN-specific launch parameters included in the
     *                               telemetry payload; {@code null} for non-GDN requests
     */
    void sendTelemetry(
            @NotNull Set<RequestInstanceDestination> nonByocDestinations,
            @NotNull SpotInstanceRequestSchema instanceRequest,
            @NotNull InstanceRequestV2Entity instanceRequestEntity,
            @NotNull Map<String, String> envVars,
            @Nullable GdnLaunchSpecification gdnLaunchSpecification);
}
