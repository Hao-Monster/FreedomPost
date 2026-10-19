import assert from "node:assert/strict";
import { existsSync, readFileSync, readdirSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { join } from "node:path";

const root = fileURLToPath(new URL("../apps/public-reader/dist/", import.meta.url));
assert.ok(existsSync(join(root, "reader/index.html")), "Production reader shell is missing");
assert.deepEqual(JSON.parse(readFileSync(join(root, "search/post-list.json"), "utf8")), [], "Production post snapshot must not contain demo data");
const seeds = ["welcome", "architecture-notes", "comment-safety"];
for (const slug of seeds) assert.ok(!existsSync(join(root, "p", slug)), `Demo article route leaked: ${slug}`);
function scan(directory) {
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    const path = join(directory, entry.name);
    if (entry.isDirectory()) scan(path);
    else if (/\.(html|json|xml|js)$/.test(entry.name)) {
      const content = readFileSync(path, "utf8");
      for (const slug of seeds) {
        assert.ok(!content.includes(`/p/${slug}`) && !content.includes(`"slug":"${slug}"`), `Demo artifact leaked into ${path}`);
      }
    }
  }
}
scan(root);
console.log("OK production reader artifacts contain no demo article routes or snapshots");
