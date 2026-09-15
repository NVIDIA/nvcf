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
package com.nvidia.icms.configuration.aws;

import com.amazonaws.auth.AWSStaticCredentialsProvider;
import com.amazonaws.auth.BasicAWSCredentials;
import com.amazonaws.auth.WebIdentityTokenCredentialsProvider;
import com.amazonaws.client.builder.AwsClientBuilder;
import com.amazonaws.services.sns.AmazonSNS;
import com.amazonaws.services.sns.AmazonSNSClientBuilder;
import com.amazonaws.services.sqs.AmazonSQS;
import com.amazonaws.services.sqs.AmazonSQSClientBuilder;
import java.net.URI;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.context.annotation.Primary;
import org.springframework.context.annotation.Profile;
import software.amazon.awssdk.auth.credentials.AwsCredentialsProvider;
import software.amazon.awssdk.auth.credentials.AwsSessionCredentials;
import software.amazon.awssdk.auth.credentials.StaticCredentialsProvider;
import software.amazon.awssdk.auth.credentials.WebIdentityTokenFileCredentialsProvider;
import software.amazon.awssdk.regions.Region;
import software.amazon.awssdk.services.sts.StsAsyncClient;

@Configuration
@AllArgsConstructor
@Slf4j
public class AwsConfiguration {

    public static final String AWS_SQS_CLIENT_QUALIFIER = "aws-sqs-client-qualifier";

    private final AwsConfigurationProperties awsConfigurationProperties;

    @Bean
    @RefreshScope
    @Profile({"local | test"})
    public AWSStaticCredentialsProvider getAwsStaticCredentialsProvider() {
        BasicAWSCredentials credentials = new BasicAWSCredentials(
                awsConfigurationProperties.getAccessKey(),
                awsConfigurationProperties.getSecretKey());
        return new AWSStaticCredentialsProvider(credentials);
    }

    @Bean
    @RefreshScope
    @Profile({"local | test"})
    public AwsCredentialsProvider getAwsCredentialsProvider() {
        return StaticCredentialsProvider.create(
                AwsSessionCredentials.create(awsConfigurationProperties.getAccessKey(),
                                             awsConfigurationProperties.getSecretKey(),
                                             "dummy_session_token"));
    }

    @Bean
    @RefreshScope
    @Profile({"!local & !test"})
    public AwsCredentialsProvider awsCredentialsProvider() {

        // WebIdentityTokenFileCredentialsProvider should be used when the
        // service is being deployed to AWS EKS as it has direct access to AWS IAM
        // for authentication purposes.
        return WebIdentityTokenFileCredentialsProvider.create();
    }

    @Bean
    @Primary
    @RefreshScope
    @Profile({"local | test"})
    public AmazonSQS amazonSQSClientForGlobalQueueLocal(
            AWSStaticCredentialsProvider credentialsProvider) {

        AmazonSQSClientBuilder builder = AmazonSQSClientBuilder.standard();
        if (awsConfigurationProperties.getSqsEndpoint() == null) {
            builder.setRegion(awsConfigurationProperties.getSqsRegion());
        } else {
            builder.withEndpointConfiguration(
                    new AwsClientBuilder.EndpointConfiguration(
                            awsConfigurationProperties.getSqsEndpoint(),
                            awsConfigurationProperties.getSqsRegion()));
        }
        return builder.withCredentials(credentialsProvider).build();
    }

    @Bean(AWS_SQS_CLIENT_QUALIFIER)
    @RefreshScope
    @Profile({"local | test"})
    public AmazonSQS amazonSQSClientLocal(
            AWSStaticCredentialsProvider credentialsProvider) {

        AmazonSQSClientBuilder builder = AmazonSQSClientBuilder.standard();
        if (awsConfigurationProperties.getAwsEndpoint() == null) {
            builder.setRegion(awsConfigurationProperties.getSqsRegion());
        } else {
            builder.withEndpointConfiguration(
                    new AwsClientBuilder.EndpointConfiguration(
                            awsConfigurationProperties.getAwsEndpoint(),
                            awsConfigurationProperties.getSqsRegion()));
        }
        return builder.withCredentials(credentialsProvider).build();
    }

