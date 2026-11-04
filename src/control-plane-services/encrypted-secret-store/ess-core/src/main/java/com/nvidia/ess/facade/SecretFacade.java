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
package com.nvidia.ess.facade;

import static com.nvidia.ess.constants.Constants.MSG_SECRET_VERSION_NOT_FOUND;
import static com.nvidia.ess.constants.OpenTelemetryAttributes.LWT_WRITE_FAILURE_OPERATION_KEY;
import static com.nvidia.ess.constants.OpenTelemetryAttributes.PARTIAL_DELETE_TYPE_KEY;
import static com.nvidia.ess.constants.OpenTelemetryAttributes.SECRET_CAS_ERROR_PROVIDED_VERSION_KEY;

import com.datastax.oss.driver.api.core.uuid.Uuids;
import com.nvidia.boot.exceptions.BootResponseException;
import com.nvidia.boot.exceptions.ConflictException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.ess.constants.Constants;
import com.nvidia.ess.constants.Constants.RetriesExhaustedErrorTags.Errors;
import com.nvidia.ess.constants.Constants.RetriesExhaustedErrorTags.SubErrors;
import com.nvidia.ess.constants.ErrorSubType;
import com.nvidia.ess.constants.OpenTelemetryAttributes.PartialDeleteType;
import com.nvidia.ess.controller.request.CreateSecretRequest;
import com.nvidia.ess.controller.response.kv2.CreateSecretResponse;
import com.nvidia.ess.controller.response.kv2.SecretInfo;
import com.nvidia.ess.controller.response.kv2.SecretResponse;
import com.nvidia.ess.controller.response.kv2.SecretVersionMetadata;
import com.nvidia.ess.exceptions.AnomalyException;
import com.nvidia.ess.exceptions.RetriesExhaustedInternalErrorException;
import com.nvidia.ess.exceptions.RetryableException;
import com.nvidia.ess.metrics.CustomMetricsRegistry;
import com.nvidia.ess.metrics.CustomMetricsRegistry.LwtOperation;
import com.nvidia.ess.persistence.models.SecretPathModel;
import com.nvidia.ess.persistence.models.SecretVersionModel;
import com.nvidia.ess.persistence.services.EntityService;
import com.nvidia.ess.persistence.services.NamespaceService;
import com.nvidia.ess.persistence.services.SecretPathService;
import com.nvidia.ess.persistence.services.SecretVersionService;
import com.nvidia.ess.telemetry.TelemetryComponents;
import com.nvidia.ess.telemetry.TelemetryComponentsImpl;
import com.nvidia.ess.utils.EntityUtils;
import com.nvidia.ess.utils.ExceptionUtils;
import com.nvidia.ess.utils.LogMessageStringUtils;
import com.nvidia.ess.encryption.crypto.CryptoService;
import com.nvidia.ess.encryption.exceptions.BadJWEException;
import com.nvidia.ess.encryption.exceptions.MissingKeyException;
import jakarta.annotation.Nullable;
import java.time.Instant;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Optional;
import java.util.UUID;
import java.util.regex.Pattern;
import lombok.Builder;
import lombok.Data;
import lombok.NonNull;
import lombok.Setter;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.apache.commons.lang3.tuple.Triple;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Qualifier;
import org.springframework.stereotype.Service;
import reactor.core.publisher.Mono;
import tools.jackson.core.type.TypeReference;

@Slf4j
@Service
public class SecretFacade {

    @Data
    @Builder
    private static final class CreateSecretVersionInput {
        private final SecretVersionModel model;
        private final boolean isLWTWrite;
        private final boolean isCASRequest;
        private final UUID prevVersionIfLWTWrite;
    }

    private static final String DELETE_SECRET_VERSIONS_FAILED =
            "Failed to delete Secret Versions for namespace: '{}', entity: '{}', secret path: '{}'";
    private static final String DELETE_SECRET_PATH_BY_VERSION_FAILED = """
            Failed to delete Secret Path for namespace: '{}', entity: '{}' secret path: '{}'.
             DB might be left in an inconsistent state with dangling paths.""";

    private static final String DELETE_SECRET_VERSIONS_SUCCEEDED = """
            Deleted Secret Versions for namespace: '{}', entity: '{}' secret path: '{}',
             if they existed""";
    private static final String DELETE_SECRET_PATH_BY_VERSION_SUCCEEDED =
            "Deleted Secret Path for namespace: '{}', entity: '{}' secret path: '{}'";

    @Setter(onMethod_ = {@Autowired})
    private SecretVersionService secretVersionService;

    @Setter(onMethod_ = {@Autowired})
    private SecretPathService secretPathService;

