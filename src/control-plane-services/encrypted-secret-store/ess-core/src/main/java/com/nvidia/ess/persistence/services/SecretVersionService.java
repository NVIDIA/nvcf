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
package com.nvidia.ess.persistence.services;

import static com.nvidia.ess.constants.Constants.MSG_SECRET_NOT_FOUND;
import static com.nvidia.ess.constants.Constants.MSG_SECRET_VERSION_NOT_FOUND;
import static com.nvidia.ess.constants.OpenTelemetryAttributes.LWT_WRITE_FAILURE_OPERATION_KEY;
import static com.nvidia.ess.constants.OpenTelemetryAttributes.PARTIAL_CREATE_TYPE_KEY;
import static com.nvidia.ess.constants.OpenTelemetryAttributes.SECRET_CAS_ERROR_ACTUAL_VERSION_KEY;
import static com.nvidia.ess.constants.OpenTelemetryAttributes.SECRET_CAS_ERROR_PROVIDED_VERSION_KEY;

import com.datastax.oss.driver.api.core.uuid.Uuids;
import com.nvidia.boot.exceptions.ConflictException;
import com.nvidia.ess.constants.Constants;
import com.nvidia.ess.constants.Constants.RetriesExhaustedErrorTags.Errors;
import com.nvidia.ess.constants.Constants.RetriesExhaustedErrorTags.SubErrors;
import com.nvidia.ess.constants.ErrorSubType;
import com.nvidia.ess.constants.OpenTelemetryAttributes.PartialCreateType;
import com.nvidia.ess.exceptions.RetriesExhaustedInternalErrorException;
import com.nvidia.ess.exceptions.RetriesExhaustedTooManyRequestsException;
import com.nvidia.ess.exceptions.RetryableException;
import com.nvidia.ess.metrics.CustomMetricsRegistry;
import com.nvidia.ess.metrics.CustomMetricsRegistry.LwtOperation;
import com.nvidia.ess.persistence.models.SecretVersionModel;
import com.nvidia.ess.persistence.repositories.SecretVersionRepository;
import com.nvidia.ess.telemetry.TelemetryComponents;
import com.nvidia.ess.telemetry.TelemetryComponentsImpl;
import com.nvidia.ess.utils.ExceptionUtils;
import com.nvidia.ess.utils.LogMessageStringUtils;
import jakarta.annotation.Nullable;
import java.util.Objects;
import java.util.Optional;
import java.util.UUID;
import lombok.Setter;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.tuple.Pair;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Qualifier;
import org.springframework.stereotype.Service;
import org.springframework.web.ErrorResponseException;
import reactor.core.publisher.Flux;
import reactor.core.publisher.Mono;

@Slf4j
@Service
public class SecretVersionService {

    @Setter(onMethod_ = {@Autowired})
    private SecretVersionRepository repository;

    @Setter(onMethod_ = {@Autowired})
    private CustomMetricsRegistry customMetricsRegistry;

    @Setter(onMethod_ = {@Autowired})
    @Qualifier(TelemetryComponentsImpl.BEAN_NAME)
    private TelemetryComponents telemetryComponents;

