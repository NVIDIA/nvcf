#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES.
# SPDX-License-Identifier: Apache-2.0
#
# Install k3s on one GPU node: the first server, another server, or an agent.
# Checks the node, then runs the k3s installer pinned to one release and
# verified by checksum. Labels the node nvidia.com/gpu.product from nvidia-smi.
#
# Usage:
#   sudo ./install-k3s.sh server --node-ip IP [--tls-san NAME]...
#   sudo ./install-k3s.sh server --join SERVER --token-file FILE --node-ip IP [--tls-san NAME]...
#   sudo ./install-k3s.sh agent --join SERVER --token-file FILE --node-ip IP
#
#   --node-ip IP              this node's stable IPv4 address on the management network
#   --join SERVER             address of a running server; omit only for the first server
#   --token-file FILE         file holding the cluster join token; required with --join
#   --tls-san NAME            extra API certificate name or address (servers); repeatable
#   --kubeconfig-mode MODE    mode of /etc/rancher/k3s/k3s.yaml on servers (k3s default 600)
#   --k3s-version VERSION     k3s release (default below); needs --installer-sha256
#   --installer-sha256 SHA256 checksum of install.sh at that release
#   --dry-run                 run the checks and print the install command only
#
# Exit code 0 means k3s is installed, was already installed, or the dry run
# passed. 1 means a check failed. 2 means a usage error.

set -euo pipefail

DEFAULT_VERSION="v1.36.5+k3s1"
DEFAULT_INSTALLER_SHA256="46177d4c99440b4c0311b67233823a8e8a2fc09693f6c89af1a7161e152fbfad"

usage() { awk 'NR>1 && /^#/ {sub(/^# ?/, ""); print; next} NR>1 {exit}' "$0" | sed -n '/^Install/,$p'; }
die() { echo "$1" >&2; exit 2; }

ROLE=""
NODE_IP=""
JOIN=""
TOKEN_FILE=""
KUBECONFIG_MODE=""
VERSION=""
INSTALLER_SHA256=""
DRY_RUN=0
TLS_SANS=()

[ $# -gt 0 ] || { usage >&2; exit 2; }
case "$1" in
  server|agent) ROLE="$1"; shift ;;
  -h|--help) usage; exit 0 ;;
  *) die "first argument must be server or agent: $1" ;;
esac
while [ $# -gt 0 ]; do
  case "$1" in
    --node-ip|--join|--token-file|--tls-san|--kubeconfig-mode|--k3s-version|--installer-sha256)
      [ $# -ge 2 ] || die "$1 needs a value"
      case "$1" in
        --node-ip) NODE_IP="$2" ;;
        --join) JOIN="$2" ;;
        --token-file) TOKEN_FILE="$2" ;;
        --tls-san) TLS_SANS+=("$2") ;;
        --kubeconfig-mode) KUBECONFIG_MODE="$2" ;;
        --k3s-version) VERSION="$2" ;;
        --installer-sha256) INSTALLER_SHA256="$2" ;;
      esac
      shift 2 ;;
    --dry-run) DRY_RUN=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done

ipv4_re='^([0-9]{1,3}\.){3}[0-9]{1,3}$'
host_re='^[A-Za-z0-9][-A-Za-z0-9.]*$'
[[ "$NODE_IP" =~ $ipv4_re ]] || die "--node-ip must be an IPv4 address: ${NODE_IP:-<missing>}"
if [ -n "$JOIN" ]; then
  [[ "$JOIN" =~ $host_re ]] || die "--join must be a host name or IPv4 address: $JOIN"
  [ -n "$TOKEN_FILE" ] || die "--join needs --token-file"
else
  [ "$ROLE" = server ] || die "an agent needs --join SERVER and --token-file FILE"
  [ -z "$TOKEN_FILE" ] || die "--token-file is only used with --join"
fi
for san in ${TLS_SANS+"${TLS_SANS[@]}"}; do
  [[ "$san" =~ $host_re ]] || die "--tls-san must be a host name or IPv4 address: $san"
