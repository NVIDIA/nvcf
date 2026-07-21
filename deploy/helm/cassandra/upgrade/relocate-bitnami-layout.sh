#!/usr/bin/env sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# PROTOTYPE. One-time, in-place relocation of a Bitnami-nested Cassandra data
# volume to the OSS Cassandra layout, so the volume no longer needs a subPath
# and every future upgrade is a normal deploy.
#
# Bitnami stored data nested under the mount:
#   <DATA_DIR>/data/{data, commitlog, hints, saved_caches}
# OSS Cassandra uses:
#   <DATA_DIR>/{data, commitlog, hints, saved_caches}
# This moves the nested contents up one level. It is idempotent (a second run
# is a no-op), guarded (refuses ambiguous state), and dry-run by default.
#
# Env:
#   DATA_DIR  mounted volume path (default /var/lib/cassandra)
#   DRY_RUN   "true" prints the plan and changes nothing (default true)

set -eu
DATA_DIR="${DATA_DIR:-/var/lib/cassandra}"
DRY_RUN="${DRY_RUN:-true}"

log() { printf '[relocate] %s\n' "$*"; }
die() { printf '[relocate][error] %s\n' "$*" >&2; exit 1; }

[ -d "$DATA_DIR" ] || die "DATA_DIR $DATA_DIR does not exist"

nested="$DATA_DIR/data"

# Signature of the Bitnami nested layout: a data volume mounted at DATA_DIR
# whose data/ subdirectory itself contains data/ and commitlog/. The OSS layout
# has keyspace directories under data/ and never a data/commitlog or data/data.
if [ -d "$nested/data" ] && [ -d "$nested/commitlog" ]; then
  # Ambiguity guard: the OSS layout would also put commitlog at the top level.
  # If both exist, the volume is in an unexpected state; do not touch it.
  if [ -d "$DATA_DIR/commitlog" ]; then
    die "ambiguous layout: both $DATA_DIR/commitlog and $nested/commitlog exist. Refusing."
  fi

  log "detected Bitnami nested layout under $nested"
  if [ "$DRY_RUN" = "true" ]; then
    for e in "$nested"/*; do
      [ -e "$e" ] || continue
      log "would move: $e -> $DATA_DIR/$(basename "$e")"
    done
    log "DRY RUN: no changes made. Set DRY_RUN=false to execute."
    exit 0
  fi

  # Preflight every destination BEFORE touching the volume, so a collision
  # aborts with the volume untouched and a rerun is still idempotent. The child
  # data/ is the one exception: its destination is DATA_DIR/data, which IS the
  # nested parent renamed out of the way below, so it is never a real collision.
  for e in "$nested"/* "$nested"/.[!.]*; do
    [ -e "$e" ] || continue
    b="$(basename "$e")"
    [ "$DATA_DIR/$b" = "$nested" ] && continue
    [ -e "$DATA_DIR/$b" ] && die "target $DATA_DIR/$b already exists; aborted before touching the volume"
  done

  # Rename the nested parent so its child data/ does not collide with the
  # DATA_DIR/data target, then move each child up. Nothing is deleted.
  tmp="$DATA_DIR/.bitnami-relocate.$$"
  mv "$nested" "$tmp"
  for e in "$tmp"/* "$tmp"/.[!.]*; do
    [ -e "$e" ] || continue
    b="$(basename "$e")"
    mv "$e" "$DATA_DIR/$b"
  done
  rmdir "$tmp" 2>/dev/null || die "leftover files in $tmp after move; inspect manually"
  log "relocation complete: $DATA_DIR now uses the OSS layout"
else
  log "no Bitnami nested layout under $DATA_DIR (already OSS, or empty); nothing to do"
fi
