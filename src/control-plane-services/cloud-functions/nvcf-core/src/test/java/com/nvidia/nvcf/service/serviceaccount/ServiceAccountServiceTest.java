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
package com.nvidia.nvcf.service.serviceaccount;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.when;

import com.nvidia.boot.exceptions.UpstreamException;
import com.nvidia.nvcf.service.apikeys.ApiKeyValidationResult.RateLimitAttributes;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class ServiceAccountServiceTest {

    @Mock
    private ServiceAccountClient serviceAccountClient;

    @Test
    void getTieredRateLimit_returnsEmptyAndSkipsLookupWhenDisabled() {
        var serviceAccountService = new ServiceAccountService(serviceAccountClient, false);

        var result = serviceAccountService.getTieredRateLimit("test-nca-id");

        assertThat(result).isEqualTo(new RateLimitAttributes(null, null));
        verifyNoInteractions(serviceAccountClient);
    }

    @Test
    void getTieredRateLimit_cachesRepeatedRequestsForSameNcaId() {
        var rate = new RateLimitAttributes("1000-M", "500-M");
        when(serviceAccountClient.fetchTieredRateLimit("nca-1")).thenReturn(rate);
        var serviceAccountService = new ServiceAccountService(serviceAccountClient, true);

        var first = serviceAccountService.getTieredRateLimit("nca-1");
        var second = serviceAccountService.getTieredRateLimit("nca-1");

        assertThat(first).isEqualTo(rate);
        assertThat(second).isEqualTo(rate);
        verify(serviceAccountClient, times(1)).fetchTieredRateLimit("nca-1");
    }

    @Test
    void getTieredRateLimit_loadsDistinctNcaIdsIndependently() {
        var rateForNca1 = new RateLimitAttributes("1000-M", "500-M");
        var rateForNca2 = new RateLimitAttributes("2000-M", "900-M");
        when(serviceAccountClient.fetchTieredRateLimit("nca-1")).thenReturn(rateForNca1);
        when(serviceAccountClient.fetchTieredRateLimit("nca-2")).thenReturn(rateForNca2);
        var serviceAccountService = new ServiceAccountService(serviceAccountClient, true);

        assertThat(serviceAccountService.getTieredRateLimit("nca-1")).isEqualTo(rateForNca1);
        assertThat(serviceAccountService.getTieredRateLimit("nca-2")).isEqualTo(rateForNca2);
        // repeat nca-1 to prove it's still independently cached, not clobbered by nca-2's load
        assertThat(serviceAccountService.getTieredRateLimit("nca-1")).isEqualTo(rateForNca1);

        verify(serviceAccountClient, times(1)).fetchTieredRateLimit("nca-1");
        verify(serviceAccountClient, times(1)).fetchTieredRateLimit("nca-2");
    }

    @Test
    void getTieredRateLimit_fallsBackToBackupCacheWhenUpstreamUnreachable() {
        var rate = new RateLimitAttributes("1000-M", "500-M");
        when(serviceAccountClient.fetchTieredRateLimit("nca-1"))
                .thenReturn(rate)
                .thenThrow(new UpstreamException("NAK unreachable"));
        var serviceAccountService = new ServiceAccountService(serviceAccountClient, true);

        // populates both the primary and the backup cache
        assertThat(serviceAccountService.getTieredRateLimit("nca-1")).isEqualTo(rate);

        // force a reload without touching the backup cache
        serviceAccountService.invalidatePrimaryCache();

        assertThat(serviceAccountService.getTieredRateLimit("nca-1")).isEqualTo(rate);
        verify(serviceAccountClient, times(2)).fetchTieredRateLimit("nca-1");
    }

    @Test
    void getTieredRateLimit_throwsWhenUnreachableAndNotInBackupCache() {
        when(serviceAccountClient.fetchTieredRateLimit("nca-1"))
                .thenThrow(new UpstreamException("NAK unreachable"));
        var serviceAccountService = new ServiceAccountService(serviceAccountClient, true);

        assertThatThrownBy(() -> serviceAccountService.getTieredRateLimit("nca-1"))
                .isInstanceOf(UpstreamException.class);
    }
}
