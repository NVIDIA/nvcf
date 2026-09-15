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
package com.nvidia.nvcf.grpc;

import static com.nvidia.nvcf.persistence.function.entity.FunctionStatus.BUSY_STATUSES;
import static com.nvidia.nvcf.service.token.GrpcTokenService.NvcfIssuedToken.VALIDITY;
import static com.nvidia.nvcf.util.NvcfConstants.SPAN_TAG_FUNCTION_ID;
import static com.nvidia.nvcf.util.NvcfConstants.SPAN_TAG_FUNCTION_VERSION_ID;
import static com.nvidia.nvcf.util.NvcfConstants.SPAN_TAG_INSTANCE_ID;
import static com.nvidia.nvcf.util.NvcfConstants.SPAN_TAG_INVOCATION_REQUEST_ID;
import static com.nvidia.nvcf.util.NvcfConstants.SPAN_TAG_ISSUED_AT;
import static com.nvidia.nvcf.util.NvcfConstants.SPAN_TAG_REGION;
import static com.nvidia.nvcf.util.ProtoMappingUtils.toTimestamp;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.UnauthorizedException;
import com.nvidia.nvcf.configuration.AwsConfiguration.AwsProperties;
import com.nvidia.nvcf.configuration.nats.NatsConfiguration.NatsProperties;
import com.nvidia.nvcf.icms.client.IcmsClient;
import com.nvidia.nvcf.persistence.function.entity.FunctionType;
import com.nvidia.nvcf.service.token.WorkerTokenIntrospectionService;
import com.nvidia.nvcf.proto.ArtifactsRequest;
import com.nvidia.nvcf.proto.ArtifactsResponse;
import com.nvidia.nvcf.proto.ArtifactsResponse.ArtifactResponse;
import com.nvidia.nvcf.proto.ArtifactsResponse.ArtifactResponse.ArtifactFile;
import com.nvidia.nvcf.proto.ArtifactsResponse.ArtifactResponse.ArtifactKindEnum;
import com.nvidia.nvcf.proto.FunctionMetadataCredentialsRequest;
import com.nvidia.nvcf.proto.FunctionMetadataCredentialsResponse;
import com.nvidia.nvcf.proto.InputAssetReference;
import com.nvidia.nvcf.proto.InstanceCredentialsRequest;
import com.nvidia.nvcf.proto.InstanceCredentialsResponse;
import com.nvidia.nvcf.proto.LargeResponseDownloadCredentialsRequest;
import com.nvidia.nvcf.proto.LargeResponseDownloadCredentialsResponse;
import com.nvidia.nvcf.proto.MultipartLargeUploadCredentialsRequest;
import com.nvidia.nvcf.proto.MultipartLargeUploadCredentialsResponse;
import com.nvidia.nvcf.proto.ProvisionWorkerRequest;
import com.nvidia.nvcf.proto.ProvisionWorkerResponse;
import com.nvidia.nvcf.proto.RefreshAssetDownloadCredentialsRequest;
import com.nvidia.nvcf.proto.RefreshAssetDownloadCredentialsResponse;
import com.nvidia.nvcf.proto.RefreshLargeUploadCredentialsRequest;
import com.nvidia.nvcf.proto.RefreshLargeUploadCredentialsResponse;
import com.nvidia.nvcf.proto.SecretCredentialsRequest;
import com.nvidia.nvcf.proto.SecretCredentialsResponse;
import com.nvidia.nvcf.proto.StreamedArtifactFile;
import com.nvidia.nvcf.proto.WorkerConnect;
import com.nvidia.nvcf.proto.WorkerConnectOnceResponse;
import com.nvidia.nvcf.proto.WorkerGrpc.WorkerImplBase;
import com.nvidia.nvcf.s3.MultipartUploadService;
import com.nvidia.nvcf.s3.S3PreSignedUrlGenerator;
import com.nvidia.nvcf.service.function.FunctionLookupService;
import com.nvidia.nvcf.service.function.invocation.WorkerUrlGeneratorService;
import com.nvidia.nvcf.service.registry.RegistryArtifactService;
import com.nvidia.nvcf.service.token.GrpcTokenService;
import com.nvidia.nvcf.service.token.GrpcTokenService.NvcfIssuedToken;
import com.nvidia.nvcf.service.token.GrpcTokenService.NvcfIssuedToken.TokenType;
import com.nvidia.nvcf.service.worker.WorkerNatsService;
import com.nvidia.nvcf.service.worker.WorkerNotaryService;
import com.nvidia.nvcf.util.NvcfUtils;
import io.grpc.stub.StreamObserver;
import io.micrometer.tracing.Tracer;
import java.time.Instant;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.UUID;
import java.util.stream.Stream;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import net.devh.boot.grpc.server.service.GrpcService;
import org.apache.commons.lang3.StringUtils;
import org.springframework.security.core.context.SecurityContextHolder;

