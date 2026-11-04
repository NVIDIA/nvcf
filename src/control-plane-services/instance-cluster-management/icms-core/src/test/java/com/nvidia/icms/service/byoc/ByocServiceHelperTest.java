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
package com.nvidia.icms.service.byoc;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.ArgumentMatchers.anyList;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.icms.configuration.byoc.ByocConfigurationProperties;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class ByocServiceHelperTest {

    @Mock
    ByocConfigurationProperties byocConfigurationProperties;

    @Mock
    TelemetryEventClient telemetryEventClient;

    @InjectMocks
    ByocServiceHelper byocServiceHelper;

    @Test
    void getRoundedOfCacheSizeInBytes_withValidInputs_returnsSuccess(){

        // Prepare
        when(byocConfigurationProperties.getCacheByteDivisionFactor()).thenReturn(1000000000L);
        when(byocConfigurationProperties.getCacheBytesBuffer()).thenReturn(1L);
        when(byocConfigurationProperties.getCacheReservedSpace()).thenReturn(5L);

        // Act
       long output = byocServiceHelper.getRoundedOfCacheSizeInBytes(73769040730L);

       // Assert
       assertEquals(82678120448L, output);

       // Verify
        verify(byocConfigurationProperties).getCacheByteDivisionFactor();
        verify(byocConfigurationProperties).getCacheBytesBuffer();
        verify(byocConfigurationProperties).getCacheReservedSpace();
    }

    @Test
    void getCacheSizeInGi_withValidInputs_returnsSuccess() {
        // Prepare
        when(byocConfigurationProperties.getCacheByteDivisionFactor()).thenReturn(1000000000L);
        when(byocConfigurationProperties.getCacheBytesBuffer()).thenReturn(1L);
        when(byocConfigurationProperties.getCacheReservedSpace()).thenReturn(5L);

        // Act
        String output = byocServiceHelper.getRoundedOfCacheSizeInGi(73769040730L);

        // Assert
        assertEquals("77Gi", output);

        // Verify
        verify(byocConfigurationProperties).getCacheByteDivisionFactor();
        verify(byocConfigurationProperties).getCacheBytesBuffer();
        verify(byocConfigurationProperties).getCacheReservedSpace();
    }

    @Test
    void getRoundedOfCacheSizeInGi_withConversionFailure_sendsTelemetryAndThrows() {
        when(byocConfigurationProperties.getCacheByteDivisionFactor()).thenReturn(0L);

        assertThrows(IcmsInternalServerException.class,
                () -> byocServiceHelper.getRoundedOfCacheSizeInGi(73769040730L));

        verify(telemetryEventClient).triggerEvent(anyList());
    }

    @Test
    void getRoundedOfCacheSizeInBytes_withConversionFailure_sendsTelemetryAndThrows() {
        when(byocConfigurationProperties.getCacheByteDivisionFactor()).thenReturn(0L);

        assertThrows(IcmsInternalServerException.class,
                () -> byocServiceHelper.getRoundedOfCacheSizeInBytes(73769040730L));

        verify(telemetryEventClient).triggerEvent(anyList());
    }
}