    @Setter(onMethod_ = {@Autowired})
    private EntityService entityService;

    @Setter(onMethod_ = {@Autowired})
    private NamespaceService namespaceService;

    @Setter(onMethod_ = {@Autowired})
    private CryptoService cryptoService;

    @Setter(onMethod_ = {@Autowired})
    private CustomMetricsRegistry customMetricsRegistry;

    @Setter(onMethod_ = {@Autowired})
    @Qualifier(TelemetryComponentsImpl.BEAN_NAME)
    private TelemetryComponents telemetryComponents;

    public static class NotFoundIgnoreException extends NotFoundException {

        public NotFoundIgnoreException(String message) {
            super(message);
        }
    }

    public Mono<CreateSecretResponse> createSecret(@NonNull String namespace, @NonNull String entityType,
            @NonNull String entityId, @NonNull String path, CreateSecretRequest request) {
        String entity = EntityUtils.getEntity(entityType, entityId);
        return Mono.deferContextual(ctx -> namespaceService.getNamespaceWithValidEntityType(namespace, entityType, NotFoundException.class)
                .flatMap(namespaceModel -> {
                    // doing right after namespace fetch to avoid creating dangling data (entities)
                    if (request.getOptions() != null && request.getOptions()
                            // should not NPE, payload was validated in controller already
                            .getCas().version() != 1) {
                        // mimic error for CAS mismatch
                        telemetryComponents.setSpanAttribute(ctx,
                                SECRET_CAS_ERROR_PROVIDED_VERSION_KEY,
                                request.getOptions().getCas().toString());
                        customMetricsRegistry.recordSecretCreateCasError(namespace);
                        return Mono.error(new ConflictException(String.format(
                                Constants.MSG_SECRET_VERSION_NOT_PRESENT,
                                namespace, entity, path, request.getOptions()
                                        .getCas())));
                    }
                    return Mono.just(namespaceModel);
                })
                .flatMap(namespaceModel -> entityService
                        .createEntityIfNotExists(namespace, entityType, entityId, namespaceModel)
                        .map(ignoredEntityModel -> namespaceModel))
          .flatMap(namespaceModel ->
                  cryptoService.asyncEncryptAndGetKid(namespaceModel.getNamespace(),
                                  request.getData())
                          .map(ciphertextAndKid -> Triple.of(namespaceModel, ciphertextAndKid.getT2(), ciphertextAndKid.getT1()))

          )
          .flatMap(nsAndKidAndEncryptedSecretPayload -> {
            var casVersion = request.getOptions() != null
                ? request.getOptions().getCas()
                : null;
            return secretVersionService.validateCurrentVersionAndGenValidNewVersion(namespace, entity, path,
                    Optional.ofNullable(casVersion))
                .map(currentVersionAndNewVersion -> CreateSecretVersionInput.builder()
                        .model(
                            SecretVersionModel.builder()
                                .namespace(namespace)
                                .entity(entity)
                                .secretPath(path)
                                .value(nsAndKidAndEncryptedSecretPayload.getRight())
                                .version(currentVersionAndNewVersion.getRight())
                                .currentVersion(currentVersionAndNewVersion.getRight())
                                .createdAt(Instant.ofEpochMilli(Uuids.unixTimestamp(currentVersionAndNewVersion.getRight())))
                                .encryptedAt(Instant.ofEpochMilli(Uuids.unixTimestamp(currentVersionAndNewVersion.getRight())))
                                .encryptedByKid(nsAndKidAndEncryptedSecretPayload.getMiddle())
                                .build()
                        )
                        .isLWTWrite(
                            !Objects.isNull(casVersion) ||
                                Boolean.TRUE.equals(nsAndKidAndEncryptedSecretPayload
                                    .getLeft()
                                    .getRequireLWTForSecretVersionWrites())
                        )
                        .isCASRequest(!Objects.isNull(casVersion))
                        .prevVersionIfLWTWrite(currentVersionAndNewVersion.getLeft())
                        .build()
                );
          })
          .flatMap(createSecretVersionInput ->
            secretPathService.writeAllPathsForSecret(namespace, entity, path)
                .map(ignoredSecretPathInsertionArgs -> createSecretVersionInput)
          )
          .flatMap(createSecretVersionInput ->
            secretVersionService.createSecretVersion(
                createSecretVersionInput.getModel(),
                createSecretVersionInput.isLWTWrite(),
                createSecretVersionInput.getPrevVersionIfLWTWrite(),
                createSecretVersionInput.isCASRequest()
            )
                .map(ignoredSuccessFlag -> CreateSecretResponse.builder()
                    .data(toSecretVersionMetadata(createSecretVersionInput.getModel()))
                    .build()
                )
          )
          .onErrorMap(
              // Any upstream exception that's not a `BootResponseException` is assumed to be transient. If there's
              // an outer retry-loop with retries remaining, a retry of the entire execution-pipeline is attempted.
              // Otherwise, a 500 is returned.
              // TODO: Remove the shaded BootResponseException check when ess-encryption adds nv-exceptions
              ex -> !(ex instanceof BootResponseException)
                  && !(ex instanceof com.nvidia.ess.encryption.exceptions.shaded.BootResponseException),
              ex -> {
                // Log this error. We really shouldn't be seeing errors like these here. They should have been transformed into
                // `BootResponseException` errors upstream from this point.
                log.error("Unhandled non-BootResponseException error from upstream: {}. Rethrowing as `RetryableException`", ex.getMessage(), ex);
                return new RetryableException(new RetriesExhaustedInternalErrorException(
                    LogMessageStringUtils.errorSummary(Errors.INTERNAL_SERVER_ERROR,
                        SubErrors.UNHANDLED_UPSTREAM_ERROR, namespace, entity, path),
                    "Unhandled error", ex));
              }
          )
      );
    }

