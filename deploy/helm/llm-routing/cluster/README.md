# Prepare a k3s GPU cluster

Build a k3s cluster from your own GPU machines, then continue with the [shared gateway guide](../README.md#prerequisites). Skip this guide if you already have a Kubernetes cluster that passes `llm.py preflight`.

Run workstation commands from `deploy/helm/llm-routing`. The examples use nodes `node1` to `node3` at `192.0.2.10` to `192.0.2.12`, and the API name `k3s.example.com`. Replace them with your own.

## Choose a layout

k3s servers run the Kubernetes control plane and embedded etcd. Agents run workloads only. In this guide every node, server or agent, also runs models.

- One node: one server.
- Two nodes: one server and one agent. Do not use two servers. Two etcd members both need to be up for quorum, so losing either node stops the API.
- Three or more nodes: three servers, then agents. The API keeps working when one server fails.

## Node requirements

Each node needs:

- An operating system, kernel and NVIDIA driver supported for its GPU, with `nvidia-smi` listing the GPUs.
- The [NVIDIA Container Toolkit](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/install-guide.html), installed before k3s. k3s detects `nvidia-container-runtime` only when it starts, and then creates the `nvidia` RuntimeClass. If you install the toolkit later, restart k3s on that node.
- One IPv4 address that never changes: a static address or a DHCP reservation. k3s registers each node by address, servers join etcd by address, and the API certificate lists the address. A node whose address changes drops out of the cluster. The address must be outside 10.42.0.0/16 and 10.43.0.0/16, the k3s pod and service networks.
- A unique hostname. k3s uses it as the node name.
- A clock synchronized with NTP. etcd and certificate checks depend on it. `timedatectl` reports `System clock synchronized: yes`.
- If a host firewall is active, these ports open between nodes: 6443/tcp (Kubernetes API), 2379-2380/tcp (etcd, servers only), 8472/udp (flannel VXLAN) and 10250/tcp (kubelet). Also allow traffic from the pod network 10.42.0.0/16 and the service network 10.43.0.0/16.
- systemd, sudo, and outbound HTTPS to GitHub to download k3s, plus the outbound access in the [cluster prerequisites](../README.md#cluster).

Give k3s each node's management network address. If a node also has high-speed links for GPU traffic, keep them for [two-node models](#two-node-models).

## Install the first server

Copy the install script to the first server, then run it there:

```bash
scp cluster/install-k3s.sh node1:
ssh -t node1 sudo ./install-k3s.sh server --node-ip 192.0.2.10 --tls-san k3s.example.com
```

The script checks the node first and stops if a check fails:

- `--node-ip` is assigned to an interface on the node, outside the k3s pod and service networks. A DHCP-assigned address gives a warning: confirm that it is reserved.
- The clock is synchronized.
- `nvidia-smi` lists GPUs, all of one model.
- The NVIDIA Container Toolkit is installed.
- An active ufw or firewalld gives a warning that lists the ports and networks to allow.
- With `--join`, the token file is readable and the server answers on 6443/tcp.

It then downloads the k3s installer for release `v1.36.5+k3s1`, verifies its SHA-256 checksum, and installs k3s with:

- `--cluster-init` on the first server, for embedded etcd. More servers can join later without a reinstall.
- `--node-ip` and `--node-external-ip` set to the address, and `--flannel-iface` set to the interface that holds it.
- `--tls-san` for the node address and each name you pass. Pass every name you will use to reach the API from your workstation.
- The node label `nvidia.com/gpu.product` from `nvidia-smi`, such as `NVIDIA-GB10`. Recipes match it against their hardware profiles. k3s applies it when the node first registers.

Add `--dry-run` to run the checks and print the install command without installing. If k3s is already running, the script changes nothing. `--help` lists all options, including `--k3s-version` with `--installer-sha256` for another release.

The server kubeconfig, `/etc/rancher/k3s/k3s.yaml`, keeps the k3s default mode 600. Pass `--kubeconfig-mode 644` only if every user on the node may administer the cluster.

## Join more nodes

Copy the join token from the first server to the new node, readable only by you:

```bash
# On node1: print the token
sudo cat /var/lib/rancher/k3s/server/node-token

# On the new node: paste the token, then press Ctrl-D
(umask 077 && cat > ~/k3s-token)
```

Copy `cluster/install-k3s.sh` to the new node. On that node, join it as an agent, or as a server:

```bash
sudo ./install-k3s.sh agent --join 192.0.2.10 --token-file ~/k3s-token --node-ip 192.0.2.11

sudo ./install-k3s.sh server --join 192.0.2.10 --token-file ~/k3s-token \
  --node-ip 192.0.2.12 --tls-san k3s.example.com
```

The k3s installer stores the token in the service environment file, mode 600. Delete `~/k3s-token` once the node has joined.

- Join every node with the same k3s version. An agent must not run a newer version than its servers.
- Add the second and third servers one after the other. Until the third joins, the cluster stops if either server fails.

## Connect from your workstation

Copy the kubeconfig from a server. The second argument must be the server address or one of its `--tls-san` names:

```bash
cluster/fetch-kubeconfig.sh node1 k3s.example.com my-cluster
export KUBECONFIG="$HOME/.kube/config:$HOME/.kube/my-cluster.yaml"
```

It writes `~/.kube/my-cluster.yaml` with context `my-cluster`, then runs `kubectl get nodes`. If your ssh user cannot read `/etc/rancher/k3s/k3s.yaml` on the server, the script asks for your sudo password there. The file grants cluster-admin. Keep it mode 600.

## Enable GPU scheduling

Install the NVIDIA device plugin, which advertises `nvidia.com/gpu` on each node:

```bash
kubectl --context my-cluster apply -f cluster/nvidia-device-plugin.yaml
kubectl --context my-cluster get nodes -o custom-columns='NAME:.metadata.name,GPUS:.status.capacity.nvidia\.com/gpu,PRODUCT:.metadata.labels.nvidia\.com/gpu\.product'
```

Within a minute each node shows its GPU count and product. The manifest pins plugin `v0.17.4`, the minimum for GB10.

Check that pods get GPUs and run CUDA on every node:

```bash
cluster/validate-cluster.sh --context my-cluster --expect-nodes 3
```

It runs `nvidia-smi` in a pod on each node and checks that every node reports a distinct GPU. It checks that an oversized GPU request stays pending, then compiles and runs a CUDA kernel. The CUDA image is several GB. `--quick` skips that check. Test pods run in namespace `default` and are deleted on exit.

## Continue

Check the cluster against the shared stack prerequisites:

```bash
python3 llm.py --context my-cluster preflight --values dev-artifacts/values.yaml
```

k3s provides the `nvidia` RuntimeClass, the `local-path` StorageClass and Traefik ingress. The preflight still fails until you [prepare and preload](../dev-artifacts/README.md) the shared stack images on each node. Then continue with [1. Install shared infrastructure](../README.md#1-install-shared-infrastructure).

## Two-node models

Some recipe profiles split one model across two nodes, such as the [Flash-Next](../ADVANCED.md#helm-flash-next-recipes) two-node profile. Its two nodes need a direct high-speed link in addition to the management network:

- Connect and address the high-speed ports following your hardware documentation. Give each port a static address.
- Keep `--node-ip` and flannel on the management network.
- The model pods use host networking and send GPU traffic over TCP on the interface you record. They do not need RDMA.

Record each node's high-speed interface, address and link speed in a capabilities file. Start from [capabilities.example.json](../recipes/capabilities.example.json), and give both nodes the same `fabric` name. On each node:

```bash
ip -brief -4 address show
cat /sys/class/net/IFACE/speed    # Mb/s: 200000 is 200 Gb/s
ping -c 3 -I IFACE PEER_ADDRESS
```

Then plan and install with that file, as in [Flash-Next recipes](../ADVANCED.md#helm-flash-next-recipes).

## Remove a node

Uninstall the models whose caches are on that node first. See [Remove downloaded model files](../ADVANCED.md#remove-downloaded-model-files).

Delete the Node from the cluster before stopping k3s on it:

```bash
kubectl --context my-cluster delete node node3

# Then on node3, for a server
sudo /usr/local/bin/k3s-uninstall.sh
# or for an agent
sudo /usr/local/bin/k3s-agent-uninstall.sh
```

Removing a server changes etcd membership, which needs quorum. Stopping the server first can lose quorum: with two servers, the API stops. To replace servers, add the new ones before removing the old ones. Uninstalling deletes the node's k3s data, including its `local-path` volumes.
