#!/usr/bin/env node
// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Check one figure SVG and render it to <prefix>-light.png and <prefix>-dark.png at 2x.
//
// usage: node render.cjs <figure.svg> <output-prefix>
//
// Checks, failing with exit 1:
//   - no scripts, foreignObject, event handlers, DOCTYPE/ENTITY, CSS @import,
//     embedded raster images, or references other than same-document "#id";
//   - the document renders to exactly one SVG root;
//   - every <text> lies inside the viewBox, and every text with data-fit="ID"
//     lies inside element #ID less data-fit-padding (default 4).
// The page is rendered with JavaScript disabled and all network requests
// blocked. The light PNG is the figure on white; the dark PNG is the same
// figure on a dark surround, as it appears in a dark-themed document viewer.
// Needs Playwright with Chromium on NODE_PATH.
"use strict";
const fs = require("fs");
const path = require("path");

let chromium;
try {
  ({ chromium } = require("playwright"));
} catch (e) {
  console.error("render.cjs: playwright not found; set NODE_PATH to a node_modules with playwright installed");
  process.exit(2);
}

const [svgArg, prefixArg] = process.argv.slice(2);
if (!svgArg || !prefixArg) {
  console.error("usage: node render.cjs <figure.svg> <output-prefix>");
  process.exit(2);
}
const svgPath = path.resolve(svgArg);
const prefix = path.resolve(prefixArg);
const svg = fs.readFileSync(svgPath, "utf8");

function staticChecks(src) {
  const bad = [
    [/<(?:\w+:)?script\b/i, "a script element"],
    [/<(?:\w+:)?foreignObject\b/i, "a foreignObject element"],
    [/\son[a-z][\w:.-]*\s*=/i, "an event-handler attribute"],
    [/<!DOCTYPE\b|<!ENTITY\b/i, "a DOCTYPE or ENTITY declaration"],
    [/@import\b/i, "a CSS @import"],
    [/<(?:\w+:)?image\b|data:image\//i, "an embedded raster image"],
  ];
  for (const [re, what] of bad) if (re.test(src)) return `contains ${what}`;
  for (const m of src.matchAll(/\b(?:href|xlink:href|src)\s*=\s*(["'])(.*?)\1/gis))
    if (!m[2].trim().startsWith("#")) return `external reference ${m[2]}`;
  for (const m of src.matchAll(/url\(\s*(?:(["'])(.*?)\1|([^)'"\s]+))\s*\)/gis)) {
    const v = (m[2] ?? m[3]).trim();
    if (!v.startsWith("#")) return `external CSS reference ${v}`;
  }
  return null;
}

const problem = staticChecks(svg);
if (problem) {
  console.error(`${svgArg}: ${problem}`);
  process.exit(1);
}

const vb = svg.match(/viewBox\s*=\s*["']\s*[-\d.]+\s+[-\d.]+\s+([\d.]+)\s+([\d.]+)\s*["']/);
if (!vb) {
  console.error(`${svgArg}: no numeric viewBox`);
  process.exit(1);
}
const width = Math.ceil(Number(vb[1]));
const height = Math.ceil(Number(vb[2]));

const page = (dark) => `<!doctype html><html><head>
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'">
<style>
html,body{margin:0;width:100%;height:100%;overflow:hidden;background:${dark ? "#101418" : "#ffffff"}}
#panel{position:fixed;inset:${dark ? "24px" : "0"};border-radius:${dark ? "16px" : "0"};overflow:hidden;background:#ffffff}
#panel>svg{display:block;width:100%;height:100%}
</style></head><body><div id="panel">${svg}</div></body></html>`;

async function open(browser, dark) {
  const ctx = await browser.newContext({
    viewport: { width, height },
    deviceScaleFactor: 2,
    javaScriptEnabled: false,
    serviceWorkers: "block",
  });
  await ctx.route("**/*", (r) => r.abort("blockedbyclient"));
  const p = await ctx.newPage();
  await p.setContent(page(dark), { waitUntil: "load" });
  const ok = await p.evaluate(() => {
    const panel = document.getElementById("panel");
    const root = panel && panel.firstElementChild;
    return document.body.children.length === 1 && panel.children.length === 1 &&
      root && root.namespaceURI === "http://www.w3.org/2000/svg" && root.localName === "svg";
  });
  if (!ok) throw new Error(`${svgArg}: does not render to exactly one SVG root`);
  return { ctx, p };
}

function fitFailures() {
  const root = document.querySelector("#panel > svg");
  const v = root.viewBox.baseVal;
  const out = [];
  const outside = (b, x0, y0, x1, y1) =>
    b.x < x0 || b.y < y0 || b.x + b.width > x1 || b.y + b.height > y1;
  for (const t of root.querySelectorAll("text")) {
    const b = t.getBBox();
    if (outside(b, v.x - 0.5, v.y - 0.5, v.x + v.width + 0.5, v.y + v.height + 0.5))
      out.push(`text outside the viewBox: "${t.textContent.trim()}"`);
  }
  for (const t of root.querySelectorAll("text[data-fit]")) {
    const target = root.querySelector(`#${CSS.escape(t.dataset.fit)}`);
    if (!target) {
      out.push(`no element #${t.dataset.fit} for "${t.textContent.trim()}"`);
      continue;
    }
    const pad = Number(t.dataset.fitPadding || 4);
    const b = t.getBBox();
    const c = target.getBBox();
    if (outside(b, c.x + pad, c.y + pad, c.x + c.width - pad, c.y + c.height - pad))
      out.push(`text overflows #${t.dataset.fit}: "${t.textContent.trim()}"`);
  }
  return out;
}

(async () => {
  const browser = await chromium.launch({ headless: true });
  try {
    const light = await open(browser, false);
    const failures = await light.p.evaluate(fitFailures);
    if (failures.length) {
      console.error(failures.map((f) => `${svgArg}: ${f}`).join("\n"));
      process.exitCode = 1;
      return;
    }
    fs.mkdirSync(path.dirname(prefix), { recursive: true });
    await light.p.screenshot({ path: `${prefix}-light.png` });
    await light.ctx.close();
    const dark = await open(browser, true);
    await dark.p.screenshot({ path: `${prefix}-dark.png` });
    await dark.ctx.close();
    console.log(`rendered ${prefix}-light.png`);
    console.log(`rendered ${prefix}-dark.png`);
  } finally {
    await browser.close();
  }
})().catch((e) => {
  console.error(e.message || e);
  process.exit(1);
});
