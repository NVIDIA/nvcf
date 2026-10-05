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
# Pull base image.
FROM nvcr.io/nvidia/distroless/java:25-jdk-v4.1.2

# Add the service jar
ARG APP_JAR=app.jar
COPY ${APP_JAR} /usr/share/app.jar

# enable setting the ulimit to max on boot
ENV ULIMIT_FLAG=1

# use this instead of JAVA_OPTS
ENV JDK_JAVA_OPTIONS=""

# set profile one of "local", "stage", "prod", "ncp"
ENV SPRING_PROFILES_ACTIVE=""

# The full OAuth2 issuer URI Spring Security validates inbound caller JWTs against.
ENV AUTH_TOKEN_ISSUER=""

# Used to configure the iss value included in the assertions produced by the Notary Service
ENV ASSERTION_ISSUER_URL=""

# The Notary Service will accept only OAuth2 tokens with the specified scope
ENV AUTH_TOKEN_SCOPE="notary-sign"

# Used to control the maximum size of the assertions produced by the Notary Service
ENV MAX_REQUEST_BODY_SIZE_BYTES=4096

# Overrides where service is looking for secrets file prepared by vault
ENV VAULT_SECRETS_JSON_PATH=vault/vault-secrets.json

WORKDIR /home/app
CMD ["java", "-jar", "/usr/share/app.jar"]
