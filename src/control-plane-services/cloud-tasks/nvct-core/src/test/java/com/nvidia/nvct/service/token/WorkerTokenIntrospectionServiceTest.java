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
package com.nvidia.nvct.service.token;

import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.nvct.service.icms.IcmsClient;
import com.nvidia.nvct.service.icms.IcmsStubService;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class WorkerTokenIntrospectionServiceTest {

    @Mock
    private IcmsClient icmsClient;

    private WorkerTokenIntrospectionService service;
    private WorkerTokenIntrospectionService disabledService;

    @BeforeEach
    void setUp() {
        service = new WorkerTokenIntrospectionService(icmsClient, true);
        disabledService = new WorkerTokenIntrospectionService(icmsClient, false);
    }

    @Test
    void isEnabled_returnsTrue_whenFlagSet() {
        assertThat(service.isEnabled()).isTrue();
    }

    @Test
    void isEnabled_returnsFalse_whenFlagNotSet() {
        assertThat(disabledService.isEnabled()).isFalse();
    }

    @Test
    void introspect_returnsActiveResult_andCaches() {
        var activeResult = IcmsStubService.WorkerTokenIntrospectResult.builder()
                .active(true)
                .instanceId("inst-1")
                .workerId("pod-1")
                .tokenType("psat")
                .build();
        when(icmsClient.introspectWorkerToken(any())).thenReturn(activeResult);

        var result1 = service.introspect("token.val.ue");
        var result2 = service.introspect("token.val.ue");

        assertThat(result1.isActive()).isTrue();
        assertThat(result2.isActive()).isTrue();
        // Second call should hit cache — ICMS called only once.
        verify(icmsClient, times(1)).introspectWorkerToken(any());
    }

    @Test
    void introspect_doesNotCache_whenInactive() {
        var inactiveResult = IcmsStubService.WorkerTokenIntrospectResult.builder()
                .active(false)
                .error("JWT verification failed")
                .build();
        when(icmsClient.introspectWorkerToken(any())).thenReturn(inactiveResult);

        service.introspect("bad.token.here");
        service.introspect("bad.token.here");

        // Both calls hit ICMS because inactive results are not cached.
        verify(icmsClient, times(2)).introspectWorkerToken(any());
    }

    @Test
    void introspect_differentTokens_callIcmsSeparately() {
        var result = IcmsStubService.WorkerTokenIntrospectResult.builder()
                .active(true)
                .instanceId("inst-2")
                .build();
        when(icmsClient.introspectWorkerToken(any())).thenReturn(result);

        service.introspect("token.a.1");
        service.introspect("token.b.2");

        verify(icmsClient, times(2)).introspectWorkerToken(any());
    }

    @Test
    void introspect_passesTokenToIcms() {
        var result = IcmsStubService.WorkerTokenIntrospectResult.builder()
                .active(false)
                .build();
        when(icmsClient.introspectWorkerToken(any())).thenReturn(result);

        service.introspect("my.raw.token");

        verify(icmsClient).introspectWorkerToken(
                IcmsStubService.WorkerTokenIntrospectRequest.builder().token("my.raw.token").build());
    }
}
