-- SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
-- SPDX-License-Identifier: Apache-2.0
--
-- Unit tests for files/lua/safe-range.lua. Loads the real file with a stubbed
-- `ngx` so the shipped code is exercised exactly as nginx runs it, with no
-- test-only branches in production code.
--
--   luajit tests/range-validator-test.lua [path/to/safe-range.lua]

local lua_path = arg and arg[1] or "deploy/files/lua/safe-range.lua"

local function run(input)
  local cleared = false
  _G.ngx = {
    var = { http_range = input },
    req = { clear_header = function(name)
      if name == "Range" then cleared = true end
    end },
  }
  local chunk = assert(loadfile(lua_path))
  local returned = chunk()
  return returned, cleared
end

-- input, expected return, expected "was the header cleared", why it matters
local cases = {
  -- The reported failure: NGC CLI computes last-byte-pos as 0 - 1.
  { "bytes=0--1",        "",                  true,  "the reported NGC CLI failure" },
  -- Valid forms must survive untouched, or we break every real download.
  { "bytes=0-0",         "bytes=0-0",         false, "single byte" },
  { "bytes=0-99",        "bytes=0-99",        false, "bounded range" },
  { "bytes=900-",        "bytes=900-",        false, "open-ended range" },
  { "bytes=-100",        "bytes=-100",        false, "suffix range" },
  { "bytes=0-9,20-29",   "bytes=0-9,20-29",   false, "multi-range" },
  { "bytes=0-9, 20-29",  "bytes=0-9, 20-29",  false, "multi-range with OWS" },
  { "bytes=536870911-1073741823", "bytes=536870911-1073741823", false, "large offsets" },
  -- Parseable but unsatisfiable: nginx's 416 is conformant, leave it alone.
  { "bytes=100-50",      "bytes=100-50",      false, "unsatisfiable stays a 416" },
  -- Other malformed shapes.
  { "bytes=abc",         "",                  true,  "non-numeric" },
  { "bytes=",            "",                  true,  "empty range-set" },
  { "bytes=-",           "",                  true,  "bare hyphen" },
  { "bytes=0-1-2",       "",                  true,  "too many hyphens" },
  { "bytes=0-9,",        "",                  true,  "trailing empty spec" },
  { "bytes=--1",         "",                  true,  "no first-byte-pos" },
  -- Unknown units are nginx's business; it already ignores them.
  { "items=0-1",         "items=0-1",         false, "unknown unit passes through" },
  -- Absent header.
  { "",                  "",                  false, "empty header" },
}

local failures = 0
for _, c in ipairs(cases) do
  local input, want_ret, want_cleared, why = c[1], c[2], c[3], c[4]
  local got_ret, got_cleared = run(input)
  if got_ret ~= want_ret or got_cleared ~= want_cleared then
    failures = failures + 1
    print(string.format(
      "FAIL  %-30s returned %-22s cleared=%-5s  (want %-22s cleared=%-5s)  -- %s",
      "'" .. input .. "'", "'" .. tostring(got_ret) .. "'", tostring(got_cleared),
      "'" .. want_ret .. "'", tostring(want_cleared), why))
  else
    print(string.format("ok    %-30s -> %s", "'" .. input .. "'",
      got_cleared and "cleared" or "kept"))
  end
end

-- nil header (no Range at all) must not error.
do
  local ok, err = pcall(run, nil)
  if not ok then
    failures = failures + 1
    print("FAIL  absent Range header raised: " .. tostring(err))
  else
    print("ok    absent Range header handled")
  end
end

if failures > 0 then
  print(string.format("\nFAILED: %d case(s)", failures))
  os.exit(1)
end
print("\nPASS: safe-range.lua validator")
