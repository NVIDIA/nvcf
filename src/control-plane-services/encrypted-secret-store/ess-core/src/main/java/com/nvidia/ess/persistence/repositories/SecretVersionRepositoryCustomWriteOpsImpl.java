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


import static com.nvidia.ess.constants.OpenTelemetryAttributes.PARTIAL_CREATE_TYPE_KEY;

import com.nvidia.ess.constants.Constants.RetriesExhaustedErrorTags.Errors;
import com.nvidia.ess.constants.Constants.RetriesExhaustedErrorTags.SubErrors;
import com.nvidia.ess.constants.OpenTelemetryAttributes.PartialCreateType;
import com.nvidia.ess.exceptions.RetriesExhaustedInternalErrorException;
import com.nvidia.ess.exceptions.RetryableException;
import com.nvidia.ess.persistence.models.SecretVersionModel;
import com.nvidia.ess.persistence.models.SecretVersionPartitionModel;
import com.nvidia.ess.telemetry.TelemetryComponents;
import com.nvidia.ess.telemetry.TelemetryComponentsImpl;
import com.nvidia.ess.utils.LogMessageStringUtils;
import java.util.List;
import java.util.UUID;
import lombok.Setter;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Qualifier;
import org.springframework.data.cassandra.core.ReactiveCassandraTemplate;
import org.springframework.data.cassandra.core.UpdateOptions;
import org.springframework.data.cassandra.core.WriteResult;
import org.springframework.data.cassandra.core.query.Criteria;
import org.springframework.stereotype.Repository;
import reactor.core.publisher.Mono;

@Repository
public class SecretVersionRepositoryCustomWriteOpsImpl implements SecretVersionRepositoryCustomWriteOps {

  @Setter(onMethod_ = {@Autowired})
  private ReactiveCassandraTemplate reactiveCassandraTemplate;

  @Setter(onMethod_ = {@Autowired})
  @Qualifier(TelemetryComponentsImpl.BEAN_NAME)
  private TelemetryComponents telemetryComponents;

  /**
   * 
   * <p>Given a secret ({@code SecretVersionModel model}), insert the secret into the
   * {@code secret_versions_by_entity_and_path} table.</p>
   * 
   * <p>If the {@code boolean isLWTWrite} argument is {@code true} contains a value, the insert is conditioned
   * on the value of {@code prevVersionIfLWTWrite} being equal to the value of the {@code current_version}
   * static table-column (i.e. {@code IF current_version = prevVersionIfLWTWrite}). This condition is applied
   * even if the value of {@code UUID prevVersionIfLWTWrite} is {@code null} (which is effectively a check on
   * whether the {@code (namespace, entity, secret_path)} partition doesn't exist).</p>
   * 
   * <p>Either way, the value of the {@code current_version} static table-column is updated to the value within
   * {@code model.getVersion()} and the value of {@code model.getCurrentVersion()} is ignored. This is because
   * the value of the {@code current_version} static table-column needs to always be equal to the {@code version}
   * column of the most recently inserted secret for the given path.</p>
   * 
   * @param model
   * @param isLWTWrite
   * @param prevVersionIfLWTWrite
   * 
   * @return {@code Mono.just(true)} if the write concluded without any errors, {@code Mono.error()} otherwise.
   */
  private Mono<Boolean> saveSecretWithOptionalVersionLWT(SecretVersionModel model, boolean isLWTWrite, UUID prevVersionIfLWTWrite) {

    Mono<Boolean> result;

    if (isLWTWrite) {
      // The write needs to be an LWT write.
      //
      // The LWT update of `current_version` needs to be in its own `UPDATE` statement.
      // `INSERT INTO <table>(...) VALUES(...)` statements cannot have an `IF <column_name> = <oldval>`
      // condition at the end. The UPDATE and the INSERT need to be batched together.
      //
      var lwtSecretVersionUpdate = SecretVersionPartitionModel.builder()
          .namespace(model.getNamespace())
          .entity(model.getEntity())
          .secretPath(model.getSecretPath())
          .currentVersion(model.getCurrentVersion())
          .build();

      // Apply the LWT `IF entity_version = {prev-value}` condition via an
      // `UpdateOptions` instance.
      //
      var lwtUpdateOptions = UpdateOptions.builder()
          .ifCondition(
              Criteria.where(SecretVersionModel.COLUMN_CURRENT_VERSION)
                  .is(prevVersionIfLWTWrite)
          )
          .build();

      // Execute a batch comprising of the above LWT-update and an
      // INSERT of the `SecretVersionModel` itself.
      result = reactiveCassandraTemplate.batchOps()
          .update(List.of(lwtSecretVersionUpdate), lwtUpdateOptions)
          .insert(model)
          .execute()
          .map(WriteResult::wasApplied);

    } else {

      // The write does not need to be a LWT write and a batch-statement is not required.
      // However, the value of the `current_version` static table-column needs to be set
      // using the `version` field from the secret to be inserted (`newVersion` from above).
      //
      result = reactiveCassandraTemplate.insert(model).map(_model -> true);
    }

    // If write execution failed with an error, surface the error to be handled downstream. This error
    // should be retryable if there's an outer-retry-loop with retries left.
    //
    // The default consistency-level with which this write is executed should be LOCAL_QUORUM.
    //
    return Mono.deferContextual(ctx ->
        result.onErrorResume(ex -> {
          telemetryComponents.setSpanAttribute(ctx, PARTIAL_CREATE_TYPE_KEY,
                  PartialCreateType.SECRET_VERSION_AFTER_PATH_UNKNOWN.name());
          // no metrics, error not related to LWT and will surface as 500
          return Mono.error(new RetryableException(new RetriesExhaustedInternalErrorException(
              LogMessageStringUtils.errorSummary(Errors.INTERNAL_SERVER_ERROR, SubErrors.SECRET_VERSION_WRITE_FAILURE,
                  model.getNamespace(), model.getEntity(), model.getSecretPath()),
              "unexpected error while writing secret to storage", ex)));
        })
    );
  }

  public Mono<Boolean> saveNewVersionWithoutLWT(SecretVersionModel secretVersionModel) {
    return saveSecretWithOptionalVersionLWT(secretVersionModel, false, null);
  }

  @Override
  public Mono<Boolean> saveNewVersionWithLWT(SecretVersionModel secretVersionModel, UUID prevVersion) {

    return saveSecretWithOptionalVersionLWT(secretVersionModel, true, prevVersion);
  }
}
