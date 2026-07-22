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
import os
import random
import datetime
import time
import json
import signal
import logging
import requests
from dataclasses import dataclass
from threading import Thread

# provided by service
ROOT_OUTPUT_DIR = os.getenv("NVCT_RESULTS_DIR", "/var/task/result")
PROGRESS_FILE = os.getenv("NVCT_PROGRESS_FILE_PATH", ROOT_OUTPUT_DIR + "/progress")

# user defined
INPUT_MODELS_DIR = os.getenv("INPUT_MODELS_DIR", "/config/models")
INPUT_RESOURCES_DIR = os.getenv("INPUT_RESOURCES_DIR", "/config/resources")

FILE_SIZE_BYTES = max(
    int(os.getenv("FILE_SIZE_BYTES", 1024 * 1024)), 1024 * 8
)  # at least an 8kB file
NUM_OF_RESULTS = max(int(os.getenv("NUM_OF_RESULTS", 1)), 1)
DELAY_BETWEEN_RESULTS_IN_MINUTES = int(os.getenv("DELAY_BETWEEN_RESULTS_IN_MINUTES", 1))
DELAY_TEARDOWN = 30
INCLUDE_METADATA = os.getenv("INCLUDE_METADATA", "true")

logger = logging.getLogger(__name__)
logger.setLevel(logging.INFO)

meter = None
tracer = None
task_results_counter = None
task_progress_gauge = None

def init_observervavility():
    global logger, meter, task_results_counter, task_progress_gauge, tracer
    from byoo import logging_handler, meter, tracer
    # Setup BYOO metrics
    if meter:
        task_results_counter = meter.create_counter(
            "task_results_count", description="Total task request", unit="1"
        )
        task_progress_gauge = meter.create_gauge(
            "task_progress_gauge", description="Task completion percentage", unit="%"
        )

    # Setup BYOO logging
    if logging_handler:
        logger.addHandler(logging_handler)

# task run status
TASK_RUNNING = True


@dataclass
class Progress:
    """
    Schema of the progress file.

    :param taskId: UUID of task, passed from env variables.
    :param percentComplete: an int value between 0 and 100 to demonstrate percent complete.
    :param name: name of result folder/artifact to upload to NGC
    :param lastUpdatedAt: timestamp in RFC 3339 Nano format.
    :param metadata: a dict of additional data to include
    """

    def __init__(
        self,
        taskId: str,
        percentComplete: int,
        name: str,
        lastUpdatedAt: str,
        metadata: dict,
    ):
        self.taskId = taskId
        self.percentComplete = percentComplete
        self.name = name
        self.lastUpdatedAt = lastUpdatedAt
        self.metadata = metadata

    def output_to_json(self):
        return json.dumps(self.__dict__)


def parse_progress_file(progress_file_path):
    with open(progress_file_path, "r") as f:
        content = json.load(f)
        return Progress(**content)


# handle termination signal
def graceful_termination_handler(_signal, _frame):
    global TASK_RUNNING
    logger.info(f"received signal {_signal}, attempt to shut down")
    TASK_RUNNING = False


signal.signal(signal.SIGTERM, graceful_termination_handler)
signal.signal(signal.SIGINT, graceful_termination_handler)


def update_task_progress_file(
    percent_complete: int,
    progress_file_path: str,
    progress_name: str,
    metadata: dict = None,
):
    """
    Creates a progress file to notify NVCT that a new output is ready to upload

    :param percent_complete: value between 0 and 100 to demonstrate percent complete
    :param progress_file_path: progress file path
    :param progress_name: name of folder/artifact to upload to NGC
    :param metadata: a dict of additional data to include
    :return:
    """
    # as requested in comment
    if metadata is None:
        metadata = dict()

    progress = Progress(
        taskId=os.getenv("NVCT_TASK_ID", ""),
        percentComplete=percent_complete,
        name=progress_name,
        lastUpdatedAt=datetime.datetime.now(datetime.timezone.utc).strftime(
            "%Y-%m-%dT%H:%M:%S.%fZ"
        ),
        metadata=metadata,
    )

    temp_file = progress_file_path + ".tmp"
    with open(temp_file, "w") as file:
        file.write(progress.output_to_json())

    try:
        os.rename(temp_file, progress_file_path)
    except Exception as e:
        logger.exception(f"Failed to write to progress file: {e}")
        raise e


