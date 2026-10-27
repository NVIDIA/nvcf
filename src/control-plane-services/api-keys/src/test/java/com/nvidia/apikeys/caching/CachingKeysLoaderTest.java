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

package com.nvidia.apikeys.caching;

import static com.nvidia.apikeys.TestData.API_KEY_HASH_1;
import static com.nvidia.apikeys.TestData.KEY_VO_1;
import static com.nvidia.apikeys.TestData.TEST_TIME;
import static org.assertj.core.api.Assertions.assertThat;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.apikeys.services.ValidatingKeyLoader;
import com.nvidia.boot.exceptions.NotFoundException;
import java.time.Clock;
import java.time.ZoneOffset;
import java.util.Optional;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class CachingKeysLoaderTest {

    @Mock
    private ValidatingKeyLoader keyLoader;

    @Test
    void invalidationReloadsPreviouslyMissingKey() {
        var cache = new CachingKeysLoader(keyLoader, Clock.fixed(TEST_TIME, ZoneOffset.UTC));
        when(keyLoader.getKeyByHashIfExists(API_KEY_HASH_1))
                .thenReturn(Optional.empty(), Optional.of(KEY_VO_1));

        assertThrows(NotFoundException.class, () -> cache.loadKeyByHash(API_KEY_HASH_1));
        assertThrows(NotFoundException.class, () -> cache.loadKeyByHash(API_KEY_HASH_1));
        verify(keyLoader).getKeyByHashIfExists(API_KEY_HASH_1);

        cache.invalidate();

        assertThat(cache.loadKeyByHash(API_KEY_HASH_1)).isEqualTo(KEY_VO_1);
        verify(keyLoader, times(2)).getKeyByHashIfExists(API_KEY_HASH_1);
    }
}
