# gRPC Streaming ASR Client

Invoke the [Nemotron ASR Streaming](https://catalog.ngc.nvidia.com/orgs/nim/nvidia/containers/nemotron-asr-streaming)
NIM over the NVCF gRPC gateway using bidirectional streaming. The
[nvidia-riva/python-clients](https://github.com/nvidia-riva/python-clients)
sample script sends a wav file and prints interim and final transcripts.

## Prerequisites

- A self-hosted NVCF cluster with a gRPC gateway exposed on port 10081
- An NGC API key that can pull `nvcr.io/nim/nvidia/nemotron-asr-streaming`
- Python 3.10+

## Deploying the ASR function

1. Edit `function-create.json` and replace `<ngc-api-key>` with an NGC
   API key that can pull `nvcr.io/nim/nvidia/nemotron-asr-streaming`.

1. Edit `function-deploy.json` and set `<backend>` to the name of a
   registered GPU cluster, `<gpu>` to the GPU type on that cluster (for
   example `A100` or `H100`), and `<instance-type>` to a matching
   instance type (for example `NCP.GPU.A100_1x`). The ASR NIM needs one
   GPU.

1. Create the function:

    ```console
    nvcf-cli function create --input-file function-create.json
    ```

    Save the function ID and version ID from the output.

1. Deploy the function:

    ```console
    nvcf-cli function deploy create <function-id> \
        --input-file function-deploy.json
    ```

1. Wait for the function to become ACTIVE. The NIM downloads its model
   profile on first boot, which can take several minutes:

    ```console
    nvcf-cli function show <function-id>
    ```

## Invoking the function

1. Install the Riva Python client and clone the sample scripts:

    ```console
    python3 -m venv .venv
    . .venv/bin/activate
    pip install nvidia-riva-client
    git clone --depth 1 https://github.com/nvidia-riva/python-clients /tmp/python-clients
    ```

1. Provide a wav file as the input. For example:

    ```console
    export INPUT_FILE=/path/to/your/audio.wav
    ```

1. Resolve the gRPC gateway and generate an invocation API key via
   `nvcf-cli`:

    ```console
    export GATEWAY_ADDR=$(kubectl get gateway nvcf-gateway -n envoy-gateway \
        -o jsonpath='{.status.addresses[0].value}')
    export NVCF_API_KEY=$(nvcf-cli api-key generate \
        --description "grpc-asr-client" --json \
        | jq -r '.keys[] | select(.service=="function") | .apiKey')
    ```

1. Transcribe the file through the gateway, routing with gRPC metadata:

    ```console
    python /tmp/python-clients/scripts/asr/transcribe_file.py \
        --server "${GATEWAY_ADDR}:10081" \
        --input-file "${INPUT_FILE}" \
        --language-code en-US \
        --show-intermediate \
        --metadata authorization "Bearer ${NVCF_API_KEY}" \
        --metadata function-id "<function-id>" \
        --metadata function-version-id "<version-id>"
    ```

The script opens a gRPC bidirectional stream (`StreamingRecognize`) to
the NVCF gateway. The gateway reads `function-id` from the metadata to
route the stream to the correct ASR pod. The script itself is a generic
Riva client and knows nothing about NVCF.
