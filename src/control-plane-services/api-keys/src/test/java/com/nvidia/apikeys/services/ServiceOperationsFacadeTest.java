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

import static com.nvidia.apikeys.TestData.SERVICE_DTO_1;
import static com.nvidia.apikeys.TestData.SERVICE_ID_1;
import static com.nvidia.apikeys.TestData.SERVICE_VO_1;
import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.Mockito.when;

import com.nvidia.apikeys.converters.ServicesConvertor;
import com.nvidia.apikeys.dto.keys.ListServicesResponse;
import com.nvidia.apikeys.facade.ServiceOperationsFacade;
import java.util.List;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class ServiceOperationsFacadeTest {

    @Mock
    private ServicesService servicesServiceMock;
    @Mock
    private ServicesConvertor servicesConvertorMock;

    @InjectMocks
    private ServiceOperationsFacade facade;


    @Test
    void getService() {
        when(servicesServiceMock.loadById(SERVICE_ID_1)).thenReturn(SERVICE_VO_1);
        when(servicesConvertorMock.toDto(SERVICE_VO_1)).thenReturn(SERVICE_DTO_1);

        assertThat(facade.getService(SERVICE_ID_1)).isEqualTo(SERVICE_DTO_1);
    }

    @Test
    void listServices() {
        when(servicesServiceMock.listServices()).thenReturn(List.of());

        var expected = ListServicesResponse.builder()
                .services(List.of())
                .build();

        assertThat(facade.listServices()).isEqualTo(expected);

        when(servicesServiceMock.listServices()).thenReturn(List.of(SERVICE_VO_1));
        when(servicesConvertorMock.toDto(SERVICE_VO_1)).thenReturn(SERVICE_DTO_1);
        expected = ListServicesResponse.builder()
                .services(List.of(SERVICE_DTO_1))
                .build();

        assertThat(facade.listServices()).isEqualTo(expected);
    }
}
