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
package com.nvidia.nvcf.service.ratelimit;

import java.util.UUID;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

@Slf4j
@Service
public class LegacyRateLimiter implements RateLimiter {
    private static final String MESG_ACCOUNT_LIMIT =
            "Account id '{}': LEGACY policy - Account rate limiting not supported";
    private static final String MESG_FUNCTION_LIMIT =
            "Function id '{}': LEGACY policy - Function rate limiting not supported";
    private static final String MESG_FUNCTION_VERSION_LIMIT =
            "Function id '{}', version '{}' : LEGACY policy - Version rate limiting not supported";

    @Override
    public void accountLimit(String ncaId, long allowedInvocations) {
        log.debug(MESG_ACCOUNT_LIMIT, ncaId);
    }

    @Override
    public void functionLimit(UUID functionId, long allowedInvocations) {
        log.debug(MESG_FUNCTION_LIMIT, functionId);
    }

    @Override
    public void functionVersionLimit(
            UUID functionId,
            UUID functionVersionId,
            long allowedInvocations) {
        log.debug(MESG_FUNCTION_VERSION_LIMIT, functionId, functionVersionId);
    }
}