def create_file_with_specific_size(filename, size_in_bytes):
    """
    Creates a file of a specific size in bytes filled with null bytes.

    Parameters:
    - filename: Path to the file to be created.
    - size_in_bytes: Desired size of the file in bytes.
    """
    if tracer is not None:
        with tracer.start_as_current_span("create_file") as span:
            span.set_attribute("file.name", filename)
            span.set_attribute("file.size_bytes", size_in_bytes)

            with open(filename, "wb") as file:
                # Calculate how many full blocks of 4096 bytes need to be written
                full_blocks = size_in_bytes // 4096
                span.set_attribute("file.full_blocks", full_blocks)

                # Write full blocks
                for _ in range(full_blocks):
                    file.write(b"\0" * 4096)

                # Write remaining bytes if size_in_bytes is not a multiple of 4096
                remainder = size_in_bytes % 4096
                if remainder > 0:
                    span.set_attribute("file.remainder_bytes", remainder)
                    file.write(b"\0" * remainder)
    else:
        with open(filename, "wb") as file:
            # Calculate how many full blocks of 4096 bytes need to be written
            full_blocks = size_in_bytes // 4096
            # Write full blocks
            for _ in range(full_blocks):
                file.write(b"\0" * 4096)

            # Write remaining bytes if size_in_bytes is not a multiple of 4096
            remainder = size_in_bytes % 4096
            if remainder > 0:
                file.write(b"\0" * remainder)


def heartbeat(progress_file_path: str):
    """
    The task container must periodically update 'lastUpdatedAt' field
    in the progress file as a heartbeat that worker container monitors.
    The task will fail if there's no change in 'lastUpdatedAt' over 5 min.

    Parameters:
    :param progress_file_path: progress file path
    """
    while TASK_RUNNING:
        ts = datetime.datetime.now(datetime.timezone.utc).strftime(
            "%Y-%m-%dT%H:%M:%S.%fZ"
        )
        if not os.path.isfile(progress_file_path):
            logger.info("Creating progress file")
            progress = Progress(
                taskId=os.getenv("NVCT_TASK_ID", ""),
                percentComplete=0,
                name="",
                lastUpdatedAt=ts,
                metadata=dict(),
            )
            with open(progress_file_path, "w") as f:
                f.write(progress.output_to_json())

        else:
            progress = parse_progress_file(progress_file_path)
            progress.lastUpdatedAt = ts

            temp_file = progress_file_path + ".tmp"
            with open(temp_file, "w") as f:
                f.write(progress.output_to_json())

            try:
                os.rename(temp_file, progress_file_path)
            except Exception as e:
                logger.exception(f"Failed to write to progress file: {e}")
                raise e

        logger.info(f"Updated timestamp in progress file {ts}")
        time.sleep(60 + random.randint(1, 10))


def wait_for_secrets_file(file_path, max_retries=30, retry_delay=5.0):
    """
    Wait for the secrets file to exist and have content
    :param file_path: Path to the accounts secrets file
    :param max_retries: Maximum number of retries before giving up
    :param retry_delay: Delay in seconds between retries
    :return: True if file exists and has content, False otherwise
    """
    attempts = 0
    while attempts < max_retries:
        if os.path.exists(file_path):
            try:
                with open(file_path, 'r') as f:
                    content = f.read().strip()
                    if content:
                        return True
            except Exception as e:
                print(f"Error reading secrets file: {e}")

        time.sleep(retry_delay)
        attempts += 1

    return False

