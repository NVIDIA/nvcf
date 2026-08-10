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
FROM nvcr.io/nvidia/distroless/java:25-jdk-v4.0.8

# Add the service jar
ARG APP_JAR=nvct-service/target/app.jar
COPY ${APP_JAR} /usr/share/app.jar

# enable setting the ulimit to max on boot
ENV ULIMIT_FLAG=1

# use this instead of JAVA_OPTS
ENV JDK_JAVA_OPTIONS="-XX:MaxRAMPercentage=40.0 -XX:+EnableDynamicAgentLoading -Dreactor.netty.pool.maxIdleTime=30000 -Dreactor.netty.pool.maxConnections=500"

WORKDIR /home/app
CMD ["java", "-jar", "/usr/share/app.jar"]
