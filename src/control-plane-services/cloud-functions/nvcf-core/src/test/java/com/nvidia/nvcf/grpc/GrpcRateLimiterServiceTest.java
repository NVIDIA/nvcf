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

import static com.nvidia.nvcf.IntegrationTestConfiguration.MOCK_OAUTH2_TOKEN_SERVER;
import static com.nvidia.nvcf.util.TestConstants.MD_KEY_AUTHORIZATION;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;

import com.nvidia.nvcf.persistence.function.entity.RateLimitUdt;
import com.nvidia.nvcf.proto.RateLimitGrpc;
import com.nvidia.nvcf.proto.RateLimitPolicyRequest;
import com.nvidia.nvcf.rest.function.invocation.BaseFunctionInvocationTest;
import io.grpc.ManagedChannelBuilder;
import io.grpc.Metadata;
import io.grpc.StatusRuntimeException;
import io.grpc.stub.MetadataUtils;
import java.util.Map;
import java.util.Set;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.Test;

@Slf4j
class GrpcRateLimiterServiceTest extends BaseFunctionInvocationTest {

    @Test
    void getRateLimitConfigBadScope() {
        var functionId = TEST_FUNCTION_ID;
        var versionId = TEST_VERSION_ID_1;

        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt();
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var rateLimitBlockingStub = RateLimitGrpc.newBlockingStub(channel);
        var request = RateLimitPolicyRequest.newBuilder()
                .setFunctionId(functionId.toString())
                .setFunctionVersionId(versionId.toString())
                .build();

        assertThatThrownBy(() -> rateLimitBlockingStub.rateLimitPolicy(request))
                .isInstanceOf(StatusRuntimeException.class)
                .hasMessageContaining("PERMISSION_DENIED");
        channel.shutdownNow();
    }

    @Test
    void getEmptyRateLimitConfig() {
        var functionId = TEST_FUNCTION_ID;
        var versionId = TEST_VERSION_ID_1;

        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt("ratelimit:check_invocation");
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var rateLimitBlockingStub = RateLimitGrpc.newBlockingStub(channel);
        var request = RateLimitPolicyRequest.newBuilder()
                .setFunctionId(functionId.toString())
                .setFunctionVersionId(versionId.toString())
                .build();
        assertThatThrownBy(() -> rateLimitBlockingStub.rateLimitPolicy(request))
                .isInstanceOf(StatusRuntimeException.class)
                .hasMessageContaining("does not have a rate limit policy");
        channel.shutdownNow();
    }

    @Test
    void getRateLimitConfig() {
        var functionId = TEST_FUNCTION_ID;
        var versionId = TEST_VERSION_ID_1;
        setFunctionRateLimit(functionId,
                             versionId,
                             RateLimitUdt.builder()
                                     .rate("4-S")
                                     .exemptedNcaIds(Set.of(TEST_NCA_ID))
                                     .build());

        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt("ratelimit:check_invocation");
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var rateLimitBlockingStub = RateLimitGrpc.newBlockingStub(channel);
        var request = RateLimitPolicyRequest.newBuilder()
                .setFunctionId(functionId.toString())
                .setFunctionVersionId(versionId.toString())
                .build();
        var response = rateLimitBlockingStub.rateLimitPolicy(request);
        assertThat(response).isNotNull();
        var config = response.getConfig();
        assertThat(config).isNotNull();
        assertThat(config.getRate()).isEqualTo("4-S");
        assertThat(config.getExcludedNcaIdsCount()).isEqualTo(1);
        assertThat(config.getExcludedNcaIds(0)).isEqualTo(TEST_NCA_ID);
        assertThat(config.getPerNcaIdConfigsCount()).isEqualTo(0);
        channel.shutdownNow();
    }