@Slf4j
@GrpcService
@RequiredArgsConstructor
public class GrpcWorkerService extends WorkerImplBase {
    private static final String MESG_NOT_SUPPORTED_ARTIFACT_TYPE =
            "Function id '%s', version id '%s': Received unsupported artifact type '%s'";

    private final Tracer tracer;
    private final GrpcTokenService grpcTokenService;
    private final MultipartUploadService multipartUploadService;
    private final NatsProperties natsProperties;
    private final WorkerNotaryService workerNotaryService;
    private final AwsProperties awsProperties;
    private final S3PreSignedUrlGenerator preSignedUrlGenerator;
    private final FunctionLookupService functionLookupService;
    private final WorkerNatsService workerNatsService;
    private final RegistryArtifactService artifactService;
    private final WorkerUrlGeneratorService workerUrlGeneratorService;
    private final IcmsClient icmsClient;
    private final WorkerTokenIntrospectionService workerTokenIntrospectionService;

    @Override
    public void connectOnce(
            WorkerConnect request,
            StreamObserver<WorkerConnectOnceResponse> responseObserver) {
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(
                SPAN_TAG_FUNCTION_ID, request.getFunctionId(),
                SPAN_TAG_FUNCTION_VERSION_ID, request.getFunctionVersionId(),
                SPAN_TAG_INSTANCE_ID, request.getInstanceId()));
        var functionId = UUID.fromString(request.getFunctionId());
        var functionVersionId = UUID.fromString(request.getFunctionVersionId());
        var validated = validateWorkerToken(functionId, functionVersionId);
        var workerToken = validated.token();
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(
                SPAN_TAG_ISSUED_AT, workerToken.issuedAt().toString()));
        var otherRegions =
                natsProperties.getSecondaryRegions() != null ? natsProperties.getSecondaryRegions()
                        : List.<String>of();
        // validate function or throw
        var function = functionLookupService.lookupUsingFunctionIdAndVersionId(functionId,
                                                                               functionVersionId)
                // filter out inactive functions. don't let zombie workers keep connecting.
                .filter(entity -> BUSY_STATUSES.contains(entity.getFunctionStatus()))
                .orElseThrow(() -> new ForbiddenException(
                        "function " + functionId + " version " + functionVersionId
                                + " is not active"));

        // LLM workers do not consume from JetStream request queues.
        if (function.getFunctionType() != FunctionType.LLM) {
            Stream.concat(Stream.of(natsProperties.getRegion()), otherRegions.stream())
                    .forEach(region -> workerNatsService.createOrExtendQueues(
                            region, functionVersionId));
        }

        var response = WorkerConnectOnceResponse.newBuilder()
                .setConnectedRegion(natsProperties.getRegion())
                .addAllOtherRegions(otherRegions);
        if (validated.delegated()) {
            // The mounted PSAT stays the worker's credential; no NVCF-issued token replaces it.
            response.setExpiration(toTimestamp(validated.expiresAt()));
        } else {
            response.setNvcfWorkerToken(grpcTokenService.issueToken(
                            functionId, functionVersionId, TokenType.WORKER))
                    .setExpiration(toTimestamp(Instant.now().plus(VALIDITY)));
        }
        responseObserver.onNext(response.build());
        responseObserver.onCompleted();
    }

    @Override
    @Deprecated
    public void requestArtifacts(
            ArtifactsRequest request,
            StreamObserver<ArtifactsResponse> responseObserver) {
        var nvcfIssuedToken = grpcTokenService.validateToken(getToken(), TokenType.WORKER);
        var functionId = nvcfIssuedToken.functionId();
        var functionVersionId = nvcfIssuedToken.functionVersionId();

        var artifacts = artifactService.fetchArtifacts(functionId, functionVersionId);
        var artifactResponses = artifacts.stream()
                .map(artifact -> {
                    var allFiles = artifact.files().stream()
                            .map(file -> ArtifactFile.newBuilder()
                                    .setUrl(file.url())
                                    .setPath(Objects.requireNonNullElse(file.path(), ""))
                                    .build())
                            .toList();

                    var kind = switch (artifact.artifactType()) {
                        case MODEL -> ArtifactKindEnum.MODEL;
                        case RESOURCE -> ArtifactKindEnum.RESOURCE;
                        default -> {
                            var errorMsg = MESG_NOT_SUPPORTED_ARTIFACT_TYPE.formatted(
                                    functionId, functionVersionId, artifact.artifactType());
                            log.error(errorMsg);
                            throw new IllegalStateException(errorMsg);
                        }
                    };

                    return ArtifactResponse.newBuilder()
                            .setName(artifact.name())
                            .setVersion(artifact.version())
                            .setKind(kind)
                            .addAllFiles(allFiles)
                            .build();
                })
                .toList();
        responseObserver.onNext(ArtifactsResponse.newBuilder()
                                        .addAllArtifacts(artifactResponses)
                                        .build());
        responseObserver.onCompleted();
    }

    @Override
    public void streamArtifacts(
            ArtifactsRequest request,
            StreamObserver<StreamedArtifactFile> responseObserver) {
        var nvcfIssuedToken = grpcTokenService.validateToken(getToken(), TokenType.WORKER);
        var functionId = nvcfIssuedToken.functionId();
        var functionVersionId = nvcfIssuedToken.functionVersionId();

        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(
                SPAN_TAG_FUNCTION_ID, functionId.toString(),
                SPAN_TAG_FUNCTION_VERSION_ID, functionVersionId.toString()));

        // Stream each artifact file individually
        artifactService.fetchArtifacts(functionId, functionVersionId).stream()
                .flatMap(artifact -> {
                    var kind = switch (artifact.artifactType()) {
                        case MODEL -> StreamedArtifactFile.ArtifactKindEnum.MODEL;
                        case RESOURCE -> StreamedArtifactFile.ArtifactKindEnum.RESOURCE;
                        default -> {
                            var errorMsg = MESG_NOT_SUPPORTED_ARTIFACT_TYPE.formatted(
                                    functionId, functionVersionId, artifact.artifactType());
                            log.error(errorMsg);
                            throw new IllegalStateException(errorMsg);
                        }
                    };

                    return artifact.files().stream()
                            .map(file -> StreamedArtifactFile.newBuilder()
                                    .setArtifactName(artifact.name())
                                    .setArtifactVersion(artifact.version())
                                    .setArtifactKind(kind)
                                    .setPath(Objects.requireNonNullElse(file.path(), ""))
                                    .setUrl(file.url())
                                    .build());
                })
                .forEach(streamedArtifactFile -> {
                    responseObserver.onNext(streamedArtifactFile);
                });
        
        responseObserver.onCompleted();
        log.debug("Successfully streamed artifact files for function {} version {}", 
                functionId, functionVersionId);
    }

    /**
     * A worker identity accepted for the requested function version.
     *
     * @param token     the function-bound identity; for delegated tokens it is built from the
     *                  ICMS-resolved binding, never from the request
     * @param delegated true when the worker authenticated with a mounted PSAT
     * @param expiresAt expiry of the presented credential
     */
    record ValidatedWorker(NvcfIssuedToken token, boolean delegated, Instant expiresAt) {
    }

    private ValidatedWorker validateWorkerToken(UUID functionId, UUID functionVersionId) {
        return validateWorkerToken(getToken(), functionId, functionVersionId);
    }

    /**
     * Selects the legacy or delegated path by token shape. A legacy JWE that does not match the
     * requested function is rejected outright and is never retried through introspection; a
     * delegated PSAT is accepted only when ICMS reports it active and bound to exactly the
     * requested function version.
     */
    ValidatedWorker validateWorkerToken(String token, UUID functionId, UUID functionVersionId) {
        if (!WorkerTokenIntrospectionService.isDelegatedToken(token)) {
            var nvcfIssuedToken = grpcTokenService.validateToken(token, TokenType.WORKER);
            if (nvcfIssuedToken.functionId().equals(functionId)
                    && nvcfIssuedToken.functionVersionId().equals(functionVersionId)) {
                return new ValidatedWorker(nvcfIssuedToken, false,
                                           nvcfIssuedToken.issuedAt().plus(VALIDITY));
            }
            throw new ForbiddenException("invalid worker token");
        }

        if (!workerTokenIntrospectionService.isEnabled()) {
            throw new ForbiddenException("delegated worker tokens are not enabled");
        }
        var result = workerTokenIntrospectionService.introspect(token);
        if (!result.isActive()) {
            log.warn("worker token introspection returned active=false: {}", result.getError());
            throw new ForbiddenException("worker token not active");
        }
        UUID boundFunctionId;
        UUID boundFunctionVersionId;
        try {
            boundFunctionId = UUID.fromString(result.getFunctionId());
            boundFunctionVersionId = UUID.fromString(result.getFunctionVersionId());
        } catch (IllegalArgumentException | NullPointerException e) {
            log.warn("worker token introspection returned malformed binding, instance_id={}",
                     result.getInstanceId());
            throw new ForbiddenException("worker token not active");
        }
        if (!boundFunctionId.equals(functionId)
                || !boundFunctionVersionId.equals(functionVersionId)) {
            log.warn("delegated worker token bound to function {} version {} used for function {} "
                             + "version {}, instance_id={}", boundFunctionId,
                     boundFunctionVersionId, functionId, functionVersionId,
                     result.getInstanceId());
            throw new ForbiddenException("worker token not bound to requested workload");
        }
        log.debug("worker authorized via delegated token, instance_id={} cluster_id={}",
                  result.getInstanceId(), result.getClientId());
        var expiresAt = Instant.ofEpochSecond(result.getExp());
        return new ValidatedWorker(
                new NvcfIssuedToken(boundFunctionId, boundFunctionVersionId, Instant.now(),
                                    TokenType.WORKER),
                true, expiresAt);
    }

    private static String getToken() {
        var authentication = SecurityContextHolder.getContext().getAuthentication();
        if (authentication == null || !(authentication.getCredentials() instanceof String token)) {
            throw new UnauthorizedException("missing token");
        }
        return token;
    }

    @Override
    public void refreshLargeUploadCredentials(
            RefreshLargeUploadCredentialsRequest request,
            StreamObserver<RefreshLargeUploadCredentialsResponse> responseObserver) {
        var functionId = UUID.fromString(request.getFunctionId());
        var functionVersionId = UUID.fromString(request.getFunctionVersionId());
        var requestId = UUID.fromString(request.getRequestId());
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(
                SPAN_TAG_FUNCTION_ID, functionId.toString(),
                SPAN_TAG_FUNCTION_VERSION_ID, functionVersionId.toString(),
                SPAN_TAG_INVOCATION_REQUEST_ID, request.getRequestId()));
        validateWorkerToken(functionId, functionVersionId);

        var invocationRequest = workerNatsService.lookupFunctionInvocationRequest(
                functionVersionId,
                requestId);
        var url = workerUrlGeneratorService.getPreSignedUploadUrlForLargeResults(
                requestId, invocationRequest.getNcaId());
        responseObserver.onNext(RefreshLargeUploadCredentialsResponse.newBuilder()
                                        .setLargeResponseUrl(url.toString())
                                        .build());
        responseObserver.onCompleted();
    }

    @Override
    public StreamObserver<RefreshAssetDownloadCredentialsRequest> refreshAssetDownloadCredentials(
            StreamObserver<RefreshAssetDownloadCredentialsResponse> responseObserver) {
        // The function binding arrives with the first streamed message, so capture the bearer
        // token now (the security context is not available on the stream thread) and validate
        // it against that binding on first use. Both legacy and delegated tokens take this path.
        var token = getToken();
        return new RefreshAssetDownloadCredentialsRequestStreamObserver(
                (functionId, functionVersionId) ->
                        validateWorkerToken(token, functionId, functionVersionId).token(),
                responseObserver,
                workerNatsService,
                workerUrlGeneratorService);
    }

    @FunctionalInterface
    interface WorkerTokenValidator {
        NvcfIssuedToken validate(UUID functionId, UUID functionVersionId);
    }

    @RequiredArgsConstructor
    private static class RefreshAssetDownloadCredentialsRequestStreamObserver implements
            StreamObserver<RefreshAssetDownloadCredentialsRequest> {

        private String ncaId;
        private final WorkerTokenValidator tokenValidator;
        private final StreamObserver<RefreshAssetDownloadCredentialsResponse> responseObserver;
        private final WorkerNatsService workerNatsService;
        private final WorkerUrlGeneratorService workerUrlGeneratorService;

        @Override
        public void onNext(RefreshAssetDownloadCredentialsRequest request) {
            if (ncaId == null) {
                var firstRequestId = UUID.fromString(request.getRequestId());
                var functionId = UUID.fromString(request.getFunctionId());
                var functionVersionId = UUID.fromString(request.getFunctionVersionId());
                tokenValidator.validate(functionId, functionVersionId);
                var invocationRequest = workerNatsService.lookupFunctionInvocationRequest(
                        UUID.fromString(request.getFunctionVersionId()), firstRequestId);

                ncaId = invocationRequest.getNcaId();
            }
            var assetId = UUID.fromString(request.getAssetId());
            var asset = workerUrlGeneratorService.toAssetReferenceDto(assetId, ncaId);
            var response = RefreshAssetDownloadCredentialsResponse.newBuilder()
                    .setInputAssetReference(InputAssetReference.newBuilder()
                                                    .setAssetId(asset.assetId().toString())
                                                    .setContentType(asset.contentType())
                                                    .setReference(asset.reference().toString()))
                    .build();
            responseObserver.onNext(response);
        }

        @Override
        public void onError(Throwable t) {
            responseObserver.onError(t);
        }

        @Override
        public void onCompleted() {
            responseObserver.onCompleted();
        }
    }

    @Override
    public void multipartLargeUploadCredentials(
            MultipartLargeUploadCredentialsRequest request,
            StreamObserver<MultipartLargeUploadCredentialsResponse> responseObserver) {
        var functionId = UUID.fromString(request.getFunctionId());
        var functionVersionId = UUID.fromString(request.getFunctionVersionId());
        var requestId = UUID.fromString(request.getRequestId());
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(
                SPAN_TAG_FUNCTION_ID, functionId.toString(),
                SPAN_TAG_FUNCTION_VERSION_ID, functionVersionId.toString(),
                SPAN_TAG_INVOCATION_REQUEST_ID, request.getRequestId()));
        validateWorkerToken(functionId, functionVersionId);

        var invocationRequest = workerNatsService.lookupFunctionInvocationRequest(
                functionVersionId, requestId);
        var response = multipartUploadService.issueCredentials(invocationRequest.getNcaId(),
                                                               requestId);
        responseObserver.onNext(response);
        responseObserver.onCompleted();
    }

    @Override
    public void requestInstanceCredentials(
            InstanceCredentialsRequest request,
            StreamObserver<InstanceCredentialsResponse> responseObserver) {
        var functionId = UUID.fromString(request.getFunctionId());
        var functionVersionId = UUID.fromString(request.getFunctionVersionId());
        validateWorkerToken(functionId, functionVersionId);
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(
                SPAN_TAG_FUNCTION_ID, functionId.toString(),
                SPAN_TAG_FUNCTION_VERSION_ID, functionVersionId.toString()));

        var instance = icmsClient.getInstanceById(request.getInstanceId());
        var assertionToken = workerNotaryService.validateAndIssueInstanceAssertion(functionId,
                                                                                   functionVersionId,
                                                                                   instance,
                                                                                   request.getIpsList());
        responseObserver.onNext(InstanceCredentialsResponse.newBuilder()
                                        .setInstanceCredentialsToken(assertionToken)
                                        .setExpiration(toTimestamp(Instant.now().plus(VALIDITY)))
                                        .build());
        responseObserver.onCompleted();
    }

    @Override
    public void requestSecretCredentials(
            SecretCredentialsRequest request,
            StreamObserver<SecretCredentialsResponse> responseObserver) {
        var functionId = UUID.fromString(request.getFunctionId());
        var functionVersionId = UUID.fromString(request.getFunctionVersionId());
        validateWorkerToken(functionId, functionVersionId);
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(
                SPAN_TAG_FUNCTION_ID, functionId.toString(),
                SPAN_TAG_FUNCTION_VERSION_ID, functionVersionId.toString()));
        var assertionToken = workerNotaryService.issueSecretsAssertion(functionId,
                                                                       functionVersionId);
        responseObserver.onNext(SecretCredentialsResponse.newBuilder()
                                        .setSecretCredentialsToken(assertionToken)
                                        .setExpiration(toTimestamp(Instant.now().plus(VALIDITY)))
                                        .build());
        responseObserver.onCompleted();
    }

    @Override
    public void requestFunctionMetadataCredentials(
            FunctionMetadataCredentialsRequest request,
            StreamObserver<FunctionMetadataCredentialsResponse> responseObserver) {
        var ncaId = request.getNcaId();
        var functionId = UUID.fromString(request.getFunctionId());
        var functionVersionId = UUID.fromString(request.getFunctionVersionId());
        validateWorkerToken(functionId, functionVersionId);
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(
                SPAN_TAG_FUNCTION_ID, functionId.toString(),
                SPAN_TAG_FUNCTION_VERSION_ID, functionVersionId.toString()));
        var assertionToken = workerNotaryService.issueFunctionMetadataAssertion(ncaId, functionId,
                                                                                functionVersionId);
        responseObserver.onNext(FunctionMetadataCredentialsResponse.newBuilder()
                                        .setFunctionMetadataCredentialsToken(assertionToken)
                                        .setExpiration(toTimestamp(Instant.now().plus(VALIDITY)))
                                        .build());
        responseObserver.onCompleted();
    }

    /**
     * workers need a request stream, a request stream consumer, and a response stream in each
     * region. if they call this method, that means one of those was missing, and we should create
     * those resources for them.
     * <p>
     * even if we don't know about the region we should assume the worker was told about this
     * region from a more up to date NVCF api, so try to provision it anyway.
     */
    @Override
    public void provisionRegionalWorker(
            ProvisionWorkerRequest request,
            StreamObserver<ProvisionWorkerResponse> responseObserver) {
        var functionId = UUID.fromString(request.getFunctionId());
        var functionVersionId = UUID.fromString(request.getFunctionVersionId());
        validateWorkerToken(functionId, functionVersionId);
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(
                SPAN_TAG_FUNCTION_ID, functionId.toString(),
                SPAN_TAG_FUNCTION_VERSION_ID, functionVersionId.toString(),
                SPAN_TAG_REGION, request.getRegionToProvision()));
        if (StringUtils.isBlank(request.getRegionToProvision())) {
            throw new BadRequestException("blank region to provision");
        }
        workerNatsService.createConsumer(request.getRegionToProvision(), functionVersionId);
        responseObserver.onNext(ProvisionWorkerResponse.getDefaultInstance());
        responseObserver.onCompleted();
    }

    @Override
    public void requestLargeResponseDownloadCredentials(
            LargeResponseDownloadCredentialsRequest request,
            StreamObserver<LargeResponseDownloadCredentialsResponse> responseObserver) {
        var requestId = UUID.fromString(request.getRequestId());
        var functionId = UUID.fromString(request.getFunctionId());
        var functionVersionId = UUID.fromString(request.getFunctionVersionId());
        validateWorkerToken(functionId, functionVersionId);
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(
                SPAN_TAG_FUNCTION_ID, functionId.toString(),
                SPAN_TAG_FUNCTION_VERSION_ID, functionVersionId.toString()));
        var bucketName = awsProperties.getS3().getResults().getBucketName();
        var url = preSignedUrlGenerator.downloadUrl(requestId, bucketName, request.getNcaId());
        responseObserver.onNext(LargeResponseDownloadCredentialsResponse.newBuilder()
                                        .setLargeResponseDownloadUrl(url.toString())
                                        .build());
        responseObserver.onCompleted();
    }

}
