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

import static com.nvidia.apikeys.TestData.SERVICE_ID_1;
import static com.nvidia.apikeys.TestData.SERVICE_VO_1;
import static com.nvidia.apikeys.utils.TestUtils.assertThrowsExceptionWithDetails;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.apikeys.config.NakProperties;
import com.nvidia.boot.exceptions.NotFoundException;
import java.util.List;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.InjectMocks;
import org.mockito.Spy;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class ServicesServiceTest {

    @Spy
    private NakProperties nakProperties = NakProperties.builder()
            .registrations(
                    """
                    [{"serviceId":"nvidia-cloud-functions-ncp-service-id-aketm",\
                    "serviceName":"test-service",\
                    "audienceServiceIds":["nvidia-cloud-functions-ncp-service-id-aketm"],\
                    "maxApiKeysPerUser":10,\
                    "maxApiKeyTtlDays":1,\
                    "maxAuthzSizeChars": 1024,\
                    "minAuthzUpdateIntervalSeconds":10}]""")
            .build();

    @InjectMocks
    private ServicesService service;


    @Test
    void loadById_returnsIfPresent() {
        assertThat(service.loadById(SERVICE_ID_1)).isEqualTo(SERVICE_VO_1);
    }

    @Test
    void loadById_throwsIfNotPresent() {
        assertThrowsExceptionWithDetails(
                NotFoundException.class,
                () -> service.loadById("not-existing"),
                "Service 'not-existing' not found");
    }

    @Test
    void listServices() {
        assertThat(service.listServices()).isEqualTo(List.of(SERVICE_VO_1));
    }
}
