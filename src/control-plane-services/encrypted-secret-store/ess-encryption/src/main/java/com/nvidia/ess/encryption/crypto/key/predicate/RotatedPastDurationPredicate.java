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
package com.nvidia.ess.encryption.crypto.key.predicate;

import com.datastax.oss.driver.api.core.uuid.Uuids;
import com.nvidia.ess.encryption.persistence.models.EncryptionKeyModel;
import java.time.Duration;
import java.time.Instant;
import lombok.AllArgsConstructor;

@AllArgsConstructor
public class RotatedPastDurationPredicate implements EncryptionKeyPredicate {

    private final Duration duration;

    /*
     * Predicate passes if time passed since NEK was rotated >= X duration (createdAt field)
     */
    @Override
    public boolean shouldRun(EncryptionKeyModel encryptionKeyModel) {
        Instant now = Instant.now();
        Instant encryptionKeyCreatedAt = Instant.ofEpochMilli(Uuids.unixTimestamp(encryptionKeyModel.getCreatedAt()));
        return Duration.between(encryptionKeyCreatedAt, now).compareTo(duration) >= 0;
    }
}
