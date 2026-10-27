# Upstream review state

Snapshot of the upstream Dynamo discovery work as last reviewed, so the review can resume
from a known point. Compare the SHAs below against the live PRs to see what moved. The
analysis itself is in `upstream-dynamo-comparison.md` (sections 1 to 8, with 1.4 covering
the changes seen on 2026-10-27).

Last checked: 2026-10-27, about 22:00 UTC.

## Local clones

| Repo | Path | Ref | SHA |
|---|---|---|---|
| ai-dynamo/dynamo | `~/projects/dynamo` | `main` (local branch, matches origin/main as fetched) | `df083f8e` |
| ai-dynamo/dynamo | `~/projects/dynamo` | `origin/global-router-v2` | `cb6b98fb` |
| ai-dynamo/dynamo | `~/projects/dynamo` | `pr-17330`, `pr-17332`, `pr-17371` (PR heads fetched as local branches) | as in the table below |
| ai-dynamo/enhancements | `~/projects/enhancements` | `pr-103` (PR head) | `aeba94e93` |

PR diffs and bodies saved during the first pass live in the session scratchpad and are not
durable; re-fetch with `gh pr diff` if needed.

## Pull requests reviewed

| PR | Author | State at check | Base | Head SHA | Last updated (UTC) | Notes |
|---|---|---|---|---|---|---|
| enhancements #103 | atchernych | open, draft | main | `aeba94e93` | 2026-10-27 16:48 | 17 commits, 0 review comments; components table rewritten 2026-10-27 |
| enhancements #99 | atchernych | open | main | `21540ef6e` | 2026-08-06 | no discovery content |
| dynamo #17208 | jioffe502 | open, approved, CI blocked | main | `7f9a3a961` | 2026-10-27 21:31 | 11 commits, 14 review comments |
| dynamo #17330 | atchernych | open, conflicts with main | main | `c7ee161c5` | 2026-10-27 18:01 | nnshah1 naming review 17:56 unanswered at check |
| dynamo #17331 | atchernych | open | #17330 | `130aaef1e` | 2026-10-27 16:48 | third review round addressed |
| dynamo #17371 | atchernych | draft | #17331 | `6db71b3f7` | 2026-10-27 16:48 | lease cap 86400 s in service flag |
| dynamo #17332 | atchernych | draft | #17371 | `896e50d6f` | 2026-10-27 16:48 | suffix flags accept leading hyphen |
| dynamo #17333 | atchernych | open | #17371 | `e6795d702` | 2026-10-27 16:48 | POST /routers 501 |
| dynamo #17412 | atchernych | draft, CI blocked | main | `96404fdd7` | 2026-10-27 17:48 | 1 commit, no reviews |
| dynamo #16963 | atchernych | open | global-router-v2 | `cf404086f` | 2026-10-21 | Karmada and MCS CDK |
| dynamo #17355 | sachalmalick | merged 2026-10-27 21:26 | main | `a10d8b916` | 2026-10-27 21:26 | empty lib/global-* crates and code ownership now on main; the other crates will rebase onto it |
| dynamo #17276 | sachalmalick | open, draft | global-router-v2 | `8e491a340` | 2026-10-25 | MCS pool models from a file |
| dynamo #17278 | sachalmalick | open, draft | codex/global-router-rr-backends | `c70a84ffe` | 2026-10-25 | discovery-backed global ingress |
| dynamo #15295 | sachalmalick | open, draft | main | `79f294935` | 2026-09-28 | Global View PoolId derivation |
| dynamo #17215 | hhzhang16 | open | main | `e02d96380` | 2026-10-27 19:16 | KV DC Relay to PoolRelay rename |

## How to refresh

```sh
# PR heads and state
for p in 17208 17330 17331 17371 17332 17333 17412 16963 17355 17276 17278 15295 17215; do
  gh api repos/ai-dynamo/dynamo/pulls/$p \
    --jq '"\(.number) \(.head.sha[0:9]) \(.state)\(if .draft then " draft" else "" end) \(.mergeable_state) updated=\(.updated_at[0:16])"'
done
gh api repos/ai-dynamo/enhancements/pulls/103 --jq '"103 \(.head.sha[0:9]) updated=\(.updated_at[0:16])"'

# New comments since the check
gh api "repos/ai-dynamo/dynamo/issues/17330/comments?since=2026-10-27T22:00:00Z" --jq '.[] | "\(.user.login) \(.created_at) \(.body[0:200])"'

# Local clone
cd ~/projects/dynamo && git fetch origin main \
  +refs/heads/global-router-v2:refs/remotes/origin/global-router-v2 \
  pull/17330/head:pr-17330 pull/17332/head:pr-17332 pull/17371/head:pr-17371
cd ~/projects/enhancements && git fetch origin pull/103/head:pr-103
```

The unauthenticated GitHub API is limited to 60 requests per hour; use `gh api`.

## Where the review stopped

- Comparison and assessment written; open questions in `upstream-dynamo-comparison.md`
  section 8 not yet answered.
- Not yet done: posting any comments upstream (v1beta1 export in #17412, region equals
  cluster name in the MCS store, registration direction evidence from Pylon).
- Not yet done: any GDS change in response to the comparison.
