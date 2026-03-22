#!/usr/bin/env node
/**
 * scripts/patch-extension-components.js
 *
 * Applied by `npm run sync:components` after copying components to
 * webflow-designer-extension-cli/public/.
 *
 * Patches hover-job-card.js for the extension context:
 *   - Replaces defaultFetcher (which tries to import /app/lib/api-client.js)
 *     with a version that throws a clear error if setApiFetcher was never called.
 *   - Appends the window.HoverJobCard bridge so index.js (non-module) can access
 *     createJobCard and setApiFetcher.
 */

const fs = require("fs");
const path = require("path");

const target = path.join(
  __dirname,
  "../webflow-designer-extension-cli/public/hover-job-card.js"
);

if (!fs.existsSync(target)) {
  throw new Error(
    `patch-extension-components: target file not found at ${target}\n` +
      "Ensure 'sync:components' copy step completed successfully."
  );
}

let src = fs.readFileSync(target, "utf8");

// Replace the defaultFetcher that imports from /app/lib/api-client.js
src = src.replace(
  /async function defaultFetcher\(path\) \{[\s\S]*?^}/m,
  `async function defaultFetcher(path) {
  // Extension context: call setApiFetcher() before creating cards.
  throw new Error(
    \`hover-job-card: no API fetcher set. Call setApiFetcher() before creating cards. Path: \${path}\`
  );
}`
);

// Append window bridge if not already present
if (!src.includes("window.HoverJobCard")) {
  src +=
    "\n// Window bridge for non-module scripts (index.js)\nwindow.HoverJobCard = { createJobCard, setApiFetcher };\n";
}

// Assert the patch was applied — if the dynamic import path is still present,
// the regex silently no-oped (e.g. due to a Prettier reformat).
if (src.includes('import("/app/lib/api-client.js")')) {
  throw new Error(
    "patch-extension-components: defaultFetcher patch did not apply.\n" +
      "The hover-job-card.js source may have been reformatted. Update the regex in this script."
  );
}

fs.writeFileSync(target, src, "utf8");
console.log("Patched hover-job-card.js for extension context.");
