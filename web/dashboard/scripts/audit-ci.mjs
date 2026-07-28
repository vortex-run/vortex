#!/usr/bin/env node
// CI dependency audit for the dashboard.
//
// `npm audit --audit-level=high` is all-or-nothing: one accepted, provably
// unreachable advisory would force us to either drop the threshold (losing the
// signal entirely) or downgrade a package (sometimes a net security loss). This
// wrapper keeps the threshold at high/critical and fails on anything that is
// not explicitly allowlisted, with a written reachability argument, in
// audit-allowlist.json.
//
// It also reports allowlist entries that no longer match a live advisory, so
// stale exceptions get deleted instead of silently widening over time.

import { spawnSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const dashboardDir = join(dirname(fileURLToPath(import.meta.url)), "..");
const FAIL_ON = new Set(["high", "critical"]);

/** advisoryIds extracts GHSA ids from a vulnerability's `via` entries. */
function advisoryIds(vuln) {
  const ids = new Set();
  for (const via of vuln.via ?? []) {
    if (typeof via !== "object") continue;
    const match = /GHSA-[a-z0-9-]+/i.exec(via.url ?? "");
    if (match) ids.add(match[0]);
  }
  return ids;
}

// npm audit exits non-zero when it finds anything, so capture output regardless.
// The command is passed as a single string with shell:true (rather than an args
// array, which would trip Node's DEP0190 escaping warning). A shell is needed on
// Windows, where npm is a .cmd shim that spawnSync cannot resolve directly.
const proc = spawnSync("npm audit --json", {
  cwd: dashboardDir,
  encoding: "utf8",
  shell: true,
  maxBuffer: 32 * 1024 * 1024,
});
if (!proc.stdout) {
  console.error("audit-ci: npm audit produced no output");
  console.error(proc.stderr ?? "");
  process.exit(2);
}

let report;
try {
  report = JSON.parse(proc.stdout);
} catch (err) {
  console.error(`audit-ci: could not parse npm audit output: ${err.message}`);
  process.exit(2);
}

const allowlist = JSON.parse(
  readFileSync(join(dashboardDir, "audit-allowlist.json"), "utf8"),
).allow;
const allowById = new Map(allowlist.map((entry) => [entry.id, entry]));

const blocking = [];
const accepted = [];
const seenIds = new Set();

for (const [name, vuln] of Object.entries(report.vulnerabilities ?? {})) {
  if (!FAIL_ON.has(vuln.severity)) continue;
  for (const id of advisoryIds(vuln)) {
    seenIds.add(id);
    const entry = allowById.get(id);
    if (entry) accepted.push({ name, id, reason: entry.reason });
    else blocking.push({ name, id, severity: vuln.severity });
  }
}

for (const { name, id, reason } of accepted) {
  console.log(`accepted  ${id}  (${name})`);
  console.log(`          ${reason.slice(0, 140)}…`);
}

// A stale entry is not fatal — it means something got fixed — but it must be
// visible so the exception is removed rather than left to broaden silently.
for (const entry of allowlist) {
  if (!seenIds.has(entry.id)) {
    console.log(
      `::warning::audit-allowlist.json entry ${entry.id} (${entry.package}) no longer matches ` +
        `any high/critical advisory — it is probably fixed. Delete the entry.`,
    );
  }
}

if (blocking.length > 0) {
  console.error("");
  for (const { name, id, severity } of blocking) {
    console.error(`::error::${severity} advisory ${id} in ${name} is not allowlisted`);
  }
  console.error(
    `\naudit-ci: ${blocking.length} unreviewed high/critical advisory/advisories.\n` +
      "Fix by upgrading, or — only if the vulnerable path is genuinely unreachable —\n" +
      "add an entry to web/dashboard/audit-allowlist.json with the reachability argument.",
  );
  process.exit(1);
}

console.log(
  `\naudit-ci: no unreviewed high/critical advisories ` +
    `(${accepted.length} accepted via allowlist).`,
);