done
if [ "$ROLE" = agent ]; then
  [ "${#TLS_SANS[@]}" -eq 0 ] || die "--tls-san applies to servers only"
  [ -z "$KUBECONFIG_MODE" ] || die "--kubeconfig-mode applies to servers only"
fi
if [ -n "$KUBECONFIG_MODE" ]; then
  [[ "$KUBECONFIG_MODE" =~ ^[0-7]{3,4}$ ]] || die "--kubeconfig-mode must be octal, such as 600"
  [ $((8#$KUBECONFIG_MODE & 2)) -eq 0 ] || die "--kubeconfig-mode must not let other users write the kubeconfig: $KUBECONFIG_MODE"
fi
if [ -z "$VERSION" ] || [ "$VERSION" = "$DEFAULT_VERSION" ]; then
  VERSION="$DEFAULT_VERSION"
  INSTALLER_SHA256="${INSTALLER_SHA256:-$DEFAULT_INSTALLER_SHA256}"
fi
[[ "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+\+k3s[0-9]+$ ]] || die "--k3s-version must look like $DEFAULT_VERSION: $VERSION"
[[ "$INSTALLER_SHA256" =~ ^[0-9a-f]{64}$ ]] || die "--k3s-version $VERSION needs --installer-sha256 with the SHA-256 of its install.sh"

PASS=0
WARN=0
FAIL=0
pass() { printf '  PASS  %s\n' "$1"; PASS=$((PASS + 1)); }
warn() { printf '  WARN  %s\n' "$1"; WARN=$((WARN + 1)); }
fail() { printf '  FAIL  %s\n' "$1"; FAIL=$((FAIL + 1)); }

if [ "$DRY_RUN" -eq 0 ] && [ "$(id -u)" -ne 0 ]; then
  die "run as root: sudo $0 ..."
fi
[ "$(uname -s)" = Linux ] || die "install-k3s.sh runs on the Linux node that joins the cluster"
command -v systemctl >/dev/null 2>&1 || die "k3s needs systemd; systemctl was not found"

# An existing install is left alone. Reinstalling with different flags means
# uninstalling first, which deletes the node's k3s data.
if command -v k3s >/dev/null 2>&1; then
  installed=$(k3s --version 2>/dev/null | awk 'NR==1 {print $3}' || true)
  for service in k3s k3s-agent; do
    if systemctl is-active --quiet "$service"; then
      running_role=server
      [ "$service" = k3s ] || running_role=agent
      if [ "$running_role" != "$ROLE" ]; then
        echo "k3s is already running here as $running_role ($service), not $ROLE. No changes made." >&2
        echo "To change the role, delete the node from the cluster, then run /usr/local/bin/$service-uninstall.sh." >&2
        exit 1
      fi
      echo "k3s ${installed:-<unknown version>} is already installed and $service is running. No changes made."
      [ "$installed" = "$VERSION" ] || echo "This script installs $VERSION. Keep every node on the same k3s version."
      echo "To reinstall, run /usr/local/bin/$service-uninstall.sh first. It deletes this node's k3s data."
      exit 0
    fi
  done
  echo "k3s ${installed:-<unknown version>} is installed but neither k3s nor k3s-agent is running." >&2
  echo "Start the service, or run its uninstall script under /usr/local/bin before installing again." >&2
  exit 1
fi

echo "Checking $(hostname) for a k3s $ROLE install"

# The flannel interface is the one holding --node-ip, so cluster traffic stays
# on the management network and off any high-speed links reserved for models.
IFACE=""
addr_line=$(ip -o -4 addr show 2>/dev/null | awk -v ip="$NODE_IP" '{split($4, a, "/"); if (a[1] == ip) {print; exit}}' || true)
if [ -z "$addr_line" ]; then
  fail "$NODE_IP is not assigned to any interface on this node"
else
  IFACE=$(echo "$addr_line" | awk '{print $2}')
  case " $addr_line " in
    *" dynamic "*) warn "$NODE_IP on $IFACE is DHCP-assigned. Reserve it for this node so it never changes." ;;
    *) pass "$NODE_IP is a static address on $IFACE" ;;
  esac
fi
case "$NODE_IP" in
  10.42.*|10.43.*) fail "$NODE_IP is inside the k3s default pod (10.42.0.0/16) or service (10.43.0.0/16) network" ;;
esac

if ! command -v timedatectl >/dev/null 2>&1; then
  warn "timedatectl not found; confirm the clock is synchronized with NTP"
elif [ "$(timedatectl show -p NTPSynchronized --value 2>/dev/null || true)" = yes ]; then
  pass "clock synchronized"
else
  fail "clock not synchronized. Run 'timedatectl set-ntp true' and wait for 'System clock synchronized: yes'"
fi

GPU_PRODUCT=""
if gpu_names=$(nvidia-smi --query-gpu=name --format=csv,noheader 2>/dev/null) && [ -n "$gpu_names" ]; then
  distinct=$(echo "$gpu_names" | sort -u)
  if [ "$(echo "$distinct" | grep -c .)" -ne 1 ]; then
    fail "GPU models differ on this node: $(echo "$distinct" | paste -sd, -)"
  else
    # The same form GPU Feature Discovery uses: drop other characters, then
    # join the words with dashes.
    GPU_PRODUCT=$(echo "$distinct" | tr -cd 'A-Za-z0-9._ -' | awk '{$1 = $1; gsub(/ /, "-"); print}')
    pass "$(echo "$gpu_names" | grep -c .) x $distinct, label nvidia.com/gpu.product=$GPU_PRODUCT"
  fi
else
  fail "nvidia-smi found no GPU. Install the NVIDIA driver for this hardware first"
fi

# k3s configures containerd for the NVIDIA runtime and creates the nvidia
# RuntimeClass only if the toolkit is present when k3s starts.
if command -v nvidia-container-runtime >/dev/null 2>&1; then
  pass "NVIDIA Container Toolkit installed"
else
  fail "nvidia-container-runtime not found. Install the NVIDIA Container Toolkit before k3s"
fi

if [ "$ROLE" = server ]; then
  ports="6443/tcp, 2379-2380/tcp, 8472/udp and 10250/tcp"
else
  ports="8472/udp and 10250/tcp"
fi
rules="allow $ports between cluster nodes, and traffic from the pod and service networks 10.42.0.0/16 and 10.43.0.0/16"
firewall=""
if command -v ufw >/dev/null 2>&1; then
  case "$(ufw status 2>&1 || true)" in
    *"Status: active"*) firewall="ufw" ;;
    *"Status: inactive"*) ;;
    *) warn "could not read ufw status; if it is active, $rules" ;;
  esac
