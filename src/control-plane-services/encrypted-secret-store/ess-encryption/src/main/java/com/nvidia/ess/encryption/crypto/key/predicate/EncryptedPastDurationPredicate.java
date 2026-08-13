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

import static com.nvidia.ess.encryption.metrics.EncryptionMetricsRegistry.maskKid;

import com.datastax.oss.driver.api.core.uuid.Uuids;
import com.nvidia.ess.encryption.persistence.models.EncryptionKeyModel;
import java.time.Duration;
import java.time.Instant;
import java.util.UUID;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;

@AllArgsConstructor
@Slf4j
public class EncryptedPastDurationPredicate implements EncryptionKeyPredicate {

    private final Duration duration;

    private final String mekKid;

    /*
     * Predicate passes if the NEK based off when it was encrypted by MEK and which MEK it was encrypted with
     * 1. MEK kid is a UUIDv1 with a timestamp. It is guaranteed that the loaded in MEK is a UUIDv1. However, for backwards compatibility, NEK's encryptedByKid might not be one.
     *   a. If NEK's encryptedByKid is not a UUIDv1, skip to Step 3
     * 2. Compare timestamps of NEK's encryptedByKid and MEK kid.
     *   a. If encryptedByKid's timestamp >= MEK kid's timestamp, then fail the predicate (return false)
     *   b. Otherwise, continue to Step 3
     * 3. Compare the time passed since when the NEK was re-encrypted with X duration (encryptedAt field)
     *   a. If now - encryptedAt >= X duration, predicate passes
     *   b. if now - encryptedAt < X duration, predicate fails
     */
    @Override
    public boolean shouldRun(EncryptionKeyModel encryptionKeyModel) {
        Instant now = Instant.now();
        Instant encryptionKeyEncryptedAt = encryptionKeyModel.getEncryptedAt();
        String encryptedByKid = encryptionKeyModel.getEncryptedByKid();

        if (isEncryptedByNewerOrEqualMek(encryptedByKid,
                mekKid)) {
            return false;
        }

        return Duration.between(encryptionKeyEncryptedAt, now).compareTo(duration) >= 0;
    }

    private boolean isEncryptedByNewerOrEqualMek(String encryptedByKid, String mekKid) {
        if (mekKid.equals(encryptedByKid)) {
            return true;
        }

        UUID encryptedByKidUuid;
        // if it so happens that a NEK was encrypted by a MEK kid that is not UUID v1, then it is safe to assume that the current MEK is newer
        // additionally, current MEK will be a UUID v1 because it is validated on startup in CryptoProperties
        try {
            encryptedByKidUuid = UUID.fromString(encryptedByKid);
            if (encryptedByKidUuid.version() != 1) {
                // TODO metrics
                log.warn("NEK's encryptedByKid {} is not a UUID. Assuming {} to have been an older MEK.", maskKid(encryptedByKid), maskKid(encryptedByKid));
                return false;
            }
        } catch (IllegalArgumentException _) {
            // TODO metrics
            log.warn("NEK's encryptedByKid {} is not a UUID v1. Assuming {} to have been an older MEK.", maskKid(encryptedByKid), maskKid(encryptedByKid));
            return false;
        }

        // all should be UUID v1
        long encryptedByKidTimestamp = Uuids.unixTimestamp(encryptedByKidUuid);
        long mekKidTimestamp = Uuids.unixTimestamp(UUID.fromString(mekKid));

        return encryptedByKidTimestamp >= mekKidTimestamp;
    }
}
