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

import com.nvidia.apikeys.config.NakProperties;
import com.nvidia.apikeys.config.exceptions.ServiceUnavailableException;
import com.nvidia.apikeys.persistance.dao.RowUpdateLockDao;
import java.time.Duration;
import java.time.Instant;
import lombok.RequiredArgsConstructor;
import org.springframework.stereotype.Service;

/**
 * Maintenance window for the legacy key migration.
 *
 * <p>The migration runner holds a lease row in row_update_lock while it copies legacy keys. While
 * the lease exists, every replica rejects key updates and deletes for the legacy account with 503,
 * so the runner never races a write to the same key. The check runs whether or not the migration
 * task is enabled on this replica.
 */
@Service
@RequiredArgsConstructor
public class LegacyKeyMaintenance {

    public static final String LEASE_TABLE = "legacy_key_migration";
    public static final String LEASE_KEY = "leader";
    static final String MESG_MAINTENANCE = "Key maintenance in progress";

    private final RowUpdateLockDao rowUpdateLockDao;
    private final LegacyAccountTranslator legacyAccount;
    private final NakProperties nakProperties;

    /**
     * Rejects a key write for the legacy account while a migration run holds the lease, so the
     * runner never copies a key that a concurrent request is changing. Call it before loading the
     * key. Other accounts are never blocked.
     *
     * @throws ServiceUnavailableException with a Retry-After of one lease duration
     */
    public void assertWritable(String ncaId) {
        if (legacyAccount.isLegacy(ncaId) && rowUpdateLockDao.isLocked(LEASE_TABLE, LEASE_KEY)) {
            throw new ServiceUnavailableException(MESG_MAINTENANCE, leaseDuration());
        }
    }

    public boolean acquire(Instant token) {
        return rowUpdateLockDao.lock(LEASE_TABLE, LEASE_KEY, token, leaseTtlSeconds());
    }

    public boolean renew(Instant token) {
        return rowUpdateLockDao.renew(LEASE_TABLE, LEASE_KEY, token, leaseTtlSeconds());
    }

    public boolean release(Instant token) {
        return rowUpdateLockDao.unlock(LEASE_TABLE, LEASE_KEY, token);
    }

    private int leaseTtlSeconds() {
        return Math.toIntExact(Math.max(1, leaseDuration().toSeconds()));
    }

    private Duration leaseDuration() {
        return nakProperties.getMigration().getLegacyKeys().getLeaseDuration();
    }
}
