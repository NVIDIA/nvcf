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

import com.nvidia.ess.constants.Constants.RetriesExhaustedErrorTags.Errors;
import com.nvidia.ess.constants.Constants.RetriesExhaustedErrorTags.SubErrors;
import com.nvidia.ess.exceptions.RetriesExhaustedInternalErrorException;
import com.nvidia.ess.exceptions.RetryableException;
import com.nvidia.ess.persistence.models.SecretPathModel;
import com.nvidia.ess.persistence.models.SecretPathPartitionModel;
import com.nvidia.ess.utils.LogMessageStringUtils;
import com.nvidia.ess.utils.namedtuples.SecretPathWriteArgs;
import java.util.List;
import java.util.UUID;
import lombok.Setter;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.data.cassandra.core.ReactiveCassandraTemplate;
import org.springframework.data.cassandra.core.UpdateOptions;
import org.springframework.data.cassandra.core.WriteResult;
import org.springframework.data.cassandra.core.query.Criteria;
import org.springframework.stereotype.Repository;
import reactor.core.publisher.Mono;

@Repository
public class SecretPathRepositoryCustomWriteOpsImpl implements SecretPathRepositoryCustomWriteOps {

  @Setter(onMethod_ = {@Autowired})
  private ReactiveCassandraTemplate reactiveCassandraTemplate;

  @Override
  public Mono<Boolean> batchWriteSecretPathsWithEntityVersionLWT(String namespace, String entity,
      SecretPathWriteArgs secretPathInsertionArgs) {
    
    // For each `SecretPathModel` row to be inserted, construct an `INSERT INTO <table>(...) VALUES(...)`
    // CQL statement.
    //
    if (secretPathInsertionArgs.getPathsToWrite().isEmpty()) {
      // Nothing to be done here if there's nothing to insert.
      return Mono.just(true);
    }

    // The LWT'd `entity_version` update needs to be its own write and not a part of one
    // of the writes inserting each of the `SecretPathModel` rows.
    //
    // This is because the `IF entity_version = {oldEntityVersion}` condition isn't allowed
    // at the end of `INSERT INTO <table>(...) VALUES(...)` statements. It's allowed only at
    // the end of `UPDATE <table> SET col1 = val1... WHERE col2 = val2...` statements.
    //
    var lwtEntityVersionUpdate = SecretPathPartitionModel.builder()
        .namespace(namespace)
        .entity(entity)
        .entityVersion(secretPathInsertionArgs.getNewEntityVersion())
        .build();

    // Apply the LWT `IF entity_version = {prev-value}` condition via an
    // `UpdateOptions` instance.
    //
    var lwtUpdateOptions = UpdateOptions.builder()
        .ifCondition(
            Criteria.where(SecretPathModel.COLUMN_ENTITY_VERSION)
                .is(secretPathInsertionArgs.getPrevEntityVersionForCAS())
        )
        .build();
    
    // Construct and (reactively) execute the batch of statements. The default
    // consistency-level should be LOCAL_QUORUM.
    //
    return reactiveCassandraTemplate.batchOps()
        .update(List.of(lwtEntityVersionUpdate), lwtUpdateOptions)
        .insert(secretPathInsertionArgs.getPathsToWrite())
        .execute()
        .map(WriteResult::wasApplied)
        // If execution failed with an error, push the exception to be handled downstream. This error should be
        // retryable if there's an outer retry-loop with retries left.
        .onErrorMap(ex -> new RetryableException(new RetriesExhaustedInternalErrorException(
            LogMessageStringUtils.errorSummary(Errors.INTERNAL_SERVER_ERROR, SubErrors.SECRET_PATH_DB_WRITE_FAILURE,
                namespace, entity),
            "unexpected error while saving secret path", ex)));
  }

  // Perform batch operation with two queries:
  //   1. delete the secret
  //   2. update the static version column with newVersion if its current value = oldVersion
  public Mono<Boolean> deletePathsByVersion(String namespace, String entity, List<String> paths, UUID oldVersion, UUID newVersion) {

    var secretModels = paths.stream().map(
        path -> SecretPathModel.builder()
            .namespace(namespace)
            .entity(entity)
            .path(path)
            .entityVersion(newVersion) // IGNORED in the WHERE clause. A full partition-key is already provided.
            .build()
    )
    .toList();

    var staticColumnModel = SecretPathPartitionModel.builder()
            .namespace(namespace)
            .entity(entity)
            .entityVersion(newVersion)
            .build();

    UpdateOptions options = UpdateOptions.builder()
            .ifCondition(Criteria.where(SecretPathModel.COLUMN_ENTITY_VERSION).is(oldVersion))
            .build();

    // Batch operation with LWT.
    //
    // Note: need to use
    //               update(Iterable<?> entities, WriteOptions options)
    //       and not
    //               update(Object entity, WriteOptions options)
    //       because the latter has a default implementation that uses insert instead of update.
    //       ifCondition applies only to update/delete and not insert, so it is ignored and the criteria is not applied.
    //       So the update with LWT ends up as an insert w/o LWT.

    return reactiveCassandraTemplate.batchOps()
            .delete(secretModels)
            .update(List.of(staticColumnModel), options)
            .execute()
            .map(WriteResult::wasApplied);
  }
}
