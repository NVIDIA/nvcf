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
package com.nvidia.ess.persistence.repositories;

import com.nvidia.ess.persistence.models.SecretVersionModel;
import java.util.UUID;
import reactor.core.publisher.Mono;

public interface SecretVersionRepositoryCustomWriteOps {

  /**
   * 
   * <p>Given a secret ({@link SecretVersionModel} instance), insert the secret into the
   * {@code secret_versions_by_entity_and_path} table.</p>
   * 
   * <p>This operation is different from the standard {@code SecretVersionRepository.insert(SecretVersionModel) }
   * operation in that the value of the {@code current_version} static table-column is updated to the value within
   * {@code model.getVersion()} and the value of {@code model.getCurrentVersion()} is ignored. This is because
   * the value of the {@code current_version} static table-column needs to always be equal to the {@code version}
   * column of the most recently inserted secret for the given path.</p>
   * 
   * @param secretVersionModel
   *
   * @return {@code Mono.just(true)} if the write concluded without any errors, {@code Mono.error()} otherwise.
   */
  public Mono<Boolean> saveNewVersionWithoutLWT(SecretVersionModel secretVersionModel);

  /**
   * 
   * <p>Given a secret ({@link SecretVersionModel} instance), insert the secret into the
   * {@code secret_versions_by_entity_and_path} table.</p>
   * 
   * <p>This is a CAS (check-and-set) write. The insert is conditioned on the value of the passed {@link UUID} being
   * equal to the value of the {@code current_version} static table-column (i.e.
   * {@code IF current_version = <passed-UUID>}), even if the passed {@link UUID} is {@code null} (which is effectively
   * a check on whether the {@code (namespace, entity, secret_path)} partition doesn't exist).</p>
   * 
   * <p>NOTE: The value of the {@code current_version} static table-column is updated to the value within
   * {@code model.getVersion()} and the value of {@code model.getCurrentVersion()} is ignored. This is because
   * the value of the {@code current_version} static table-column needs to always be equal to the {@code version}
   * column of the most recently inserted secret for the given path.</p>
   * 
   * @param secretVersionModel The {@link SecretVersionModel} instance to be inserted along with a CAS-check.
   * 
   * @param prevVersion The {@link UUID} version-value for the CAS-check against {@code current_version}.
   *
   * @return {@code Mono.just(true)} if the write concluded without any errors, {@code Mono.error()} otherwise.
   */
  public Mono<Boolean> saveNewVersionWithLWT(SecretVersionModel secretVersionModel, UUID prevVersion);
}
