#!/usr/bin/env bash
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

set -e

EXAMPLE_USAGE="example usage: build_zips.sh <VERSION>"
SCRIPT_DIR="$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )" &> /dev/null && pwd )"
REPO_ROOT_DIR=$(dirname "$(dirname "${SCRIPT_DIR}")")

if [[ -z "$1" ]]; then
  echo "need to provide version number as arg"
  echo "$EXAMPLE_USAGE"
  exit 1
fi

#BUILD_ALL=0
#while getopts "a" opt; do
#  case $opt in
#    a) BUILD_ALL=1;;
#    *) echo "$EXAMPLE_USAGE"
#  esac
#done

# Go variables
# "-s -w" tells Go compiler to omit symbol table and debug information.
# See https://pkg.go.dev/cmd/link for more info
LD_FLAGS="-s -w"

# Script args
VERSION=$1
#SKIP_UPX=$2

# Filesystem variables
ZIP_BASENAME="ess-agent"
BUILDS_DIR_SRC="${REPO_ROOT_DIR}/bin"
BUILDS_DIR_DEST="${REPO_ROOT_DIR}/nv_releases/builds"
ZIPS_DIR_DEST="${REPO_ROOT_DIR}/nv_releases/zips"

# List of operating systems
OS_DARWIN="darwin"
OS_WINDOWS="windows"
OS_LINUX="linux"

# List of CPU architectures
ARCH_AMD64="amd64"
ARCH_ARM64="arm64"
ARCH_UNIVERSAL="universal"

