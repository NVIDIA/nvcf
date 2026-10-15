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
package com.nvidia.icms.outbound.cassandra.lockbyttl;

import com.google.common.annotations.VisibleForTesting;
import com.nvidia.icms.outbound.cassandra.lockbyttl.entity.LockByTtlEntity;

import java.time.Duration;
import java.util.Optional;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Repository;

@Slf4j
@Repository
public class LockByTtlRepository {

    @VisibleForTesting
    public final LockByTtlRepo lockByTtlRepo;

    public LockByTtlRepository(LockByTtlRepo lockByTtlRepo) {
        this.lockByTtlRepo = lockByTtlRepo;
    }

    public boolean insertWithTtl(LockByTtlEntity entity, int ttl) {
        if (!lockByTtlRepo.insertWithTtl(entity, Duration.ofSeconds(ttl), true)) {
            log.error("Lock {} for {} already exists", entity.getLockName(), entity.getLockedBy());
            return false;
        }

        return true;
    }

    public boolean insertIfNotExist(LockByTtlEntity entity) {
        if (!lockByTtlRepo.isInsertedIfNotExists(entity)) {
            log.error("Lock {} for {} already exists", entity.getLockName(), entity.getLockedBy());
            return false;
        }

        return true;
    }


    public Optional<LockByTtlEntity> findByLockName(String lockName) {
        return lockByTtlRepo.findById(lockName);
    }

    public LockByTtlEntity update(LockByTtlEntity entity) {
        return lockByTtlRepo.update(entity);
    }
}