    /**
     *
     * <p><b>If {@code casVersion} is not specified:</b> Obtain the present {@link UUID} value of {@code current_version}
     * in the {@code secret_versions_by_entity_and_path} table corresponding to a {@code (namespace, entity, secret_path)}
     * partition, if it exists. <b>At this time, the only reason for this step when CAS is not required is that the
     * new value of {@code current_version} we generate for a fresh secret-version insertion is more recent than its
     * previous value</b> (as we need the {@code timeuuid} version-values of secret-versions to increase monotonically over
     * time for a secret-path). However, until the secret-version write itself applies an LWT conditioned on the current value
     * of {@code current_version} remaining unchanged (see column {@code require_lwt_for_secret_version_writes} in the
     * {@code namespaces} table), this is not a guarantee.</p>
     *
     * <p><b>If {@code casVersion} is specified:</b> Check that a secret with the same {@code version} exists in the
     * {@code (namespace, entity, secret_path)} partition and that the value of {@code current_version} is the same as
     * {@code casVersion} too.</p>
     *
     * <p>Also obtain a new new {@link UUID} value of a secret {@code version} whose timestamp is strictly greater than the
     * validated {@code casVersion} (if it's specified) or the obtained value of {@code current_version} (if {@code casVersion}
     * is not specified), and therefore can be used as the version-ID for inserting a new version of the secret in this
     * partition.</p>
     *
     * @param namespace
     * @param entity
     * @param scrtPath
     * @param casVersion
     * @return A {@link Pair} of {@code (fetched-current_version-or-validated-cas-version, valid-value-of-a-new-version)}
     */
    public Mono<Pair<UUID, UUID>> validateCurrentVersionAndGenValidNewVersion(String namespace, String entity, String scrtPath,
        Optional<UUID> casVersion) {
      return Mono.deferContextual(ctx ->
          getSecretVersion(namespace, entity, scrtPath, null)
          .onErrorMap(ex -> {
              // Rethrow any DB-fetch errors as a `RetryableException` (if there's an outer-retry-loop with retries
              // remaining, a retry of the entire execution-pipeline from the beginning is attempted. Otherwise,
              // an internal server error is returned to the API caller).
              return new RetryableException(new RetriesExhaustedInternalErrorException(
                  LogMessageStringUtils.errorSummary(Errors.INTERNAL_SERVER_ERROR,
                      SubErrors.SECRET_VERSION_FETCH_FAILURE, namespace, entity, scrtPath),
                  "Error while fetching secret's most recent version " +
                      LogMessageStringUtils.namespaceEntitySecretTuple(namespace, entity, scrtPath), ex));
          })
          .flatMap(model -> {
            // Check whether the most recent secret-version is the same as the CAS-version, if the latter is
            // specified.
            if (casVersion.map(cv -> !cv.equals(model.getVersion())).orElse(false)) {
                // The most recent secret-version is different from the specified CAS-version. This error
                // is not retryable.
                customMetricsRegistry.recordSecretCreateCasError(namespace);
                telemetryComponents.setSpanAttribute(ctx,
                        SECRET_CAS_ERROR_PROVIDED_VERSION_KEY, casVersion.get().toString());
                telemetryComponents.setSpanAttribute(ctx,
                        SECRET_CAS_ERROR_ACTUAL_VERSION_KEY, model.getVersion().toString());
                return Mono.error(new ConflictException(String.format(Constants.MSG_SECRET_VERSION_NOT_MOST_RECENT,
                      namespace, entity, scrtPath, casVersion.get(), model.getVersion())));
            }
            return Mono.just(Pair.of(model.getVersion(), model.getCurrentVersion()));
          })
          .flatMap(mostRecentVersionAndCurrentVersion -> {
            // The most recent secret-payload's `version` can sometimes diverge from the value of `current_version`
            // for the partition as these timeuuids are generated within the application and their order can differ
            // from the commit-order when multiple concurrent transactions succeed on the same partition.
            //
            // The value of the most recent `version` is compared with the value of the new version UUID to ensure that
            // the latter is more recent before being committed.
            //
            // When performing CAS updates, the present value of `current_version` is used in the condition guarding the
            // LWT that writes the new secret-payload and replaces the present value of `current_version` with the new version
            // UUID. Irrespective of whether a CAS update is requested, the value of `current_version` will be replaced with
            // the new version UUID in the secret-payload write transaction.
            //
            var mostRecentVersion = mostRecentVersionAndCurrentVersion.getLeft();
            var currentVersion = mostRecentVersionAndCurrentVersion.getRight();
            // Generate a new `timeuuid` value for insertion of a new version of this secret.
            var newVersion = Uuids.timeBased();
            if (newVersion.timestamp() <= mostRecentVersion.timestamp()) {
              // The `timeuuid` value of a new secret-version's `version`-ID should be chronologically more recent
              // than the most recent secret-version (which is the most recently inserted secret-version's version-ID
              // as of performing the current_version query). A retry is required in order to proceed further.
              //
              return Mono.error(new RetryableException(new RetriesExhaustedInternalErrorException(
                  LogMessageStringUtils.errorSummary(Errors.INTERNAL_SERVER_ERROR,
                      SubErrors.GEN_SECRET_VERSION_TOO_OLD, namespace, entity, scrtPath),
                  String.format(
                      "For secret %s: timestamp(most-recent-version) = %d is not before now() = %d. " +
                      "Cannot proceed with secret-insertion until a valid new-version is obtained.",
                      LogMessageStringUtils.namespaceEntitySecretTuple(namespace, entity, scrtPath),
                      mostRecentVersion.timestamp(), newVersion.timestamp())
              )));
            }
            // Chronological check successful. Return Pair(current_version, new_version)
            return Mono.just(Pair.of(currentVersion, newVersion));
          })
          .switchIfEmpty(Mono.<Pair<UUID, UUID>>defer(() -> {
              if (casVersion.isPresent()) {
                  // If CAS-version was provided: There was no stored payload with the given CAS-version for this
                  // secret-path. This error is not retryable.
                  customMetricsRegistry.recordSecretCreateCasError(namespace);
                  telemetryComponents.setSpanAttribute(ctx,
                          SECRET_CAS_ERROR_PROVIDED_VERSION_KEY, casVersion.get().toString());
                  return Mono.error(new ConflictException(String.format(
                          "Secret at %s has no existing payload with version=%s",
                          LogMessageStringUtils.namespaceEntitySecretTuple(namespace,
                                  entity, scrtPath),
                          casVersion.get())));
              }
              // If CAS-version wasn't provided: The `current_version` query returned an empty result. This means that
              // `current_version` must have been NULL (i.e. the partition did not exist as of performing the query).
              // So return Pair(NULL, {new-timeuuid}).
              return Mono.just(Pair.of(null, Uuids.timeBased()));
          }))
      );
    }

