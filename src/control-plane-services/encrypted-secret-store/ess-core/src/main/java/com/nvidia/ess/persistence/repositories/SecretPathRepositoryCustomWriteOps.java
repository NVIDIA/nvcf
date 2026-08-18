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

import com.nvidia.ess.persistence.models.SecretPathModel;
import com.nvidia.ess.utils.namedtuples.SecretPathWriteArgs;
import java.util.List;
import java.util.UUID;
import reactor.core.publisher.Mono;

public interface SecretPathRepositoryCustomWriteOps {
  /**
   * <p>Perform a batch-write of the passed {@link SecretPathModel} rows
   * to Cassandra table {@code ess.secret_paths_by_entity}.</p>
   * 
   * <p>As part of the first write in the batch, apply an LWT conditioned on
   * the value of {@code entity_version} in the partition being equal to
   * {@code secretPathInsertionArgs.getPrevEntityVersionForCAS()} while setting it to
   * {@code secretPathInsertionArgs.getNewEntityVersion()}.</p>
   * 
   * @param namespace The namespace containing the entity under which these paths will be persisted.
   * 
   * @param entity The entity ({@code "<entity_type>/<entity_id>"}) under which these paths will be persisted.
   * 
   * @param secretPathInsertionArgs The path-rows to be written as well as the previous and new values of
   * {@code entity_version} to be respectively checked and applied by an LWT.
   *
   * @return {@code Mono.just(true)} if there was a successful write or no write attempt was required (nothing
   * to be written), and {@code Mono.just(false)} if there was a write attempt that ended without errors but no
   * writes were performed. If an error occurred during the transaction, {@code Mono.error(<error>)} is returned.
   */
  public Mono<Boolean> batchWriteSecretPathsWithEntityVersionLWT(String namespace, String entity,
      SecretPathWriteArgs secretPathInsertionArgs);

  /**
   * <p>Delete the paths given by the provided {@code (namespace, entity, path)} key for each {@code path} in the
   * given {@code paths} list, within the {@code secret_paths_by_entity} table, if it exists.</p>
   * 
   * <p>The deletion is performed along with an LWT-check conditioned on value of the {@code entity_version} column
   * equaling {@code oldVersion} right before the deletion. The LWT also updates the value of {@code entity_version} to
   * {@code newVersion} upon success.</p>
   * 
   * @param namespace
   * @param entity
   * @param paths
   * @param oldVersion
   * @param newVersion
   * @return
   */
  public Mono<Boolean> deletePathsByVersion(String namespace, String entity, List<String> paths, UUID oldVersion,
      UUID newVersion);
}
