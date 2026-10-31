#!/usr/bin/env node

const fs = require("fs");
const path = require("path");

let chromium;
try {
  ({ chromium } = require("playwright"));
} catch (error) {
  console.error(
    "playwright is required. Load the Codex workspace dependencies and set NODE_PATH to its Node package directory."
  );
  process.exit(2);
}

const [inputArg, outputPrefixArg] = process.argv.slice(2);
if (!inputArg || !outputPrefixArg) {
  console.error(
    "usage: node render-and-check.cjs <input.svg> <absolute-output-prefix>"
  );
  process.exit(2);
}

const inputPath = path.resolve(inputArg);
const outputPrefix = path.resolve(outputPrefixArg);
const svg = fs.readFileSync(inputPath, "utf8");

function assertSafeSvg(source) {
  const forbidden = [
    [/<(?:[A-Za-z_][\w.-]*:)?script\b/i, "script elements"],
    [/<(?:[A-Za-z_][\w.-]*:)?foreignObject\b/i, "foreignObject elements"],
    [/\son[a-z][\w:.-]*\s*=/i, "event-handler attributes"],
    [/\b(?:javascript|vbscript)\s*:/i, "script URLs"],
    [/<!DOCTYPE\b/i, "DOCTYPE declarations"],
    [/<!ENTITY\b/i, "entity declarations"],
    [/@import\b/i, "CSS imports"],
  ];

  for (const [pattern, description] of forbidden) {
    if (pattern.test(source)) {
      throw new Error(`unsafe SVG: ${description} are not allowed`);
    }
  }

  const isLocalReference = (value) => {
    const normalized = value.trim();
    return (
      normalized.startsWith("#") ||
      /^data:image\/(?:png|jpeg|webp);base64,/i.test(normalized)
    );
  };

  const attributeReference = /\b(?:href|xlink:href|src)\s*=\s*(["'])(.*?)\1/gis;
  for (const match of source.matchAll(attributeReference)) {
    if (!isLocalReference(match[2])) {
      throw new Error(`unsafe SVG: external reference is not allowed: ${match[2]}`);
    }
  }

  const cssReference = /url\(\s*(?:(["'])(.*?)\1|([^)'"\s]+))\s*\)/gis;
  for (const match of source.matchAll(cssReference)) {
    const value = match[2] ?? match[3];
    if (!isLocalReference(value)) {
      throw new Error(`unsafe SVG: external CSS reference is not allowed: ${value}`);
    }
  }
}

assertSafeSvg(svg);

const viewBoxMatch = svg.match(
  /viewBox\s*=\s*["']\s*[-\d.]+\s+[-\d.]+\s+([\d.]+)\s+([\d.]+)\s*["']/
);

if (!viewBoxMatch) {
  console.error("input SVG must declare a numeric viewBox");
  process.exit(2);
}

const width = Math.ceil(Number(viewBoxMatch[1]));
const height = Math.ceil(Number(viewBoxMatch[2]));
const outputDirectory = path.dirname(outputPrefix);
fs.mkdirSync(outputDirectory, { recursive: true });

const lightPath = `${outputPrefix}-light.png`;
const darkPath = `${outputPrefix}-dark.png`;

const documentMarkup = (darkSurround) => `
  <!doctype html>
  <html>
    <head>
      <meta http-equiv="Content-Security-Policy" content="default-src 'none'; img-src data:; style-src 'unsafe-inline'; script-src 'none'; connect-src 'none'; object-src 'none'; frame-src 'none'; media-src 'none'; worker-src 'none'; base-uri 'none'; form-action 'none'">
      <style>
        html, body { width: 100%; height: 100%; margin: 0; overflow: hidden; }
        body { background: ${darkSurround ? "#101418" : "#ffffff"}; }
        #diagram-panel {
          position: fixed;
          inset: ${darkSurround ? "24px" : "0"};
          overflow: hidden;
          border-radius: ${darkSurround ? "16px" : "0"};
          background: #ffffff;
        }
        #diagram-panel > svg { display: block; width: 100%; height: 100%; }
      </style>
    </head>
    <body><div id="diagram-panel">${svg}</div></body>
  </html>
`;

async function createLockedPage(browser) {
  const context = await browser.newContext({
    viewport: { width, height },
    deviceScaleFactor: 2,
    colorScheme: "light",
    javaScriptEnabled: false,
    serviceWorkers: "block",
  });
  await context.route("**/*", (route) => route.abort("blockedbyclient"));
  const page = await context.newPage();
  return { context, page };
}

async function assertSingleSvgRoot(page) {
  const failure = await page.evaluate(() => {
    const panel = document.querySelector("#diagram-panel");
    const root = panel?.firstElementChild;
    if (
      document.body.children.length !== 1 ||
      !panel ||
      panel.children.length !== 1 ||
      root?.namespaceURI !== "http://www.w3.org/2000/svg" ||
      root?.localName !== "svg"
    ) {
      return "input must produce exactly one SVG root inside the diagram panel";
    }
    return null;
  });

  if (failure) {
    throw new Error(`unsafe SVG: ${failure}`);
  }
}

async function checkTextFit(page) {
  return page.evaluate(() => {
    const root = document.querySelector("#diagram-panel > svg");
    const viewBox = root.viewBox.baseVal;
    const failures = [];

    for (const element of root.querySelectorAll("text")) {
      const box = element.getBBox();
      if (
        box.x < viewBox.x - 0.5 ||
        box.y < viewBox.y - 0.5 ||
        box.x + box.width > viewBox.x + viewBox.width + 0.5 ||
        box.y + box.height > viewBox.y + viewBox.height + 0.5
      ) {
        failures.push(
          `text outside viewBox: "${element.textContent.trim()}"`
        );
      }
    }

    for (const element of root.querySelectorAll("text[data-fit]")) {
      const target = root.querySelector(
        `#${CSS.escape(element.dataset.fit)}`
      );
      if (!target) {
        failures.push(
          `missing fit target #${element.dataset.fit} for "${element.textContent.trim()}"`
        );
        continue;
      }

      const padding = Number(element.dataset.fitPadding || 4);
      const textBox = element.getBBox();
      const targetBox = target.getBBox();
      if (
        textBox.x < targetBox.x + padding ||
        textBox.y < targetBox.y + padding ||
        textBox.x + textBox.width > targetBox.x + targetBox.width - padding ||
        textBox.y + textBox.height > targetBox.y + targetBox.height - padding
      ) {
        failures.push(
          `text exceeds #${element.dataset.fit}: "${element.textContent.trim()}"`
        );
      }
    }

    return failures;
  });
}

(async () => {
  const browser = await chromium.launch({ headless: true });
  try {
    const { context: lightContext, page: lightPage } =
      await createLockedPage(browser);
    await lightPage.setContent(documentMarkup(false), {
      waitUntil: "load",
    });
    await assertSingleSvgRoot(lightPage);

    const failures = await checkTextFit(lightPage);
    if (failures.length) {
      console.error(failures.join("\n"));
      process.exitCode = 1;
      return;
    }

    await lightPage.screenshot({ path: lightPath });
    await lightContext.close();

    const { context: darkContext, page: darkPage } =
      await createLockedPage(browser);
    await darkPage.setContent(documentMarkup(true), {
      waitUntil: "load",
    });
    await assertSingleSvgRoot(darkPage);
    await darkPage.screenshot({ path: darkPath });
    await darkContext.close();

    console.log(`rendered ${lightPath}`);
    console.log(`rendered ${darkPath}`);
  } finally {
    await browser.close();
  }
})().catch((error) => {
  console.error(error);
  process.exit(1);
});
