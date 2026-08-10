# NVIDIA Cloud Task Worker Container

`worker-task` runs alongside the task container: it monitors task progress via a
shared directory and, under the `UPLOAD` result-handling strategy, uploads task
results to NGC using the caller-supplied API key.

## Building

### Binary

```bash
# With Bazel
bazel build //src/compute-plane-services/worker-task/cmd:worker-task

# Or with the Go toolchain
$ cd cmd && go build
```

### Container image

```bash
bazel build //src/compute-plane-services/worker-task/cmd:image
```

## Testing

```bash
# With Bazel
bazel test //src/compute-plane-services/worker-task/...

# Or with the Go toolchain
$ go test ./... -v -race
```

## Running Locally

### Setup

#### Shared Directory

Create a directory used to share data between `worker-task` and the task
container.

```bash
$ mkdir ~/nvct-shared-dir
```

Create a directory used to store secrets consumed by `worker-task`.

```bash
$ mkdir ~/nvct-secrets-dir
```

Create `secrets.json` in `~/nvct-secrets-dir` and add your NGC API key. The key
is used to upload results to the NGC private registry.

```json
{
    "NGC_API_KEY": "<YOUR-API-KEY>"
}
```

#### Mock NVCT Server

Start the mock NVCT server for communication with `worker-task`.

```bash
$ bazel run //src/libraries/go/worker/test/cmd/nvctserver
```

### Run worker-task

Prepare an env file `worker-task.env` for container configuration.

```bash
NCA_ID=test-nca
ACCOUNT_NAME=test-account
ICMS_ENVIRONMENT=stage
INSTANCE_ID=test-instance-id
INSTANCE_TYPE=test-instance-type
TASK_ID=10b076eb-b6d2-4cd9-878b-a3614a931570
TASK_NAME=test-task
NVCT_WORKER_TOKEN=<JWT with iat claim>
NVCT_FQDN=http://localhost:9091
NVCT_FQDN_GRPC=http://127.0.0.1:9091
OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:8360
TERMINATION_GRACE_PERIOD=PT15M
NVCT_RESULT_HANDLING_STRATEGY=UPLOAD
RESULTS_LOCATION=<ngc org>/<model name>
NVCT_MAX_RUN_TIME_DURATION=PT3M
NVCT_RESULTS_DIR=/var/task/result
NVCT_PROGRESS_FILE_PATH=/var/task/result/progress
POLL_PROGRESS=false
```

With the environment set up and the image built locally, run:

```bash
$ docker run -it --rm --net=host \
    --env-file=worker-task.env \
    -v ~/nvct-shared-dir:/var/task \
    -v ~/nvct-secrets-dir:/var/secrets \
    nvcf-worker-task:local
```

## Interaction

Once everything is running, write results to `~/nvct-shared-dir` for
`worker-task` to process.

### Manually Write Results

1. Create a folder for the checkpoint result.

```bash
$ cd ~/nvct-shared-dir/result
$ mkdir <result-name>
```

2. Generate a random file of a certain size in that folder.

```bash
$ cd ~/nvct-shared-dir/result/<result-name>
$ dd if=/dev/urandom of=result_file bs=1M count=100
```

3. Create (or update) the progress file `~/nvct-shared-dir/result/progress`:

```json
{
  "taskId": "10b076eb-b6d2-4cd9-878b-a3614a931570",
  "percentComplete": 20,
  "name": "<result-name>",
  "metadata": {
    "message": "test result"
  }
}
```

`worker-task` picks up the progress updates and sends results to the NVCT
server. To complete the task, set `percentComplete` to `100`.
