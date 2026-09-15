# Cred Helper Example

1. Update credentials in files with live ones (worker image pull, ECR, VolcEngine, etc.)

1. Install the example:

    ```sh
    ./examples/run.sh
    ```

1. Apply the test puller pod:

    ```sh
    kubectl apply -n image-credential-helper-test ./examples/pod.yaml
    ```

1. Ensure pod has pulled images

    ```console
    $ kubectl get pod/pull-test -n image-credential-helper-test2
    NAME        READY   STATUS             RESTARTS   AGE
    pull-test   2/2     Running            0          1m
    ```
