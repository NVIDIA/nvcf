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
package com.nvidia.nvcf.service.apikeys;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.boot.exceptions.UpstreamException;
import java.util.List;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class LlmApiKeyServiceTest {

    @Mock
    private LlmApiKeyClient llmApiKeyClient;

    private static ApiKeyValidationResult result(String ncaId) {
        return new ApiKeyValidationResult(
                true,
                ncaId,
                "owner-" + ncaId,
                new ApiKeyValidationResult.Policy(List.of(), List.of("invoke_function"), "product"),
                new ApiKeyValidationResult.RateLimitAttributes("1000-M", "500-M"));
    }

    @Test
    void resolveForLlmInvocation_cachesRepeatedRequestsForSameKey() {
        var result = result("nca-1");
        when(llmApiKeyClient.fetchApiKeyValidationResult("key-1")).thenReturn(result);
        var llmApiKeyService = new LlmApiKeyService(llmApiKeyClient);

        var first = llmApiKeyService.resolveForLlmInvocation("key-1");
        var second = llmApiKeyService.resolveForLlmInvocation("key-1");

        assertThat(first).isEqualTo(result);
        assertThat(second).isEqualTo(result);
        verify(llmApiKeyClient, times(1)).fetchApiKeyValidationResult("key-1");
    }

    @Test
    void resolveForLlmInvocation_loadsDistinctKeysIndependently() {
        var resultForKey1 = result("nca-1");
        var resultForKey2 = result("nca-2");
        when(llmApiKeyClient.fetchApiKeyValidationResult("key-1")).thenReturn(resultForKey1);
        when(llmApiKeyClient.fetchApiKeyValidationResult("key-2")).thenReturn(resultForKey2);
        var llmApiKeyService = new LlmApiKeyService(llmApiKeyClient);

        assertThat(llmApiKeyService.resolveForLlmInvocation("key-1")).isEqualTo(resultForKey1);
        assertThat(llmApiKeyService.resolveForLlmInvocation("key-2")).isEqualTo(resultForKey2);
        // repeat key-1 to prove it's still independently cached, not clobbered by key-2's load
        assertThat(llmApiKeyService.resolveForLlmInvocation("key-1")).isEqualTo(resultForKey1);

        verify(llmApiKeyClient, times(1)).fetchApiKeyValidationResult("key-1");
        verify(llmApiKeyClient, times(1)).fetchApiKeyValidationResult("key-2");
    }

    @Test
    void resolveForLlmInvocation_fallsBackToBackupCacheWhenUpstreamUnreachable() {
        var result = result("nca-1");
        when(llmApiKeyClient.fetchApiKeyValidationResult("key-1"))
                .thenReturn(result)
                .thenThrow(new UpstreamException("NAK unreachable"));
        var llmApiKeyService = new LlmApiKeyService(llmApiKeyClient);

        // populates both the primary and the backup cache
        assertThat(llmApiKeyService.resolveForLlmInvocation("key-1")).isEqualTo(result);

        // force a reload without touching the backup cache
        llmApiKeyService.invalidatePrimaryCache();

        assertThat(llmApiKeyService.resolveForLlmInvocation("key-1")).isEqualTo(result);
        verify(llmApiKeyClient, times(2)).fetchApiKeyValidationResult("key-1");
    }

    @Test
    void resolveForLlmInvocation_throwsWhenUnreachableAndNotInBackupCache() {
        when(llmApiKeyClient.fetchApiKeyValidationResult("key-1"))
                .thenThrow(new UpstreamException("NAK unreachable"));
        var llmApiKeyService = new LlmApiKeyService(llmApiKeyClient);

        assertThatThrownBy(() -> llmApiKeyService.resolveForLlmInvocation("key-1"))
                .isInstanceOf(UpstreamException.class);
    }
}