    public Mono<SecretResponse> getSecret(String namespace, String entityType, String entityId,
                                          String path, @Nullable UUID version) {
        String entity = EntityUtils.getEntity(entityType, entityId);

        return namespaceService.getNamespaceWithValidEntityType(namespace, entityType).flatMap(ignoredNs -> {
            if (version == null) {
                return secretVersionService.getSecretVersion(namespace, entity, path,
                            NotFoundException.class);
            } else {
                if (version.version() != 1) {
                    // mimic error for missing version
                    return Mono.error(() ->
                            ExceptionUtils.constructErrorResponseException(NotFoundException.class,
                                    String.format(MSG_SECRET_VERSION_NOT_FOUND, version, path,
                                            namespace, entity),
                                    ErrorSubType.SECRET_VERSION_NOT_FOUND));
                }
                return secretVersionService.getSecretVersion(namespace, entity, path, version,
                            NotFoundException.class);
            }
        }).flatMap(secretVersionModel -> Mono.zip(Mono.just(secretVersionModel),
                cryptoService.asyncDecrypt(
                                namespace, secretVersionModel.getValue(),
                                new TypeReference<HashMap<String, Object>>() {
                                })
                        // catch only BadJWEException since that is not a client error for ESS
                        .onErrorMap(BadJWEException.class, ex -> {
                            log.error("Failed to decrypt secret for namespace: '{}', entity: '{}',"
                                            + " secret path: '{}': ciphertext not a JWE string",
                                    namespace, entity, path, ex);
                            return new AnomalyException(String.format(
                                    "During secret-fetch: (namespace='%s', entity='%s',"
                                            + " secretPath='%s'), fetched secret in incorrect format",
                                    namespace, entity, path), ex);
                        })
                        .onErrorMap(MissingKeyException.class, ex -> {
                            log.error("Unable to find decryption key for namespace {}", namespace);
                            return new AnomalyException("During secret-fetch: (namespace='%s', "
                                    + "entity='%s', secretPath='%s'), fetched secret cannot be"
                                    + " decrypted. Contact support or abandon the secret.", ex);
                        })
                )
        ).map(modelAndPlaintext -> toSecretResponse(modelAndPlaintext.getT1(),
                modelAndPlaintext.getT2()));
    }

    public Mono<SecretResponse> getSecretPaths(String namespace, String entityType,
                                           String entityId, String partialPath) {
        String entity = EntityUtils.getEntity(entityType, entityId);
        Pattern pattern = childrenPathsPattern(partialPath);

        return namespaceService.getNamespaceWithValidEntityType(namespace, entityType)
                // TODO possible optimization for later: fetch only the subtree of paths rooted in the partialPath
                .flatMapMany(ignored -> secretPathService.getPaths(namespace, entity))
                // Sometimes, an entity-paths partition is empty because all its previously existing paths
                // were removed. When this happens, an all-paths-in-entity fetch can return a "row" with all
                // non-partition-key, non-static columns set to NULL. This "row" should be skipped (with
                // nothing else emanating from the fetch itself).
                .filter(path -> !Objects.isNull(path.getPath()))
                .filter(path -> pattern.matcher(path.getPath()).matches())
                .map(this::toFullSecretPath)
                // TODO if too many paths and Cassandra internally paginates, backpressure flux?
                .collectList()
                .map(this::toSecretResponse);
    }