# args:
# 1 - os
# 2 - arch
build_and_zip() {
  # Set OS and ARCH
  OS=$1
  ARCH=$2
  echo "Starting ${OS}/${ARCH} build..."

  # Make binary
  echo "GOOS=${OS} GOARCH=${ARCH} make build"
  GOOS=${OS} GOARCH=${ARCH} make build
  echo "${OS}/${ARCH} build completed"

  for build in "$BUILDS_DIR_SRC"/*
  do
    BUILD_FILENAME=$(basename "$build")

    if [[ "$BUILD_FILENAME" != "ess-agent" ]] && [[ "$BUILD_FILENAME" != "ess-agent.exe" ]];then
      continue
    fi

    # If build is Windows and doesn't have .exe file extension, then add it
    if [[ "$BUILD_FILENAME" == "ess-agent" ]] && [[ "$OS" == "$OS_WINDOWS" ]];then
      echo "Is Windows build, renaming $BUILD_FILENAME to ess-agent.exe"
      BUILD_FILENAME="ess-agent.exe"
    fi

    # TODO should add binary compression for zip files in a later release
    # Compress binary
#    if [[ $SKIP_UPX == 'true' ]]; then
#      echo "[WARN] Skipping UPX compression..."
#    elif ! command -v upx &> /dev/null; then
#      # Check if UPX binary exists in repo's root dir
#      # This is for when running in Jenkins pipeline
#      if ! command -v $REPO_ROOT_DIR/upx &> /dev/null; then
#        echo "[ERROR] Executable 'upx' needed for binary compression, exiting script..."
#        exit 1
#      else
#        echo "$REPO_ROOT_DIR/upx $build"
#        $REPO_ROOT_DIR/upx "$build"
#      fi
#    else
#      echo "upx $build"
#      upx "$build"
#    fi

    # Move binary to folder
    if [ -d "${BUILDS_DIR_DEST}/${OS}_${ARCH}" ]; then
      rm -r "${BUILDS_DIR_DEST}/${OS}_${ARCH}"
    fi
#    echo "rm -r ${BUILDS_DIR_DEST}/${OS}_${ARCH}"
#    echo
#    echo "mkdir -p ${BUILDS_DIR_DEST}/${OS}_${ARCH}"
    mkdir -p "${BUILDS_DIR_DEST}/${OS}_${ARCH}"
#    echo
#    echo "cp $build ${BUILDS_DIR_DEST}/${OS}_${ARCH}/${BUILD_FILENAME}"
    cp "$build" "${BUILDS_DIR_DEST}/${OS}_${ARCH}/${BUILD_FILENAME}"
#    echo
#    echo "ls -lah ${BUILDS_DIR_DEST}/${OS}_${ARCH}/${BUILD_FILENAME}"
#    ls -lah "${BUILDS_DIR_DEST}/${OS}_${ARCH}/${BUILD_FILENAME}"
#    echo
#    echo "file ${BUILDS_DIR_DEST}/${OS}_${ARCH}/${BUILD_FILENAME}"
    file "${BUILDS_DIR_DEST}/${OS}_${ARCH}/${BUILD_FILENAME}"
    echo
    echo "${OS}/${ARCH} build sent to ${BUILDS_DIR_DEST}/${OS}_${ARCH}/${BUILD_FILENAME}"
  done

  # zip binary
  echo "Zipping ${BUILDS_DIR_DEST}/${OS}_${ARCH}..."
  zip -jr "${ZIPS_DIR_DEST}/${ZIP_BASENAME}_${VERSION}_${OS}_${ARCH}.zip" "${BUILDS_DIR_DEST}/${OS}_${ARCH}"
  echo "Zipped to ${ZIPS_DIR_DEST}/${ZIP_BASENAME}_${VERSION}_${OS}_${ARCH}.zip"
  echo "Completed ${OS}/${ARCH} build"
  printf "\n\n\n\n"
}

build_and_zip_universal() {
  # set OS
    OS=$1
    echo "Starting ${OS} universal build..."

    # if build is Windows and doesn't have .exe file extension, then add it
    BUILD_FILENAME="ess-agent"
    if [[ "$BUILD_FILENAME" == "ess-agent" ]] && [[ "$OS" == "$OS_WINDOWS" ]];then
      echo "Is Windows build, renaming $BUILD_FILENAME to ess-agent.exe"
      BUILD_FILENAME="ess-agent.exe"
    fi

    # check if binaries exist
    if [ ! -f "${BUILDS_DIR_DEST}/${OS}_${ARCH_AMD64}/${BUILD_FILENAME}" ]; then
      build_and_zip "${OS}" "${ARCH_AMD64}"
    fi
    if [ ! -f "${BUILDS_DIR_DEST}/${OS}_${ARCH_ARM64}/${BUILD_FILENAME}" ]; then
      build_and_zip "${OS}" "${ARCH_ARM64}"
    fi

    # create dir
    if [ -d "${BUILDS_DIR_DEST}/${OS}_${ARCH_UNIVERSAL}" ]; then
      rm -r "${BUILDS_DIR_DEST}/${OS}_${ARCH_UNIVERSAL}"
    fi

    mkdir -p "${BUILDS_DIR_DEST}/${OS}_${ARCH_UNIVERSAL}"

    # combine binaries
    lipo -create -output "${BUILDS_DIR_DEST}/${OS}_${ARCH_UNIVERSAL}/${BUILD_FILENAME}" "${BUILDS_DIR_DEST}/${OS}_${ARCH_AMD64}/${BUILD_FILENAME}" "${BUILDS_DIR_DEST}/${OS}_${ARCH_ARM64}/${BUILD_FILENAME}"
    file "${BUILDS_DIR_DEST}/${OS}_${ARCH_UNIVERSAL}/${BUILD_FILENAME}"
    echo

    # zip binary
    echo "Zipping ${BUILDS_DIR_DEST}/${OS}_${ARCH_UNIVERSAL}..."
    zip -jr "${ZIPS_DIR_DEST}/${ZIP_BASENAME}_${VERSION}_${OS}_${ARCH_UNIVERSAL}.zip" "${BUILDS_DIR_DEST}/${OS}_${ARCH_UNIVERSAL}"
    echo "Zipped to ${ZIPS_DIR_DEST}/${ZIP_BASENAME}_${VERSION}_${OS}_${ARCH_UNIVERSAL}.zip"
    echo "Completed ${OS}/${ARCH_UNIVERSAL} build"
    printf "\n\n\n\n"
}

# build binaries for all Mac, Windows, Linux
echo "Building all builds..."


# x86-64
build_and_zip $OS_DARWIN $ARCH_AMD64
build_and_zip $OS_WINDOWS $ARCH_AMD64
build_and_zip $OS_LINUX $ARCH_AMD64

# ARM
build_and_zip $OS_DARWIN $ARCH_ARM64
build_and_zip $OS_LINUX $ARCH_ARM64
build_and_zip $OS_WINDOWS $ARCH_ARM64

# Universal
build_and_zip_universal $OS_DARWIN

exit 0