    /**
     *
     * <p>Write the given secret-payload (the {@code SecretVersionModel model} argument) to storage.</p>
     *
     * <p>If the {@code boolean isLWTWrite} argument is {@code true}, update the {@code current_version} of the
     * {@code (namespace, entity, secret_path)} partition to {@code model.getVersion()} using an LWT conditioned on
     * the value of {@code current_version} being equal to {@code UUID prevVersionIfLWTWrite} before the write.
     * If {@code prevVersionIfLWTWrite == null} then the LWT condition effectively checks whether the partition
     * did not exist (i.e. there were no stored secrets for the path) before this write.</p>
     *
     * <p>If the {@code boolean isLWTWrite} argument is {@code false}, still update the {@code current_version} of
     * the {@code (namespace, entity, secret_path)} partition to {@code model.getVersion()} but don't condition the
     * update using an LWT.</p>
     * 
     * <p>The parameter {@code isCASRequest} indicates that an LWT write was performed because this operation served
     * a CAS request to ESS from a client. It is always {@code false} if {@code isLWTWrite} is {@code false}. The purpose
     * of {@code isCASRequest} is to specify that {@code createSecretVersion(...)} return a {@code 409 CONFLICT} status
     * code (instead of {@code 500 INTERNAL_SERVER_ERROR} status-code) to the client when the LWT-guarded write
     * fails on account of {@code current_version} having changed in the {@code secret_versions_by_entity_and_path}
     * table such that the given value {@code prevVErsionIfLWTWrite} is no longer the most recently written value of
     * {@code current_version}.</p>
     *
     * @param model
     * @param isLWTWrite
     * @param prevVersionIfLWTWrite
     * @param isCASRequest
     * @return
     */
    public Mono<Boolean> createSecretVersion(SecretVersionModel model, boolean isLWTWrite, UUID prevVersionIfLWTWrite,
            boolean isCASRequest) {
      // Perform the write (LWT-guarded or otherwise).
        return Mono.deferContextual(ctx ->
            (isLWTWrite
                ? repository.saveNewVersionWithLWT(model, prevVersionIfLWTWrite)
                : repository.saveNewVersionWithoutLWT(model)
            ).flatMap(
                    // saveNewVersionWith[out]LWT(...) must return a non-empty mono. A failure returns Mono.error(RetryableException)
                    // while a finished operation returns Mono.just(Boolean).
                    isSuccess -> {
                    // If the write operation concluded but no rows were written, ensure to push an error downstream.
                    // This error should be retryable if an outer retry-loop exists and has retries left.
                    if (!isSuccess) {

                        telemetryComponents.setSpanAttribute(ctx, PARTIAL_CREATE_TYPE_KEY,
                                PartialCreateType.SECRET_VERSION_AFTER_PATH_BATCH.name());
                        telemetryComponents.setSpanAttribute(ctx, LWT_WRITE_FAILURE_OPERATION_KEY,
                                LwtOperation.SECRET_CREATION.name());

                        if (isCASRequest) {

                            customMetricsRegistry.recordNonRetryableLwtFailure(model.getNamespace(),
                                    LwtOperation.SECRET_CREATION);

                            // LWT failure in CAS request as the provided CAS-version is no longer the most recent secret-version.
                            // This is not a retryable error. Return a 409 CONFLICT.
                            customMetricsRegistry.recordSecretCreateCasError(model.getNamespace());
                            // cannot record the conflicting version without fetching it
                            telemetryComponents.setSpanAttribute(ctx,
                                    SECRET_CAS_ERROR_PROVIDED_VERSION_KEY,
                                    prevVersionIfLWTWrite.toString());
                            return Mono.error(new ConflictException(String.format(Constants.MSG_SECRET_VERSION_NOT_MOST_RECENT_AT_WRITE_TIME,
                              model.getNamespace(), model.getEntity(), model.getSecretPath(), prevVersionIfLWTWrite)));

                        } else {

                          // This is a failure in a non-CAS secret-version-creation request.
                          //
                          // Either the LWT (if LWT is required for all secret-version-creations in this namespace)
                          // failed or this is an unknown / inexplicable failure.
                          //
                          // This should be retryable as this is not a CAS request.

                          if (isLWTWrite) {
                            customMetricsRegistry.recordRetryableLwtFailure(model.getNamespace(),
                                LwtOperation.SECRET_CREATION);

                            return Mono.error(new RetryableException(
                                new RetriesExhaustedTooManyRequestsException(
                                    LogMessageStringUtils.errorSummary(Errors.TOO_MANY_REQUESTS,
                                        SubErrors.TOO_MANY_REQUESTS_ON_SECRET, model.getNamespace(), model.getEntity(),
                                        model.getSecretPath()),
                                    String.format(
                                        "Too many simultaneous writes to %s. Please slow down secret-insertions to this secret.",
                                        LogMessageStringUtils.namespaceEntitySecretTuple(model.getNamespace(),
                                            model.getEntity(), model.getSecretPath())))
                            ));

                          } else {
                            customMetricsRegistry.recordRetryablePartialSecretCreationOnVersion(model.getNamespace());

                            return Mono.error(new RetryableException(new RetriesExhaustedInternalErrorException(
                                LogMessageStringUtils.errorSummary(Errors.INTERNAL_SERVER_ERROR,
                                    SubErrors.EMPTY_NON_LWT_SECRET_VERSION_WRITE, model.getNamespace(), model.getEntity(),
                                    model.getSecretPath()),
                                String.format(Constants.MSG_SECRET_VERSION_EMPTY_WRITE,
                                    model.getNamespace(), model.getEntity(), model.getSecretPath()))));

                          }
                        }
                    }
                    // Otherwise, return success.
                    return Mono.just(true);
                })
        );
    }