    private Pattern childrenPathsPattern(String partialPath) {
        // if root, "/" not added to regex
        String partialPathWithDir = StringUtils.isEmpty(partialPath) ? "" : partialPath + "/";
        String regexLiteral = Pattern.quote(partialPathWithDir);

        String regex = "^" + regexLiteral + "[^/]+$";
        return Pattern.compile(regex);
    }

    public Mono<Void> deleteSecret(@NonNull String namespace, @NonNull String entityType, @NonNull String entityId,
                                   @NonNull String path) {
        String entity = EntityUtils.getEntity(entityType, entityId);

        return Mono.deferContextual(ctx ->
            namespaceService.getNamespaceWithValidEntityType(namespace, entityType, NotFoundIgnoreException.class)
                .flatMap(namespaceModel ->
                        secretVersionService
                                .deleteSecretVersions(namespace, entity, path)
                                .doOnError(e -> log.error(DELETE_SECRET_VERSIONS_FAILED, namespace, entity, path, e))
                                .doOnSuccess(ignored -> log.debug(DELETE_SECRET_VERSIONS_SUCCEEDED,
                                        namespace, entity, path))
                )
                .flatMap(ignored ->
                        secretPathService
                                .deleteSecretPathAndEmptyAncestorDirs(namespace, entity, path)
                                .doOnSuccess(deleted -> {
                                    if (Boolean.TRUE.equals(deleted)) {
                                        log.debug(DELETE_SECRET_PATH_BY_VERSION_SUCCEEDED, namespace, entity, path);
                                    } else {
                                        telemetryComponents.setSpanAttribute(ctx,
                                                PARTIAL_DELETE_TYPE_KEY,
                                                PartialDeleteType.SECRET_PATH_CAS_ON_SECRET.name());
                                        telemetryComponents.setSpanAttribute(ctx,
                                                LWT_WRITE_FAILURE_OPERATION_KEY,
                                                LwtOperation.PATH_DELETION.name());
                                        customMetricsRegistry.recordNonRetryableLwtFailure(namespace,
                                                LwtOperation.PATH_DELETION);
                                        customMetricsRegistry.recordPartialSecretDeletionOnPath(
                                                namespace);
                                        log.error(DELETE_SECRET_PATH_BY_VERSION_FAILED, namespace,
                                                entity, path);
                                    }
                                })
                                .onErrorResume(e -> {
                                    telemetryComponents.setSpanAttribute(ctx, PARTIAL_DELETE_TYPE_KEY,
                                            PartialDeleteType.SECRET_PATH_ON_SECRET.name());
                                    customMetricsRegistry.recordPartialSecretDeletionOnPath(namespace);
                                    log.error(DELETE_SECRET_PATH_BY_VERSION_FAILED, namespace, entity, path, e);
                                    return Mono.just(false); // tolerate error in secretPath deletion
                                })
                )
                // catch all to return 200 instead of 404 for Not Found
                .onErrorResume(NotFoundIgnoreException.class, e -> Mono.empty()).then()
        );
    }

    public Mono<SecretResponse> getSecretVersions(String namespace, String entityType, String entityId, String path) {
        String entity = EntityUtils.getEntity(entityType, entityId);

        return namespaceService.getNamespaceWithValidEntityType(namespace, entityType)
                .flatMapMany(namespaceModel -> secretVersionService.getSecretVersions(namespace, entity, path))
                .concatMap(secretVersionModel -> Mono.just(secretVersionModel.getVersion().toString()))
                .collectList()
                .map(keys -> SecretResponse.builder()
                        .data(SecretInfo.builder()
                                .keys(keys)
                                .build())
                        .build());
    }

    public SecretResponse toSecretResponse(SecretVersionModel model, Map<String, Object> decryptedData) {
        return SecretResponse.builder()
                .data(SecretInfo.builder()
                        .metadata(toSecretVersionMetadata(model))
                        .data(decryptedData)
                        .build())
                .build();
    }

    private SecretResponse toSecretResponse(List<String> paths) {
        return SecretResponse.builder()
                .data(SecretInfo.builder()
                        .keys(paths)
                        .build())
                .build();
    }

    public SecretVersionMetadata toSecretVersionMetadata(SecretVersionModel model) {
        return SecretVersionMetadata.builder()
                .version(model.getVersion())
                .createdTime(model.getCreatedAt())
                .build();
    }

    private String toFullSecretPath(SecretPathModel secretPathModel) {
        if (Boolean.TRUE.equals(secretPathModel.getIsDir())) {
            return String.format("%s/%s/", secretPathModel.getEntity(), secretPathModel.getPath());
        }
        return String.format("%s/%s", secretPathModel.getEntity(), secretPathModel.getPath());
    }


}
