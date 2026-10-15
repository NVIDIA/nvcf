## uvloop-mp-test

This test image uses `sitecustomize.py` to enable `uvloop` without touching
app code. Python auto-imports `sitecustomize` on startup if it is on `sys.path`,
so placing it in `/app` makes it apply to both `app.py` and `app_simple.py`.

Notes:
- It only affects Python processes. Non-Python containers are unaffected.
- Opt out by setting `NVSNAP_DISABLE_UVLOOP=1`.
