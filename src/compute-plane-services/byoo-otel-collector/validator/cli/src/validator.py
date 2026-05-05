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

import argparse
import logging
import sys
import traceback
from datetime import datetime, timedelta, timezone

from rich.console import Console
from rich.logging import RichHandler

from .utils import *

console = Console(width=120, force_terminal=True)
logging.basicConfig(
    level=logging.INFO,
    format="%(message)s",
    handlers=[
        RichHandler(
            enable_link_path=False,
            markup=True,
            rich_tracebacks=True,
            show_path=False,
            show_time=False,
            console=console,
        )
    ],
)

logger = logging.getLogger(__name__)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Validator for metrics collected by byoo-otel-collector")
    parser.add_argument("id", help="ID to validate")
    parser.add_argument(
        "--config",
        default="validator-config.yaml",
        help="Path to the configuration file",
    )
    parser.add_argument(
        "--wrapper-type",
        required=True,
        choices=[t.value for t in WrapperType],
        help="Wrapper type (function or task)",
    )
    parser.add_argument(
        "--workload-type",
        required=True,
        choices=[t.value for t in WorkloadType],
        help="Workload type (container or helm)",
    )
    parser.add_argument(
        "--cloud-provider",
        required=True,
        choices=[p.value for p in CloudProvider],
        help="Cloud provider (gfn or non_gfn)",
    )
    parser.add_argument("--start", help="Start time in RFC3339 format (default: 12 hours ago)")
    parser.add_argument("--end", help="End time in RFC3339 format (default: now)")
    parser.add_argument(
        "--log-level",
        default="info",
        choices=["debug", "info", "warning", "error", "critical"],
        help="Log level",
    )
    parser.add_argument(
        "--extra-promql-filters",
        default="",
        help='Additional PromQL filters (e.g. \'job="my-job",env="prod"\')',
    )
    parser.add_argument(
        "--golden",
        action="store_true",
        help="Check if the metrics are golden (validating against golden metrics)",
    )

    args = parser.parse_args()

    # Convert string arguments to enums
    args.wrapper_type = WrapperType(args.wrapper_type)
    args.workload_type = WorkloadType(args.workload_type)
    args.cloud_provider = CloudProvider(args.cloud_provider)

    return args


def main() -> int:
    args = parse_args()

    # Set log level
    logging.getLogger().setLevel(getattr(logging, args.log_level.upper()))

    # Set default times if not provided
    if not args.start:
        args.start = (datetime.now(timezone.utc) - timedelta(hours=24)).strftime("%Y-%m-%dT%H:%M:%SZ")
    if not args.end:
        args.end = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")

    # Validate time format and order
    try:
        start_time = datetime.fromisoformat(args.start)
        end_time = datetime.fromisoformat(args.end)
        if start_time >= end_time:
            raise ValueError("Start time must be before end time")
    except ValueError as e:
        pass

    logger.info(f"###########################################################")
    logger.info(f" Wrapper Type        : {args.wrapper_type}")
    logger.info(f" Workload Type       : {args.workload_type}")
    logger.info(f" Cloud Provider      : {args.cloud_provider}")
    logger.info(f" ID                  : {args.id}")
    logger.info(f" Start               : {args.start}")
    logger.info(f" End                 : {args.end}")
    logger.info(f" Extra PromQL Filters: {args.extra_promql_filters}")
    logger.info(f"###########################################################\n\n")

    try:
        validator = MetricsValidator(args.config)
        result = validator.validate(
            args.wrapper_type,
            args.workload_type,
            args.cloud_provider,
            args.id,
            args.start,
            args.end,
            args.extra_promql_filters,
            args.golden,
        )

        logger.info(f"###########################################################")
        logger.info(f" Validation Result:")
        for job, status in result.items():
            logger.info(f"  - {job:30}: {status.value}")
        logger.info(f"###########################################################\n\n")

        if any(status == MetricsValidationResult.INVALID for status in result.values()):
            exit(1)
        return 0
    except Exception as e:
        logger.error(f"Validation failed: {e}")
        traceback.print_exc(file=sys.stdout)
        exit(1)


if __name__ == "__main__":
    main()
