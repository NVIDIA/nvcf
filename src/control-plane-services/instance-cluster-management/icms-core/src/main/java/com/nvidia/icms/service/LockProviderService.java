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
package com.nvidia.icms.service;

import static com.nvidia.icms.service.telemetry.TelemetryEventClient.POD_NAME_ENV_KEY;

import com.google.common.annotations.VisibleForTesting;
import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.outbound.cassandra.lockbyttl.LockByTtlRepository;
import com.nvidia.icms.outbound.cassandra.lockbyttl.entity.LockByTtlEntity;
import java.net.InetAddress;
import java.net.UnknownHostException;
import java.time.Instant;
import java.util.Optional;
import java.util.concurrent.TimeUnit;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.RandomStringUtils;
import org.springframework.stereotype.Service;

@Service
@Slf4j
public class LockProviderService {

    @VisibleForTesting
    final LockByTtlRepository lockByTtlRepository;

    private final IcmsConfigurationProperties icmsConfigurationProperties;

    private String myName;
    // in case when environment var for instance is not set, we can use temporary name.
    private String myTempName;

    public LockProviderService(
            LockByTtlRepository lockByTtlRepository,
            IcmsConfigurationProperties icmsConfigurationProperties) {
        this.lockByTtlRepository = lockByTtlRepository;
        this.icmsConfigurationProperties = icmsConfigurationProperties;
    }

    public boolean obtainLockWithTtl(String lockName, int ttl) {
        Optional<LockByTtlEntity> lockByTtl = lockByTtlRepository.findByLockName(lockName);
        if (lockByTtl.isPresent()) {
            log.debug("Lock {} already taken by ({}) at {} with TTL {}",
                      lockName,
                      lockByTtl.get().getLockedBy(),
                      lockByTtl.get().getLockedAt().toString(),
                      lockByTtl.get().getLockTtl());
            return false; // lock cannot be obtained
        }

        LockByTtlEntity newLock = new LockByTtlEntity(lockName, Instant.now(), getLockOwnerName(), ttl);
        lockByTtlRepository.insertWithTtl(newLock, ttl);
        try {
            TimeUnit.SECONDS.sleep(
                    icmsConfigurationProperties.getWaitForDbLockByTtlValidationInSeconds());
        } catch (InterruptedException e) {
            log.error("Error of obtaining lock {} Exception {}", lockName, e.getMessage(), e);
            return false;
        }

        lockByTtl = lockByTtlRepository.findByLockName(lockName);
        if (lockByTtl.isPresent()) {
            if (lockByTtl.get().getLockedBy().equals(getLockOwnerName())) {
                log.debug("Lock {} obtained successfully by this instance ({}) at {} with TTL {}",
                          lockName,
                          lockByTtl.get().getLockedBy(),
                          lockByTtl.get().getLockedAt().toString(),
                          lockByTtl.get().getLockTtl());
                return true;
            } else {
                log.info("Lock {} was retaken by ({}) at {} with TTL {}",
                          lockName,
                          lockByTtl.get().getLockedBy(),
                          lockByTtl.get().getLockedAt().toString(),
                          lockByTtl.get().getLockTtl());
                return false; // lock cannot be obtained
            }
        }
        log.error("Created lock {} cannot be found in DB", lockName);
        return false;
    }

    private String getLockOwnerName() {
        if (myName != null && !myName.trim().isEmpty()) {
            return myName;
        }

        myName = System.getenv(POD_NAME_ENV_KEY);
        if (myName != null && !myName.trim().isEmpty()) {
            log.info("LockProviderService is starting to use a new name {}", myName);
            return myName;
        }

        if (myTempName != null && !myTempName.trim().isEmpty()) {
            return myTempName;
        }

        try {
            myTempName = InetAddress.getLocalHost().getHostAddress();
        } catch (UnknownHostException e) {
            log.error("Cannot detect current IP address, using fake one 0.0.0.0");
            myTempName = "0.0.0.0";
        }
        myTempName += "_" + RandomStringUtils.randomAlphanumeric(10);

        log.info("LockProviderService is initialized with temporary name {}", myTempName);

        return myTempName;
    }
}