# temp workaround to check if collector is ready by parsing otel exporter env variables
# TO-DO: directly use otel collector health check endpoint when available
def check_collector_health() -> bool:
    try:
        health_url = os.getenv("OTEL_HEALTH_CHECK_ENDPOINT", None)
        if not health_url:
            logger.error("OTEL_HEALTH_CHECK_ENDPOINT environment variable not set")
            return False
        logger.info(f"Checking health at: {health_url}")
        max_retries, retry_delay = 6, 10 # wait for 60 seconds
        for _ in range(max_retries):
            try:
                response = requests.get(health_url)
                if response.status_code == 200:
                    logger.info(f"Collector health check successful: {health_url}")
                    return True
                logger.info(f"Collector health check failed with status {response.status_code}: {health_url}")
            except Exception as e:
                logger.error(f"Unexpected error during health check to {health_url}: {str(e)}")
            time.sleep(retry_delay)
        logger.error(f"Collector health check failed after {max_retries} attempts")
        return False
    except Exception as e:
        logger.error(f"Collector health check error: {str(e)}")
        return False

if __name__ == "__main__":
    # first wait for secrets file to be ready. BYOO requires secrets file
    ACCOUNTS_SECRETS_PATH = os.getenv("ACCOUNTS_SECRETS_PATH", "/var/secrets/accounts-secrets.json")
    if not wait_for_secrets_file(ACCOUNTS_SECRETS_PATH):
        logger.error(f"Secrets file {ACCOUNTS_SECRETS_PATH} not available after maximum retries")
        exit(1)

    # wait for BYOO collector to be ready
    if check_collector_health() == False:
        logger.error("BYOO Collector is not ready, exit")
        exit(1)

    logger.info("BYOO Collector is ready, start generating dummy results")

    # starting from here logs will be sent to the collector if configured
    init_observervavility()

    # list mounted input files for use
    input_files = {"models": INPUT_MODELS_DIR, "resources": INPUT_RESOURCES_DIR}
    output_folder_prefix = "output_result"
    output_file_prefix = "sample_file"
    input_file_list = []
    for k, p in input_files.items():
        logger.info(f"The following {k} was found:")
        for root, dirs, files in os.walk(p):
            logger.info(f"Directory: {root}")
            logger.info(f"Folders: {dirs}")
            logger.info(f"Files: {files}")
            logger.info("---")
            input_file_list += files

    add_metadata = False
    if INCLUDE_METADATA.lower() == "true":
        add_metadata = True

    while not os.path.exists(ROOT_OUTPUT_DIR):
        logger.info(f"Waiting for {ROOT_OUTPUT_DIR} to be created")
        time.sleep(2)

    t = Thread(target=heartbeat, args=(PROGRESS_FILE,), daemon=True)
    try:
        t.start()
    except Exception as e:
        logger.exception(f"Failed to start heartbeat thread: {e}")
        exit(1)

    # start generating dummy results
    for i in range(NUM_OF_RESULTS):
        try:
            metadata = dict()

            output_folder_name = f"{output_folder_prefix}_{i}"
            output_file_name = f"{output_file_prefix}_{i}.txt"
            os.makedirs(
                os.path.join(ROOT_OUTPUT_DIR, output_folder_name), exist_ok=True
            )
            if add_metadata:
                metadata["file_desc"] = "test result for iteration " + str(i + 1)
                metadata["input_files"] = input_file_list
            dummy_output_file_path = os.path.join(
                ROOT_OUTPUT_DIR, output_folder_name, output_file_name
            )

            # create file
            create_file_with_specific_size(dummy_output_file_path, FILE_SIZE_BYTES)

            # update progress file to tell NVCT that file is created
            dummy_percent_complete = int((i + 1) / NUM_OF_RESULTS * 100)

            if task_results_counter:
                task_results_counter.add(1)

            if task_progress_gauge:
                task_progress_gauge.set(dummy_percent_complete)

            logger.info(
                f"Output result: {output_folder_name}, task progress: {dummy_percent_complete}%"
            )

            time.sleep(DELAY_BETWEEN_RESULTS_IN_MINUTES * 60)

            # make sure progress file is updated LAST
            # as the task as well as BYOO Collector will be terminated once progress reaches 100%
            update_task_progress_file(
                dummy_percent_complete, PROGRESS_FILE, output_folder_name, metadata
            )
        except Exception as e:
            logger.exception(msg=f"Exception occurs: {e}")
            time.sleep(DELAY_TEARDOWN)
            exit(1)
