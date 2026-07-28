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

import com.nvidia.boot.exceptions.BadRequestException;
import lombok.RequiredArgsConstructor;
import org.springframework.stereotype.Service;
import org.springframework.web.context.request.RequestContextHolder;
import org.springframework.web.context.request.ServletRequestAttributes;

@Service
@RequiredArgsConstructor
public class ActorResolver {

    public static final String HEADER_KEY_OWNER_ID = "Key-Owner-Id";

    public String getValidatedActorId() {
        if (RequestContextHolder.getRequestAttributes() instanceof ServletRequestAttributes attr) {
            String actorHeaderValue = attr.getRequest().getHeader(HEADER_KEY_OWNER_ID);
            if (actorHeaderValue == null) {
                throw new BadRequestException("Key-Owner-Id is not set");
            }
            return actorHeaderValue;
        } else {
            throw new IllegalStateException("Called from outside of request context");
        }
    }

}
