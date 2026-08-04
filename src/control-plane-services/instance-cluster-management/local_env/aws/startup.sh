#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

aws --profile default configure set aws_access_key_id "dummy_access_key"

aws --profile default configure set aws_secret_access_key "dummy_secret_key"

aws --profile default configure set region us-east-1

echo "Create global sqs for instance creation"

awslocal --endpoint-url=http://localhost:4566 sqs create-queue --queue-name gdn-spot-instance-requests-global.fifo --attributes FifoQueue=true,ContentBasedDeduplication=true --region us-east-1

echo "Create topic for instance termination"

awslocal --endpoint-url=http://localhost:4566 sns create-topic --name gdn-instance-termination-localhost.fifo --attributes FifoTopic=true,ContentBasedDeduplication=true --region us-east-1

echo "Create queue for subscribing to the termination topic"

awslocal --endpoint-url=http://localhost:4566 sqs create-queue --queue-name gdn-instance-termination-localhost.fifo --attributes FifoQueue=true,ContentBasedDeduplication=true --region us-east-1

echo "Subscribing termination queue to termination topic"

awslocal --endpoint-url=http://localhost:4566 sns subscribe --topic-arn arn:aws:sns:us-east-1:000000000000:gdn-instance-termination-localhost.fifo --protocol sqs --notification-endpoint arn:aws:sqs:us-east-1:000000000000:gdn-instance-termination-localhost.fifo --attributes RawMessageDelivery=true

awslocal --endpoint-url=http://localhost:4566 sns list-topics

echo "create iam role for sts assume role"

aws --endpoint-url=http://localhost:4566 iam create-role --role-name spot-queue-access-role --assume-role-policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}'

echo "Localstack setup completed"
