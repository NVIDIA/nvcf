#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Wait for the ICMS cluster read API to advertise function admission capacity."""

import argparse
import http.client
import json
import math
from pathlib import Path
import signal
import sys
import time
import urllib.error
import urllib.parse
import urllib.request


class NoRedirect(urllib.request.HTTPRedirectHandler):
    """Reject redirects so admin authorization never follows another endpoint."""

    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def positive_seconds(value):
    seconds = float(value)
    if not math.isfinite(seconds) or seconds <= 0:
        raise argparse.ArgumentTypeError("must be finite and greater than zero")
    return seconds


def deadline_expired(_signum, _frame):
    raise TimeoutError("ICMS did not advertise the required capacity before timeout")


def wait_for_capacity(args):
    try:
        state = json.loads(Path(args.state_file).read_text())
        token = state["token"]
        if not isinstance(token, str) or not token:
            raise ValueError
    except (OSError, ValueError, KeyError, TypeError):
        raise ValueError("cannot read an admin token from the CLI state file") from None

    # Keep the token in memory and headers; never put it in argv or diagnostics.
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    deadline = time.monotonic() + args.timeout
    while True:
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise TimeoutError("ICMS did not advertise the required capacity before timeout")
        request = urllib.request.Request(
            args.url, headers={"Authorization": "Bearer " + token}
        )
        try:
            with opener.open(request, timeout=min(10, remaining)) as response:
                if response.status != 200:
                    raise urllib.error.HTTPError(
                        args.url, response.status, "unexpected response status", response.headers, None
                    )
                clusters = json.load(response)
        except urllib.error.HTTPError as error:
            if error.code < 500:
                raise ValueError(f"ICMS capacity read failed: HTTP {error.code}") from None
            clusters = []
            print(f"ICMS capacity read pending: HTTP {error.code}", flush=True)
        except (urllib.error.URLError, TimeoutError, OSError, http.client.IncompleteRead):
            clusters = []
            print("ICMS capacity read pending: transport unavailable", flush=True)
        except ValueError:
            raise ValueError("ICMS capacity response is not valid JSON") from None

        if not isinstance(clusters, list) or any(not isinstance(c, dict) for c in clusters):
            raise ValueError("ICMS capacity response must be a cluster list")
        matches = [c for c in clusters if c.get("clusterName") == args.cluster]
        if len(matches) > 1:
            raise ValueError("ICMS returned multiple clusters with the selected name")
        cluster = matches[0] if matches else {}
        status = cluster.get("status")
        available = None
        if cluster:
            if not isinstance(status, str) or not status or cluster.get("region") != args.region:
                raise ValueError("ICMS selected cluster has invalid status or unexpected region")
            gpus = cluster.get("gpus")
            if not isinstance(gpus, list) or any(not isinstance(g, dict) for g in gpus):
                raise ValueError("ICMS selected cluster has an invalid GPU registration")
            registered = any(
                g.get("name") == args.gpu
                and isinstance(g.get("instanceTypes"), list)
                and any(isinstance(i, dict) and i.get("name") == args.instance_type
                        for i in g["instanceTypes"])
                for g in gpus
            )
            if not registered:
                raise ValueError("ICMS selected cluster does not register the selected GPU and instance type")
            usage = cluster.get("gpuUsage")
            if not isinstance(usage, dict) or not isinstance(usage.get(args.gpu), dict):
                raise ValueError("ICMS selected cluster has invalid GPU usage data")
            available = usage[args.gpu].get("available")
            if type(available) is not int or available < 0:
                raise ValueError("ICMS selected cluster has invalid available GPU capacity")
        # Never echo arbitrary response strings into command logs.
        observed_status = "missing" if not cluster else "not READY"
        if status in ("READY", "UNHEALTHY"):
            observed_status = status
        print(
            f"ICMS capacity cluster={args.cluster} region={args.region} status={observed_status} "
            f"gpu={args.gpu} instance_type={args.instance_type} "
            f"available={available} minimum={args.minimum_available}",
            flush=True,
        )
        if (
            status == "READY"
            and type(available) is int
            and available >= args.minimum_available
        ):
            print("ICMS capacity prerequisite satisfied", flush=True)
            return
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise TimeoutError("ICMS did not advertise the required capacity before timeout")
        time.sleep(min(args.interval, remaining))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--url", required=True, help="ICMS account cluster-list URL")
    parser.add_argument("--state-file", required=True, help="nvcf-cli state with the admin token")
    parser.add_argument("--cluster", required=True)
    parser.add_argument("--gpu", required=True)
    parser.add_argument("--region", required=True)
    parser.add_argument("--instance-type", required=True)
    parser.add_argument("--minimum-available", required=True, type=int)
    parser.add_argument("--timeout", required=True, type=positive_seconds, help="seconds")
    parser.add_argument("--interval", required=True, type=positive_seconds, help="seconds")
    args = parser.parse_args()
    url = urllib.parse.urlsplit(args.url)
    if url.scheme not in ["http", "https"] or not url.hostname or url.username or url.password:
        parser.error("URL must use HTTP(S) without credentials")
    if (args.minimum_available < 1 or not args.cluster or not args.gpu
            or not args.region or not args.instance_type):
        parser.error("cluster and GPU must be nonempty and minimum availability positive")
    # A socket timeout alone can be extended indefinitely by a slow response body.
    signal.signal(signal.SIGALRM, deadline_expired)
    signal.setitimer(signal.ITIMER_REAL, args.timeout)
    try:
        wait_for_capacity(args)
    except (ValueError, TimeoutError) as error:
        print(str(error), file=sys.stderr)
        return 1
    finally:
        signal.setitimer(signal.ITIMER_REAL, 0)
    return 0


if __name__ == "__main__":
    sys.exit(main())