fi
if command -v firewall-cmd >/dev/null 2>&1 && [ "$(firewall-cmd --state 2>/dev/null || true)" = running ]; then
  firewall="${firewall:+$firewall and }firewalld"
fi
if [ -n "$firewall" ]; then
  warn "$firewall is active: $rules"
else
  pass "no active ufw or firewalld"
fi

TOKEN=""
if [ -n "$JOIN" ]; then
  if [ -f "$TOKEN_FILE" ] && [ -r "$TOKEN_FILE" ]; then
    TOKEN=$(tr -d '[:space:]' < "$TOKEN_FILE")
  fi
  if [ -n "$TOKEN" ]; then
    pass "join token read from $TOKEN_FILE"
  else
    fail "cannot read a join token from $TOKEN_FILE"
  fi
  # shellcheck disable=SC2016  # $1 expands in the child shell
  if timeout 5 bash -c 'exec 3<>"/dev/tcp/$1/6443"' _ "$JOIN" 2>/dev/null; then
    pass "server $JOIN answers on 6443/tcp"
  else
    fail "cannot reach $JOIN on 6443/tcp. Check the address, the server and any firewall"
  fi
fi

ARGS=("$ROLE")
if [ "$ROLE" = server ] && [ -z "$JOIN" ]; then
  # Embedded etcd, so more servers can join later without a reinstall.
  ARGS+=(--cluster-init)
