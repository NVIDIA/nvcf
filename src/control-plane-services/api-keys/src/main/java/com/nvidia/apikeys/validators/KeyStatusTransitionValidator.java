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

package com.nvidia.apikeys.validators;

import com.nvidia.apikeys.vo.KeyStatus;
import com.nvidia.boot.exceptions.BadRequestException;
import java.util.Map;
import java.util.Set;
import org.springframework.stereotype.Service;

@Service
public class KeyStatusTransitionValidator {

    /**
     * Maps current key status to set of allowed new statuses.
     */
    private static final Map<KeyStatus, Set<KeyStatus>> VALID_KEY_STATUS_FOR_UPDATE = Map.of(
            KeyStatus.ACTIVE, Set.of(KeyStatus.SUSPENDED),
            KeyStatus.SUSPENDED, Set.of(KeyStatus.ACTIVE),
            KeyStatus.EXPIRED, Set.of(KeyStatus.SUSPENDED)
    );

    public void assertStatusTransitionValid(KeyStatus current, KeyStatus desired) {
        if (current == null
                || desired == null
                || !VALID_KEY_STATUS_FOR_UPDATE.getOrDefault(current, Set.of()).contains(desired)) {
            throw new BadRequestException("Invalid status transition");
        }
    }

}