    @Test
    void getRateLimitConfigWithPerNcaIdConfig() {
        var functionId = TEST_FUNCTION_ID;
        var versionId = TEST_VERSION_ID_1;
        setFunctionRateLimit(functionId,
                             versionId,
                             RateLimitUdt.builder()
                                     .rate("4-S")
                                     .exemptedNcaIds(Set.of(TEST_NCA_ID))
                                     .perNcaIdRate(Map.of(
                                             TEST_NCA_ID_2, "3-M"))
                                     .build());

        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt("ratelimit:check_invocation");
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var rateLimitBlockingStub = RateLimitGrpc.newBlockingStub(channel);
        var request = RateLimitPolicyRequest.newBuilder()
                .setFunctionId(functionId.toString())
                .setFunctionVersionId(versionId.toString())
                .build();
        var response = rateLimitBlockingStub.rateLimitPolicy(request);
        assertThat(response).isNotNull();
        var config = response.getConfig();
        assertThat(config).isNotNull();
        assertThat(config.getRate()).isEqualTo("4-S");
        assertThat(config.getExcludedNcaIdsCount()).isEqualTo(1);
        assertThat(config.getExcludedNcaIds(0)).isEqualTo(TEST_NCA_ID);
        assertThat(config.getPerNcaIdConfigsCount()).isEqualTo(1);
        assertThat(config.getPerNcaIdConfigs(0)).isNotNull();
        assertThat(config.getPerNcaIdConfigs(0).getNcaId()).isEqualTo(TEST_NCA_ID_2);
        assertThat(config.getPerNcaIdConfigs(0).getRate()).isEqualTo("3-M");
        channel.shutdownNow();
    }

    @Test
    void getRateLimitConfigWithOnlyPerNcaIdConfig() {
        var functionId = TEST_FUNCTION_ID;
        var versionId = TEST_VERSION_ID_1;
        setFunctionRateLimit(functionId,
                             versionId,
                             RateLimitUdt.builder()
                                     .perNcaIdRate(Map.of(
                                             TEST_NCA_ID_2, "3-M"))
                                     .build());

        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt("ratelimit:check_invocation");
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var rateLimitBlockingStub = RateLimitGrpc.newBlockingStub(channel);
        var request = RateLimitPolicyRequest.newBuilder()
                .setFunctionId(functionId.toString())
                .setFunctionVersionId(versionId.toString())
                .build();
        var response = rateLimitBlockingStub.rateLimitPolicy(request);
        assertThat(response).isNotNull();
        var config = response.getConfig();
        assertThat(config).isNotNull();
        assertThat(config.getRate()).isEmpty();
        assertThat(config.getExcludedNcaIdsCount()).isEqualTo(0);
        assertThat(config.getPerNcaIdConfigsCount()).isEqualTo(1);
        assertThat(config.getPerNcaIdConfigs(0)).isNotNull();
        assertThat(config.getPerNcaIdConfigs(0).getNcaId()).isEqualTo(TEST_NCA_ID_2);
        assertThat(config.getPerNcaIdConfigs(0).getRate()).isEqualTo("3-M");
        channel.shutdownNow();
    }

    @Test
    void getRateLimitConfigWithPerUserRate() {
        var functionId = TEST_FUNCTION_ID;
        var versionId = TEST_VERSION_ID_1;
        setFunctionRateLimit(functionId,
                             versionId,
                             RateLimitUdt.builder()
                                     .rate("10-S")
                                     .perUserRate("2-S")
                                     .build());

        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt("ratelimit:check_invocation");
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var rateLimitBlockingStub = RateLimitGrpc.newBlockingStub(channel);
        var request = RateLimitPolicyRequest.newBuilder()
                .setFunctionId(functionId.toString())
                .setFunctionVersionId(versionId.toString())
                .build();
        var response = rateLimitBlockingStub.rateLimitPolicy(request);
        assertThat(response).isNotNull();
        var config = response.getConfig();
        assertThat(config).isNotNull();
        assertThat(config.getRate()).isEqualTo("10-S");
        assertThat(config.hasPerUserRate()).isTrue();
        assertThat(config.getPerUserRate()).isEqualTo("2-S");
        channel.shutdownNow();
    }

    @Test
    void getRateLimitConfigWithOnlyPerUserRate() {
        var functionId = TEST_FUNCTION_ID;
        var versionId = TEST_VERSION_ID_1;
        setFunctionRateLimit(functionId,
                             versionId,
                             RateLimitUdt.builder()
                                     .perUserRate("2-S")
                                     .build());

        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt("ratelimit:check_invocation");
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var rateLimitBlockingStub = RateLimitGrpc.newBlockingStub(channel);
        var request = RateLimitPolicyRequest.newBuilder()
                .setFunctionId(functionId.toString())
                .setFunctionVersionId(versionId.toString())
                .build();
        var response = rateLimitBlockingStub.rateLimitPolicy(request);
        assertThat(response).isNotNull();
        var config = response.getConfig();
        assertThat(config).isNotNull();
        assertThat(config.hasRate()).isFalse();
        assertThat(config.getPerNcaIdConfigsCount()).isEqualTo(0);
        assertThat(config.hasPerUserRate()).isTrue();
        assertThat(config.getPerUserRate()).isEqualTo("2-S");
        channel.shutdownNow();
    }
}
