#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

# Exit on error
set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo -e "${YELLOW}Setting up test environment...${NC}"

# Check if Python is installed
if ! command -v python3 &> /dev/null; then
    echo -e "${RED}Python 3 is not installed. Please install Python 3 and try again.${NC}"
    exit 1
fi

# Check if pip is installed
if ! command -v pip3 &> /dev/null; then
    echo -e "${RED}pip3 is not installed. Please install pip3 and try again.${NC}"
    exit 1
fi

# Create and activate virtual environment if it doesn't exist
if [ ! -d "venv" ]; then
    echo -e "${YELLOW}Creating virtual environment...${NC}"
    python3 -m venv venv
fi

# Activate virtual environment
echo -e "${YELLOW}Activating virtual environment...${NC}"
source venv/bin/activate

# Install dependencies
echo -e "${YELLOW}Installing dependencies...${NC}"
pip install -r requirements.txt
pip install -r test-requirements.txt

# Generate API client if needed
if [ ! -d "cf-api" ]; then
    echo -e "${YELLOW}Generating API client...${NC}"
    python generate_api.py
fi

# Set test environment variables
export NVCF_OAUTH2_CLIENT_ID="test-client-id"
export NVCF_OAUTH2_CLIENT_SECRET="test-secret"
export NVCF_OAUTH2_TOKEN_ENDPOINT="http://test-token-endpoint"
export NVCF_BASE_HTTP_URL="https://test-api.nvcf.nvidia.com"
export NVCF_BASE_GRPC_URL="test-grpc.nvcf.nvidia.com:443"

# Run tests with coverage
echo -e "${YELLOW}Running tests...${NC}"
pytest --cov=cloud_functions_cli --cov-report=term-missing -v

# Check test results
if [ $? -eq 0 ]; then
    echo -e "${GREEN}All tests passed successfully!${NC}"
else
    echo -e "${RED}Tests failed. Please check the output above for details.${NC}"
    exit 1
fi

# Deactivate virtual environment
deactivate

echo -e "${GREEN}Test run completed.${NC}" 