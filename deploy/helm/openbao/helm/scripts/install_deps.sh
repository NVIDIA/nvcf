#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0


# Download and install jwker

JWKER_VERSION="v0.2.1"
JWKER_ARCH="Linux_x86_64"

# Create temp directory
TEMP_DIR=$(mktemp -d)
cd $TEMP_DIR

echo "Downloading jwker ${JWKER_VERSION} for ${JWKER_ARCH}"

# Download jwker release
wget "https://github.com/jphastings/jwker/releases/download/${JWKER_VERSION}/jwker_${JWKER_ARCH}.tar.gz"

# Extract archive
tar xzf "jwker_${JWKER_ARCH}.tar.gz"

# Install binary to /usr/local/bin
mv jwker /usr/local/bin/
chmod +x /usr/local/bin/jwker

# Verify
which jwker

# Clean up
rm -rf $TEMP_DIR
