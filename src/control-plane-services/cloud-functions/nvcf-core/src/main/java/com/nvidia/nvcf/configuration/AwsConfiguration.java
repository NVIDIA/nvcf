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
package com.nvidia.nvcf.configuration;

import static java.lang.String.format;

import com.nvidia.nvcf.util.NvcfUtils;
import jakarta.annotation.PostConstruct;
import java.net.URI;
import java.time.Duration;
import java.util.Arrays;
import lombok.Data;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.boot.context.properties.ConfigurationProperties;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.core.env.Environment;
import software.amazon.awssdk.auth.credentials.AwsCredentialsProvider;
import software.amazon.awssdk.auth.credentials.StaticCredentialsProvider;
import software.amazon.awssdk.auth.credentials.WebIdentityTokenFileCredentialsProvider;
import software.amazon.awssdk.awscore.client.builder.AwsClientBuilder;
import software.amazon.awssdk.regions.Region;
import software.amazon.awssdk.services.s3.S3AsyncClient;
import software.amazon.awssdk.services.s3.S3Client;
import software.amazon.awssdk.services.s3.S3Configuration;
import software.amazon.awssdk.services.s3.presigner.S3Presigner;
import software.amazon.awssdk.services.sts.StsAsyncClient;
import software.amazon.awssdk.services.sts.StsClient;

@Slf4j
@Configuration
@RequiredArgsConstructor
public class AwsConfiguration {

    // Amount of time, relative to STS token expiration, that the cached credentials are considered
    // stale and must be updated. Default is 1 minute.
    private static final Duration STALE_TIME_DURATION = Duration.ofMinutes(35);

    // Amount of time, relative to STS token expiration, that the cached credentials are considered
    // close to stale and should be updated. Default is 5 minutes.
    private static final Duration PREFETCH_TIME_DURATION = Duration.ofMinutes(36);

    private final AwsProperties awsProperties;
    private final Environment environment;

    @Bean
    public AwsCredentialsProvider awsCredentialsProvider() {
        if (Arrays.stream(environment.getActiveProfiles())
                .anyMatch(s -> "local-localstack".equalsIgnoreCase(s)
                        || "test".equalsIgnoreCase(s))) {
            // Use Localstack credentials.
            var awsCredentials = NvcfUtils.getCredentialsForLocalStack();
            return StaticCredentialsProvider.create(awsCredentials);
        } else if (Arrays.asList(environment.getActiveProfiles()).contains("local-sts-vault")) {
            // Use AWS STS credentials from Vault to authenticate with AWS IAM.
            var awsCredentials = NvcfUtils.getStsCredentialsFromVault();
            return StaticCredentialsProvider.create(awsCredentials);
        } else {
            // WebIdentityTokenFileCredentialsProvider should be used when the
            // service is being deployed to AWS EKS as it has direct access to AWS IAM
            // for authentication purposes.
            return WebIdentityTokenFileCredentialsProvider.builder()
                    .staleTime(STALE_TIME_DURATION)
                    .prefetchTime(PREFETCH_TIME_DURATION)
                    .build();
        }
    }

    private <BuilderT extends AwsClientBuilder<BuilderT, ClientT>, ClientT> BuilderT builder(
            AwsClientBuilder<BuilderT, ClientT> builder,
            AwsCredentialsProvider credentialsProvider) {
        builder.region(Region.of(awsProperties.getRegion()));
        if (StringUtils.isNotBlank(awsProperties.getEndpoint())) { // For LocalStack
            builder.endpointOverride(URI.create(awsProperties.getEndpoint()));
        }
        return builder.credentialsProvider(credentialsProvider);
    }

    @Bean
    public StsClient stsClient(AwsCredentialsProvider credentialsProvider) {
        log.info("AWS Region: {}", awsProperties.getRegion());
        return builder(StsClient.builder(), credentialsProvider).build();
    }

    @Bean
    public StsAsyncClient stsAsyncClient(AwsCredentialsProvider credentialsProvider) {
        log.info("AWS Region: {}", awsProperties.getRegion());
        return builder(StsAsyncClient.builder(), credentialsProvider).build();
    }

