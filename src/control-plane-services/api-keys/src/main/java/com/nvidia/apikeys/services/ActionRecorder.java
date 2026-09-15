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

package com.nvidia.apikeys.services;

import java.util.Set;
import java.util.stream.Collectors;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RequiredArgsConstructor
public class ActionRecorder {

    public enum ResourceType {SERVICE, KEY_OWNER, API_KEY, USER}

    public enum Action {SUSPEND, UPDATE, CREATE, DELETE, INTROSPECT, ACTIVE, SUSPENDED, NOT_APPLICABLE}

    public static final String VALUE_NOT_APPLICABLE = "N/A";

    private final ActorResolver actorJwtResolver;

    public void record(
            ResourceType resourceType, String resourceId,
            String resourceOwnerType, String resourceOwnerId,
            Action action, String issuerServiceId, Set<String> audienceServiceIds) {

        try {
            String userId = getUserId();
            String clientId = getClientId();

            String audiences = audienceServiceIds == null || audienceServiceIds.isEmpty()
                    ? VALUE_NOT_APPLICABLE
                    : audienceServiceIds.stream()
                            .sorted()
                            .collect(Collectors.joining(","));

            log.info("resource-type:'{}' resource-id:'{}' resource-owner-type:'{}' "
                             + "resource-owner-id:'{}' action:'{}' actor-user-id:'{}' "
                             + "actor-client-id:'{}' issuer-service-id:'{}' audience-service-id:'{}'",
                     resourceType, resourceId, resourceOwnerType, resourceOwnerId, action, userId,
                     clientId, issuerServiceId, audiences);
        } catch (Throwable ignored) {
            // any exception in this method must be captured to avoid disruption of execution
            // this method is non-essential and must not affect any business logic.
        }
    }

    private String getUserId() {
        return actorJwtResolver.getValidatedActorId();
    }

    private String getClientId() {
        return VALUE_NOT_APPLICABLE;
    }
}
