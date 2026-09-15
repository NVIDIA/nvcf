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

import static com.nvidia.apikeys.vo.KeyStatus.ACTIVE;

import com.nvidia.apikeys.vo.KeyByOwnerAndServiceVo;
import com.nvidia.apikeys.vo.KeyStatus;
import com.nvidia.apikeys.vo.KeyVo;
import java.time.Clock;
import java.time.Instant;
import java.util.function.Supplier;
import lombok.RequiredArgsConstructor;
import org.springframework.stereotype.Service;

/**
 * This class should be used with Keys that were just pulled from the database to adjust expiration
 * status.
 */
@Service
@RequiredArgsConstructor
public class KeyExpirationValidator {

    private final Clock clock;

    public KeyVo validateStatus(KeyVo key) {
        return isExpired(key::getExpiresAt, key::getKeyStatus)
                ? key.toBuilder()
                .keyStatus(KeyStatus.EXPIRED)
                .build()
                : key;
    }

    public KeyByOwnerAndServiceVo validateStatus(KeyByOwnerAndServiceVo key) {
        return isExpired(key::getExpiresAt, key::getKeyStatus)
                ? key.toBuilder()
                .keyStatus(KeyStatus.EXPIRED)
                .build()
                : key;
    }

    private boolean isExpired(
            Supplier<Instant> expirationSupplier, Supplier<KeyStatus> statusSupplier) {
        return statusSupplier.get() == ACTIVE
                && clock.instant().isAfter(expirationSupplier.get());
    }


}