    @Bean
    @RefreshScope
    @Profile({"local | test"})
    public AmazonSNS amazonSNSLocal(AWSStaticCredentialsProvider credentialsProvider) {
        AmazonSNSClientBuilder snsClientBuilder = AmazonSNSClientBuilder.standard();
        if (awsConfigurationProperties.getSqsEndpoint() == null) {
            snsClientBuilder.setRegion(awsConfigurationProperties.getSnsRegion());
        } else {
            snsClientBuilder.withEndpointConfiguration(
                    new AwsClientBuilder.EndpointConfiguration(
                            awsConfigurationProperties.getAwsEndpoint(),
                            awsConfigurationProperties.getSnsRegion()));
        }
        return snsClientBuilder.withCredentials(credentialsProvider).build();
    }

    @Bean
    @Primary
    @RefreshScope
    @Profile({"!local & !test"})
    public AmazonSQS amazonSQSClientForGlobalQueue() {

        AmazonSQSClientBuilder builder = AmazonSQSClientBuilder.standard();
        configureAwsClient(awsConfigurationProperties.getSqsEndpoint(),
                           awsConfigurationProperties.getSqsRegion(),
                           awsConfigurationProperties.getRoleArn(),
                           awsConfigurationProperties.getTokenFile(),
                           builder);
        return builder.build();
    }

    @Bean(AWS_SQS_CLIENT_QUALIFIER)
    @RefreshScope
    @Profile({"!local & !test"})
    public AmazonSQS amazonSQSClient() {

        AmazonSQSClientBuilder builder = AmazonSQSClientBuilder.standard();
        configureAwsClient(awsConfigurationProperties.getAwsEndpoint(),
                           awsConfigurationProperties.getSqsRegion(),
                           awsConfigurationProperties.getRoleArn(),
                           awsConfigurationProperties.getTokenFile(),
                           builder);
        return builder.build();
    }

    @Bean
    @RefreshScope
    @Profile({"!local & !test"})
    public AmazonSNS amazonSNS() {
        AmazonSNSClientBuilder snsClientBuilder = AmazonSNSClientBuilder.standard();
        configureAwsClient(awsConfigurationProperties.getAwsEndpoint(),
                           awsConfigurationProperties.getSnsRegion(),
                           awsConfigurationProperties.getRoleArn(),
                           awsConfigurationProperties.getTokenFile(),
                           snsClientBuilder);
        return snsClientBuilder.build();
    }

    @Bean
    @RefreshScope
    public StsAsyncClient stsAsyncClient(AwsCredentialsProvider credentialsProvider) {
        log.info("Build stsAsyncClient with region {}, with aws endpoint {}.",
                 awsConfigurationProperties.getSqsRegion(),
                 awsConfigurationProperties.getAwsEndpoint());
        return awsClientBuilder(StsAsyncClient.builder(), credentialsProvider).build();
    }

    private <BuilderT extends software.amazon.awssdk.awscore.client.builder.AwsClientBuilder<BuilderT, ClientT>, ClientT> BuilderT awsClientBuilder(
            software.amazon.awssdk.awscore.client.builder.AwsClientBuilder<BuilderT, ClientT> builder,
            AwsCredentialsProvider credentialsProvider) {
        builder.region(Region.of(awsConfigurationProperties.getSqsRegion()));
        if (StringUtils.isNotBlank(awsConfigurationProperties.getAwsEndpoint())) { // For LocalStack
            builder.endpointOverride(URI.create(awsConfigurationProperties.getAwsEndpoint()));
        }
        return builder.credentialsProvider(credentialsProvider);
    }

    private void configureAwsClient(
            String awsEndpoint, String awsRegion, String roleArn, String tokenFile,
            AwsClientBuilder<?, ?> awsClientBuilder) {
        if (awsEndpoint != null) {
            awsClientBuilder.withEndpointConfiguration(
                    new AwsClientBuilder.EndpointConfiguration(awsEndpoint, awsRegion));
        } else {
            awsClientBuilder.withRegion(awsRegion);
        }
        awsClientBuilder.withCredentials(
                WebIdentityTokenCredentialsProvider.builder()
                        .roleArn(roleArn)
                        .webIdentityTokenFile(tokenFile)
                        .build());

    }
}
