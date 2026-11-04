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
package com.nvidia.ess.encryption.it;

import com.nvidia.ess.encryption.scheduled.KeyRotatorScheduledService;
import com.nvidia.ess.encryption.scheduled.ReactiveKeyRotatorScheduledService;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.MethodOrderer;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestMethodOrder;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.context.ContextConfiguration;
import org.springframework.test.context.junit.jupiter.SpringExtension;
import org.springframework.test.context.web.WebAppConfiguration;

@ExtendWith(SpringExtension.class)
@ExtendWith(MockitoExtension.class)
@SpringBootTest(classes = TestApplication.class, properties = {
        "spring.profiles.active:it",
        "encryption.rotation.scheduled.enabled=true",
        "encryption.rotation.scheduled.cron=-",
        "spring.main.web-application-type=reactive"
})
@WebAppConfiguration
@ContextConfiguration
@TestMethodOrder(MethodOrderer.OrderAnnotation.class)
class ReactiveKeyRotatorScheduledServiceIT {

    @Autowired
    private KeyRotatorScheduledService keyRotatorScheduledService;


    @Test
    void configure_isReactiveKeyRotatorScheduledService() {
        Assertions.assertTrue(keyRotatorScheduledService.getClass().isAssignableFrom(
                ReactiveKeyRotatorScheduledService.class));
    }
}
