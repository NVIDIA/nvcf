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
import subprocess
import random
import datetime
import time
import json
import signal
from dataclasses import dataclass
from threading import Thread

# provided by service
ROOT_OUTPUT_DIR = os.getenv("NVCT_RESULTS_DIR", "/var/task/result")
PROGRESS_FILE = os.getenv("NVCT_PROGRESS_FILE_PATH", ROOT_OUTPUT_DIR + "/progress")

# task run status
TASK_RUNNING = True

TEST_EXECUTABLE_FILE_PATH = "/opt/nccl-tests/build/all_reduce_perf"


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
    def __init__(self,
                 taskId: str,
                 percentComplete: int,
                 name: str,
                 lastUpdatedAt: str,
                 metadata: dict):
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
    print(f"received signal {_signal}, attempt to shut down")
    TASK_RUNNING = False


signal.signal(signal.SIGTERM, graceful_termination_handler)
signal.signal(signal.SIGINT, graceful_termination_handler)


def update_task_progress_file(percent_complete: int, progress_file_path: str,
                              progress_name: str, metadata: dict = None):
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
        lastUpdatedAt=datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%fZ"),
        metadata=metadata
    )

    temp_file = progress_file_path + ".tmp"
    with open(temp_file, 'w') as file:
        file.write(progress.output_to_json())

    try:
        os.rename(temp_file, progress_file_path)
    except Exception as e:
        print(f"Failed to write to progress file: {e}")
        raise e


def create_file_with_specific_size(filename, size_in_bytes):
    """
    Creates a file of a specific size in bytes filled with null bytes.

    Parameters:
    - filename: Path to the file to be created.
    - size_in_bytes: Desired size of the file in bytes.
    """
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
        ts = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%fZ")
        if not os.path.isfile(progress_file_path):
            print("Creating progress file")
            progress = Progress(
                taskId=os.getenv("NVCT_TASK_ID", ""),
                percentComplete=0,
                name="",
                lastUpdatedAt=ts,
                metadata=dict()
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
                print(f"Failed to write to progress file: {e}")
                raise e

        print(f"Updated timestamp in progress file {ts}")
        time.sleep(60+random.randint(1,10))


def env_flag_enabled(name: str, default: bool = True) -> bool:
    value = os.getenv(name)
    if value is None:
        return default
    return value.strip().lower() not in ("0", "false", "no", "")


def check_gpu_availability(gpu_count: int) -> str:
    """
    Check GPU availability using nvidia-smi.

    Returns:
        str: The nvidia-smi output

    Raises:
        HTTPException: If nvidia-smi fails or GPUs are not available
    """
    print("Checking GPU availability with nvidia-smi...")
    try:
        gpu_info = subprocess.check_output("nvidia-smi", shell=True, text=True)
        print(f"nvidia-smi output:\n{gpu_info}")
        print("nvidia-smi executed successfully, GPU is available and drivers are installed correctly.")
    except subprocess.CalledProcessError as e:
        error_msg = f"nvidia-smi failed: {str(e)}\nOutput: {e.output if hasattr(e, 'output') else 'N/A'}"
        print(error_msg)
        raise Exception(error_msg)

    print("Checking number of GPUs...")
    try:
        gpu_list = subprocess.check_output("nvidia-smi -L", shell=True, text=True)
        print(f"nvidia-smi -L output:\n{gpu_list}")
        print("nvidia-smi -L executed successfully, GPU is available and drivers are installed correctly.")
        detected_gpu_count = len([line for line in gpu_list.splitlines() if line.strip()])
        if detected_gpu_count != gpu_count:
            raise Exception(f"Number of GPUs is not equal to {gpu_count}, {gpu_list}")
    except subprocess.CalledProcessError as e:
        error_msg = f"nvidia-smi -L failed: {str(e)}\nOutput: {e.output if hasattr(e, 'output') else 'N/A'}"
        print(error_msg)
        raise Exception(error_msg)
    print(f"Number of GPUs is equal to {gpu_count}")
    return gpu_info



def test(np: int = 0, b: int = 8, e: str = "128M", f: int = 2, g: int = 1, n: int = 100, debug: bool = False, mnnvl: bool = False, npernode: int = 4, cluster_type: str = "efa"):
    check_gpu_availability(npernode)
    if np > 1:
        command = (f"/opt/amazon/openmpi/bin/mpirun --allow-run-as-root --debug-devel -bind-to none -mca plm_rsh_agent ssh_helper "
                    f"--mca pml ^cm,ucx --mca btl tcp,self --mca btl_tcp_if_exclude lo,docker0,veth_def_agent "

                    f"-x LD_LIBRARY_PATH=/opt/amazon/openmpi/lib:/opt/nccl/build/lib:/opt/amazon/efa/lib:/opt/aws-ofi-nccl/install/lib:/usr/local/nvidia/lib:/opt/amazon/ofi-nccl/lib/aarch64-linux-gnu "
                    f"-x PATH=$PATH:/opt/amazon/efa/bin:/usr/bin "

                    f'{"-x FI_PROVIDER=efa -x FI_EFA_USE_DEVICE_RDMA=1 -x FI_EFA_FORK_SAFE=1 " if cluster_type == "efa" else ""}' # for efa/AWS

                    f'{"-x NCCL_DEBUG=INFO " if debug else ""}'
                    f'{"-x NCCL_MNNVL_ENABLE=1 " if mnnvl else ""}'
                    f"-np {np} -npernode {npernode} --hostfile $HOSTFILE -- "
                    f"{TEST_EXECUTABLE_FILE_PATH} -n {n} -b {b} -e {e} -f {f} -g {g}")
    else:
        command = f"{TEST_EXECUTABLE_FILE_PATH} -n {n} -b {b} -e {e} -f {f} -g {g}"

    print("Running: ", command, flush=True)
    return command

if __name__ == "__main__":
    update_task_progress_file(1, PROGRESS_FILE, "test")

    t = Thread(target=heartbeat, args=(PROGRESS_FILE,), daemon=True)
    try:
        t.start()
    except Exception as e:
        print(f"Failed to start heartbeat thread: {e}")
        exit(1)

    command = test(
        np=int(os.getenv("TOTAL_NUM_GPUS", 8)),
        b=int(os.getenv("MIN_BYTES", 1024)),
        e=os.getenv("MAX_BYTES", "16G"),
        f=int(os.getenv("STEPFACTOR", 2)),
        g=int(os.getenv("NUM_GPUS_PER_THREAD", 1)),
        n=int(os.getenv("N", 20)),
        debug=env_flag_enabled("DEBUG"),
        mnnvl=env_flag_enabled("MNNVL"),
        npernode=int(os.getenv("NPERNODE", 4)),
        cluster_type=os.getenv("CLUSTER_TYPE", "efa"),
    )

    try:
        command_output = subprocess.check_output(command, shell=True, text=True)
    except subprocess.CalledProcessError as e:
        print(f"Command failed with error: {e}")
        print(f"Command output: {e.output}")
        # Update progress file with error information before exiting
        update_task_progress_file(1, PROGRESS_FILE, "test", metadata={
            "command_output": e.output,
            "error": str(e),
            "status": "failed"
        })
        exit(1)

    print(command_output)

    # update progress file that the task was successful
    update_task_progress_file(100, PROGRESS_FILE, "test", metadata={
        "command_output": command_output,
        "status": "success"
    })

    print("Task completed successfully")
    time.sleep(600)

    exit(0)