    @Bean
    public S3Client s3Client(AwsCredentialsProvider credentialsProvider) {
        log.info("AWS Region: {}", awsProperties.getRegion());
        var builder = builder(S3Client.builder(), credentialsProvider);
        if (StringUtils.isNotBlank(awsProperties.getEndpoint())) { // For LocalStack
            builder.forcePathStyle(true);
        }

        // Override the region using the one where the buckets were provisioned. If we
        // do not override, it results in the following error from AWS when the request
        // to list assets lands in a region(such as us-east-1) different from the one
        // where the buckets were provisioned(such as us-west-2):
        //     The authorization header is malformed; the region 'us-east-1' is wrong;
        //     expecting 'us-west-2'
        builder.region(Region.of(awsProperties.getS3().getProvisionedRegion()));
        return builder.build();
    }

    @Bean
    public S3AsyncClient s3AsyncClient(AwsCredentialsProvider credentialsProvider) {
        log.info("AWS Region: {}", awsProperties.getRegion());
        var builder = builder(S3AsyncClient.builder(), credentialsProvider);
        if (StringUtils.isNotBlank(awsProperties.getEndpoint())) { // For LocalStack
            builder.forcePathStyle(true);
        }

        // Override the region using the one where the buckets were provisioned. If we
        // do not override, it results in the following error from AWS when the request
        // to list assets lands in a region(such as us-east-1) different from the one
        // where the buckets were provisioned(such as us-west-2):
        //     The authorization header is malformed; the region 'us-east-1' is wrong;
        //     expecting 'us-west-2'
        builder.region(Region.of(awsProperties.getS3().getProvisionedRegion()));
        return builder.build();
    }

    @Bean
    public S3Presigner s3Presigner(AwsCredentialsProvider credentialsProvider) {
        // Use the region where the buckets were provisioned to configure S3Presigner.
        // Otherwise, using pre-signed URLs to download assets results in 400 - Bad Request
        // error.
        var builder = S3Presigner.builder()
                .region(Region.of(awsProperties.getS3().getProvisionedRegion()))
                .credentialsProvider(credentialsProvider);
        if (StringUtils.isNotBlank(awsProperties.getEndpoint())) { // For LocalStack
            builder.endpointOverride(URI.create(awsProperties.getEndpoint()))
                    .serviceConfiguration(S3Configuration.builder()
                                                  .pathStyleAccessEnabled(true)
                                                  .build());
        } else {
            builder.serviceConfiguration(S3Configuration.builder()
                                                 .accelerateModeEnabled(true)
                                                 .build());
        }
        return builder.build();
    }

    @Data
    @Configuration
    @ConfigurationProperties(prefix = "nvcf.aws")
    public static class AwsProperties {

        private static final String MESG_MISSING_REQUIRED_PROPERTY =
                "Missing required configuration property '%s'";

        // AWS Creds from NVCF's Vault namespace. The creds are passed from
        // NVCF to the Inference Container to access S3 via intermediaries.
        private String region;
        private String endpoint;  // Used only for LocalStack.
        private AwsS3Properties s3;

        @Data
        public static class AwsS3Properties {

            private String provisionedRegion;
            private ResultsProperties results;
            private AssetsProperties assets;

            @Data
            public static class ResultsProperties {

                private String bucketName;
                private String roleArn;
            }

            @Data
            public static class AssetsProperties {

                private String bucketName;
            }
        }

        @PostConstruct
        public void validate() {
            validateProperty("nvcf.aws.region", region);
            validateProperty("nvcf.aws.s3.provisioned-region", getS3().getProvisionedRegion());
            validateProperty("nvcf.aws.s3.results.bucket-name",
                             getS3().getResults().getBucketName());
            validateProperty("nvcf.aws.s3.results.role-arn", getS3().getResults().getRoleArn());
            validateProperty("nvcf.aws.s3.assets.bucket-name", getS3().getAssets().getBucketName());
        }

        private static void validateProperty(String propName, String propValue) {
            if (StringUtils.isBlank(propValue)) {
                String msg = format(MESG_MISSING_REQUIRED_PROPERTY, propName);
                log.error(msg);
                throw new IllegalArgumentException(msg);
            }
        }
    }
}