fi
[ -z "$JOIN" ] || ARGS+=(--server "https://$JOIN:6443")
ARGS+=(--node-ip "$NODE_IP" --node-external-ip "$NODE_IP")
[ -z "$IFACE" ] || ARGS+=(--flannel-iface "$IFACE")
[ -z "$GPU_PRODUCT" ] || ARGS+=(--node-label "nvidia.com/gpu.product=$GPU_PRODUCT")
if [ "$ROLE" = server ]; then
  ARGS+=(--tls-san "$NODE_IP")
  for san in ${TLS_SANS+"${TLS_SANS[@]}"}; do ARGS+=(--tls-san "$san"); done
  [ -z "$KUBECONFIG_MODE" ] || ARGS+=(--write-kubeconfig-mode "$KUBECONFIG_MODE")
fi

INSTALLER_URL="https://raw.githubusercontent.com/k3s-io/k3s/${VERSION/+/%2B}/install.sh"
printf '\n%d passed, %d warnings, %d failed\n' "$PASS" "$WARN" "$FAIL"
if [ "$FAIL" -gt 0 ]; then
  echo "Fix the failed checks, then run this again." >&2
  exit 1
fi

printf '\nInstall command:\n  INSTALL_K3S_VERSION=%s' "$VERSION"
[ -z "$JOIN" ] || printf ' K3S_TOKEN=<from %s>' "$TOKEN_FILE"
printf ' sh install.sh'
printf ' %q' "${ARGS[@]}"
printf '\n  install.sh: %s\n  sha256: %s\n' "$INSTALLER_URL" "$INSTALLER_SHA256"
if [ "$DRY_RUN" -eq 1 ]; then
  echo "Dry run: nothing installed."
  exit 0
fi

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
curl -fsSL --retry 3 -o "$WORK/install.sh" "$INSTALLER_URL" || { echo "could not download $INSTALLER_URL" >&2; exit 1; }
if ! printf '%s  %s\n' "$INSTALLER_SHA256" "$WORK/install.sh" | sha256sum -c --quiet -; then
  echo "install.sh checksum mismatch. Not installing." >&2
  exit 1
fi

# The installer writes K3S_TOKEN to the service environment file, mode 600,
# so the token never appears in the systemd unit or the process list.
if [ -n "$JOIN" ]; then
  INSTALL_K3S_VERSION="$VERSION" K3S_TOKEN="$TOKEN" sh "$WORK/install.sh" "${ARGS[@]}"
else
  INSTALL_K3S_VERSION="$VERSION" sh "$WORK/install.sh" "${ARGS[@]}"
fi

if [ "$ROLE" = agent ]; then
  systemctl is-active --quiet k3s-agent || { echo "k3s-agent is not running. See: journalctl -u k3s-agent" >&2; exit 1; }
  echo
  echo "Agent installed. Delete the token file: rm $TOKEN_FILE"
  echo "Check the node from a server or your workstation: kubectl get nodes -o wide"
  exit 0
fi

node=$(hostname | tr '[:upper:]' '[:lower:]')
ready=0
for _ in $(seq 60); do
  if [ "$(k3s kubectl get node "$node" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || true)" = True ]; then
    ready=1
    break
  fi
  sleep 2
done
k3s kubectl get nodes -o wide || true
if [ "$ready" -ne 1 ]; then
  echo "Node $node is not Ready after 2 minutes. See: journalctl -u k3s" >&2
  exit 1
fi
echo
if [ -n "$JOIN" ]; then
  echo "Server joined. Delete the token file: rm $TOKEN_FILE"
else
  echo "First server installed. The join token is in /var/lib/rancher/k3s/server/node-token. Keep it private."
  echo "Join other nodes with this script and --join $NODE_IP."
fi
