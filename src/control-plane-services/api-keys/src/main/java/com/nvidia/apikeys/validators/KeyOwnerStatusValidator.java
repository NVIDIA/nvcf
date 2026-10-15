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

import static com.nvidia.apikeys.vo.KeyOwnerStatus.ACTIVE;

import com.nvidia.apikeys.services.KeyOwnerService;
import com.nvidia.apikeys.vo.KeyOwnerVo;
import com.nvidia.boot.exceptions.ForbiddenException;
import java.time.Instant;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.apache.commons.lang3.Strings;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RequiredArgsConstructor
public class KeyOwnerStatusValidator {

    public KeyOwnerVo updateAndValidateActorStatus(KeyOwnerVo keyOwner, String actorId) {
        switch (keyOwner.getOwnerType()) {
            case USER -> {
                assertCallerMatchesKeyOwner(keyOwner, actorId);
            }
            default ->
                    throw new IllegalStateException("Unexpected value: " + keyOwner.getOwnerType());
        }

        assertKeyOwnerIsActive(keyOwner);
        return keyOwner;
    }


    private static void assertKeyOwnerIsActive(KeyOwnerVo keyOwner) {
        if (keyOwner.getOwnerStatus() != ACTIVE) {
            log.info("Key owner is not active: {}", keyOwner);
            throw new ForbiddenException("Key owner suspended.");
        }
    }

    private static void assertCallerMatchesKeyOwner(KeyOwnerVo keyOwner, String actorId) {
        if (!Strings.CS.equals(keyOwner.getOwnerId(), actorId)) {
            log.error("caller-subject-id:'{}' poses as actor-user-id:'{}'",
                      actorId, keyOwner.getOwnerId());
            throw new ForbiddenException("Not a key owner");
        }
    }

}
