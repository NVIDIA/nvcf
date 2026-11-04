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
package com.nvidia.nvct.service.task;

import static com.nvidia.nvct.service.apikeys.ApiKeysService.isApiKeyAuth;

import java.util.Optional;
import java.util.UUID;
import lombok.experimental.UtilityClass;
import org.springframework.security.core.Authentication;

@UtilityClass
public class TaskPredicateUtils {

    public static boolean taskAccessMatch(
            Authentication authentication,
            Optional<UUID> optTaskId) {
        return isApiKeyAuth(authentication).map(access -> {
            if (access.privateTasksAllowed()) {
                return true;
            }

            return optTaskId
                    .map(access::hasResourcesScopedForTask)
                    .orElse(false);
        }).orElseGet(() -> true);  // JWT and others
    }
}
