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
package com.nvidia.nvcf.service.client;

import static org.assertj.core.api.AssertionsForClassTypes.assertThat;
import static org.mockito.Mockito.when;

import com.nvidia.nvcf.persistence.client.ClientsRepository;
import com.nvidia.nvcf.persistence.client.entity.ClientEntity;
import java.util.Optional;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class ClientServiceTest {

    @InjectMocks
    private ClientService clientService;

    @Mock
    private ClientsRepository clientsRepository;

    @AfterEach
    void reset() {
        clientService.clearClientCache();
    }

    @Test
    void testNonEmptyCache() {
        var cache = clientService.getClientCache();
        assertThat(cache.estimatedSize()).isZero();

        // First time Caffeine cache is empty, this triggers uncached path
        when(clientsRepository.findById("test")).thenReturn(Optional.of(new ClientEntity()));
        var result = clientService.lookupClient("test");
        assertThat(result).isPresent();
        assertThat(cache.estimatedSize()).isEqualTo(1);

        // Second time Caffeine has it; won't trigger uncached path
        var result2 = clientService.lookupClient("test");
        assertThat(result2).isPresent();
        assertThat(cache.estimatedSize()).isEqualTo(1);

        assertThat(result).isEqualTo(result2);
    }

    @Test
    void testEmptyCache() {
        var cache = clientService.getClientCache();
        assertThat(cache.estimatedSize()).isZero();

        // First time Caffeine cache is empty, this triggers uncached path
        when(clientsRepository.findById("test")).thenReturn(Optional.empty());
        var result = clientService.lookupClient("test");
        assertThat(result).isEmpty();
        assertThat(cache.estimatedSize()).isZero();

        // Second time Caffeine still doesn't have it, this triggers uncached path again
        var result2 = clientService.lookupClient("test");
        assertThat(result2).isEmpty();
        assertThat(cache.estimatedSize()).isZero();
    }

    @Test
    void testEmptyCacheThenNonEmpty() {
        var cache = clientService.getClientCache();
        assertThat(cache.estimatedSize()).isZero();

        // First time Caffeine cache is empty, this triggers uncached path
        when(clientsRepository.findById("test")).thenReturn(Optional.empty());
        var result = clientService.lookupClient("test");
        assertThat(result).isEmpty();
        assertThat(cache.estimatedSize()).isZero();

        // Then add some client, Caffeine doesn't have it, this triggers uncached path
        when(clientsRepository.findById("test")).thenReturn(Optional.of(new ClientEntity()));
        var result2 = clientService.lookupClient("test");
        assertThat(result2).isNotNull();
        assertThat(cache.estimatedSize()).isEqualTo(1);

        var result3 = clientService.lookupClient("test");
        assertThat(result2).isNotNull();
        assertThat(result2).isEqualTo(result3);
        assertThat(cache.estimatedSize()).isEqualTo(1);
    }
}
