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

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.integration.IntegrationTest;
import com.nvidia.icms.outbound.cassandra.lockbyttl.entity.LockByTtlEntity;
import java.util.Optional;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.beans.factory.annotation.Autowired;


@ExtendWith(MockitoExtension.class)
public class LockProviderServiceTest extends IntegrationTest {

  //  @Autowired
   // private LockByTtlRepository lockByTtlRepository;

    @Mock
    private IcmsConfigurationProperties icmsConfigurationProperties;

    @Autowired
    private LockProviderService lockProviderService;


    /*@BeforeEach
    void init() {
        lockProviderService = new LockProviderService(lockByTtlRepository, icmsConfigurationProperties);
    }

     */

    @Test
    void obtainLockWithTtl_ok() {
        //Arrange
        String lockName = "lock_test_1";
        int ttl = 30;

        //Act
        boolean result = lockProviderService.obtainLockWithTtl(lockName, ttl);

        //Assert
        assertTrue(result);
    }

    @Test
    void obtainLockWithTtl_lockTaken() {
        //Arrange
        String lockName = "lock_test_2";
        int ttl = 30;

        //Act
        boolean result1 = lockProviderService.obtainLockWithTtl(lockName, ttl);
        boolean result2 = lockProviderService.obtainLockWithTtl(lockName, ttl);

        //Assert
        assertTrue(result1);
        assertTrue(!result2);
    }


    @Test
    void obtainLockWithTtl_noInsertIfExists() {
        //Arrange
        String lockName = "lock_test_3";
        int ttl = 30;

        //Act
        boolean result1 = lockProviderService.obtainLockWithTtl(lockName, ttl);
        Optional<LockByTtlEntity> lock =  lockProviderService.lockByTtlRepository.findByLockName(lockName);
        boolean result2 = lockProviderService.lockByTtlRepository.insertIfNotExist(lock.get());

        //Also test "update" functionality
        String newLockBy = "MyRandomName";
        lock.get().setLockedBy(newLockBy);
        lockProviderService.lockByTtlRepository.update(lock.get());

        Optional<LockByTtlEntity> lock2 =  lockProviderService.lockByTtlRepository.findByLockName(lockName);

        //Assert
        assertTrue(result1);
        assertFalse(result2);
        assertEquals(newLockBy, lock2.get().getLockedBy());
    }


}
