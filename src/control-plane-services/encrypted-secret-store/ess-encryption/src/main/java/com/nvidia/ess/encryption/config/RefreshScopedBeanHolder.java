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
package com.nvidia.ess.encryption.config;

/**
 * <p>This interface provides a mechanism to mock dependencies with a
 * {@link org.springframework.cloud.context.config.annotation.RefreshScope} annotation
 * inside {@link org.springframework.boot.test.context.SpringBootTest} integration-tests
 * without the need to use {@link org.springframework.test.annotation.DirtiesContext}.</p>
 * 
 * <p>Implement this interface with a class that can hold a bean of type {@link T}
 * with a {@code @RefreshScope} annotation. <strong>Look at
 * {@link com.nvidia.ess.encryption.config.properties.CryptoPropertiesHolder} for a
 * simple example.</strong></p>
 * 
 * <p>Use this interface as the member-type for a dependency on the corresponding
 * {@code @RefreshScope} bean inside any dependent bean (inject it with the
 * interface implementation described above for non-testing contexts). <strong>Look at usage inside
 * {@link com.nvidia.ess.encryption.crypto.key.BaseEncryptionKeyService} for
 * an example.</strong></p>
 * 
 * <p>For test-environments where attributes of the {@code @RefreshScope} dependency need
 * to change mid-test without use of {@code @DirtiesContext}, simply declare the
 * {@code RefreshScopedBeanHolder<}{@link T}{@code >} dependency as a
 * {@link org.springframework.boot.test.mock.mockito.SpyBean} in the relevant
 * {@code @SpringBootTest} and mock the {@link RefreshScopedBeanHolder#get()} method to
 * return a stub instance of the {@code @RefreshScope} bean. <strong>Look at usage inside
 * {@link com.nvidia.ess.encryption.it.BaseEncryptionKeyServiceIT} for an
 * example.</strong></p>
 * 
 */
public interface RefreshScopedBeanHolder<T> {
    T get();
}
