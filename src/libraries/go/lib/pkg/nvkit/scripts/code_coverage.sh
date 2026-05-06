#!/bin/bash -e
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0


# IMPORTANT! FAIL if one of pipe commands returns error
set -o pipefail
EXITCODE=0
mkdir -p reports

function main() {
  # Run tests
  goTest

  # Remove unused dependecies
  go mod tidy
}

function goTest() {
  echo "Running go test:"
  goTestRun
  # Generate reports
  # Download dependencies, moved to base image for go test
  go-junit-report <reports/go-test.log >reports/junit-report.xml
  return $EXITCODE
}

function goTestRun() {
  # Run go test
  ### go test : Run test and also generate cover profile
  ### tee     : We want to see test output and save it for later use in go-junit-report
  ### grep    : Display only few details of running tests. Full log is reports/go-test.log. NOTE: if you remove 'ok' the grep command will exit with status 1 and break the script (due -o pipefail)
  if go test -covermode=count -cover $(go list ./... | grep -v '.*\/vendor\/\|.*\/examples\/') -coverprofile=reports/coverage-report.out -v 2>&1 |
    tee reports/go-test.log |
    grep -w '^ok \|FAIL\|FAILED!'; then
    echo "Go test succeeded!"
    echo "Total coverage:"
    go tool cover -func reports/coverage-report.out | grep total:
    EXITCODE=0
    return
  else
    echo "Go test has failed!"
    EXITCODE=1
  fi
  total_threshold=$(go tool cover -func reports/coverage-report.out | grep total: | awk '{print $3}' | awk -F '.' '{print $1}')
  if [ "$total_threshold" -lt 55 ]
  then
    echo "Go test has failed! Insufficient threshold"
    EXITCODE=1
  else
    echo "Go test coverage succeeded!"
  fi
}

main $@
