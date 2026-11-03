# Documentation sample scripts

These downloadable helpers support the adjacent customer upgrade guides.
Keep upgrade validators read-only and use the Python 3 standard library.
Cluster mutations belong in the guide as explicit commands.

Run the failure-case fixtures from the repository root:

```bash
python3 -m unittest discover -s docs/overview/samples/scripts -p 'test_*_upgrade_check.py'
git diff --check
```

`tools/ci/check-docs` runs these fixtures in the docs validation check.
Keep fixture credentials synthetic. Follow `docs/AGENTS.md` and the
`documentation-style` skill for public-safe prose and links.