    public Mono<SecretVersionModel> getSecretVersion(String namespace, String entity,
            String scrtPath, UUID version,
            @Nullable Class<? extends ErrorResponseException> notFoundExClass) {
        var fetch = repository.findByNamespaceAndEntityAndSecretPathAndVersion(namespace, entity,
                scrtPath, version);
        if (!Objects.isNull(notFoundExClass)) {
            // TODO: If this secret-fetch fails, check whether the entity doesn't exist as well.
            // This is to be done so that we provide the API caller a better error-message.
            return fetch.switchIfEmpty(Mono.error(() ->
                    ExceptionUtils.constructErrorResponseException(notFoundExClass,
                            String.format(MSG_SECRET_VERSION_NOT_FOUND, version, scrtPath,
                                    namespace, entity),
                            ErrorSubType.SECRET_VERSION_NOT_FOUND)
            ));
        }
        return fetch;
    }

    public Mono<SecretVersionModel> getSecretVersion(String namespace, String entity,
            String scrtPath, @Nullable Class<? extends ErrorResponseException> notFoundExClass) {
        log.debug("SecretVersionService.getSecretVersion(namespace='{}', entity='{}', secretPath='{}')", namespace, entity, scrtPath);
        var fetch = repository.findFirstByNamespaceAndEntityAndSecretPath(namespace, entity, scrtPath);
        if (!Objects.isNull(notFoundExClass)) {
            // TODO: If this secret-fetch fails, check whether the entity doesn't exist as well.
            // This is to be done so that we provide the API caller a better error-message.
            return fetch.switchIfEmpty(Mono.error(() ->
                    ExceptionUtils.constructErrorResponseException(notFoundExClass,
                            String.format(MSG_SECRET_NOT_FOUND, scrtPath, namespace, entity),
                            ErrorSubType.SECRET_NOT_FOUND)
            ));
        }
        return fetch;
    }

    // Note: this function always returns true. The caller should ignore the value.
    // The underlying repository call returns an empty mono, but this function returns a boolean
    // mono to avoid propagating emptiness and to help with chaining subsequent reactive calls.
    public Mono<Boolean> deleteSecretVersions(String namespace, String entity, String scrtPath) {
        return repository.deleteByNamespaceAndEntityAndSecretPath(namespace, entity, scrtPath)
                .thenReturn(true);
    }

    public Flux<SecretVersionModel> getSecretVersions(String namespace, String entity, String scrtPath) {
        return repository.findAllByNamespaceAndEntityAndSecretPath(namespace, entity, scrtPath);
    }

}
