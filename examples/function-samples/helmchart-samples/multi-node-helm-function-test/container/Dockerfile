# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

FROM public.ecr.aws/hpc-cloud/nccl-tests:cuda12.8.1-efa1.43.2-ofiv1.16.3-ncclv2.27.7-1-testsv2.16.9

ENV DEBIAN_FRONTEND=noninteractive

WORKDIR /workspace/

RUN apt-get update && \
    DEBIAN_FRONTEND=noninteractive apt-get install -y \
    apt-transport-https \
    ca-certificates \
    curl \
    git \
    python3 \
    python3-pip \
    openssh-server \
    cmake \
    build-essential \
    libboost-program-options-dev \
    && \
    apt-get clean

# RUN mkdir /var/run/sshd

RUN sed -i 's/#PermitRootLogin prohibit-password/PermitRootLogin yes/' /etc/ssh/sshd_config && \
    sed -i 's/#PermitEmptyPasswords no/PermitEmptyPasswords yes/' /etc/ssh/sshd_config && \
    sed -i 's/#PermitUserEnvironment no/PermitUserEnvironment yes/' /etc/ssh/sshd_config && \
    sed -i 's/#Banner \/etc\/issue.net/Banner \/dev\/null/' /etc/ssh/sshd_config

# Set the root password to an empty password
RUN passwd -d root

# build nccl tests
# RUN git clone --depth 1 --branch v2.17.5 https://github.com/NVIDIA/nccl-tests.git
# RUN cd nccl-tests && make MPI=1 MPI_HOME=/usr/local/mpi

# build nvbandwidth
RUN git clone https://github.com/NVIDIA/nvbandwidth.git
RUN cd nvbandwidth && \
    cmake -DMULTINODE=1 . && \
    make 

# install python requirements
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt --break-system-packages  

# add tooling for using kubctl to connect between nodes
RUN mkdir tools
COPY ssh_helper /usr/local/bin
RUN chmod +x /usr/local/bin/ssh_helper

# copy app source code
COPY *.py .
COPY *.sh .

EXPOSE 22

CMD [ "/bin/bash", "run.sh" ]
