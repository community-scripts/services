// Renders the /new page against a synthetic payload and checks that every
// section produced something.
//
// node --check only proves the script parses, and a function deleted during a
// refactor still parses -- that is exactly how "renderWarnings is not defined"
// reached a browser. This actually calls render(), so a missing function, a
// renamed element id, or a section that silently produces nothing all fail here
// instead of on the page.
//
// Run from the repository root: node testdata/newdash-smoke.js

const fs = require("fs");
const path = require("path");

const file = path.join(__dirname, "..", "public", "templates", "new", "index.html");
const html = fs.readFileSync(file, "utf8");
const js = html.split("<script>")[1].split("</script>")[0];

// Minimal DOM: every element remembers what was written into it, which is all
// the assertions below need.
const store = {};
const mk = (id) => ({
  id,
  innerHTML: "",
  textContent: "",
  value: "",
  dataset: {},
  style: {},
  classList: { add() {}, remove() {}, toggle() {} },
  children: [],
  addEventListener() {},
  querySelectorAll: () => [],
});

global.document = {
  getElementById: (id) => (store[id] ||= mk(id)),
  addEventListener() {},
  createElement: () => mk("a"),
};
global.fetch = async () => {
  throw new Error("no network in the smoke test");
};
global.URL = { createObjectURL: () => "blob:", revokeObjectURL() {} };
global.setInterval = () => 0;
global.clearInterval = () => {};

eval(js);

// Shaped like a real /api/new response, including a warning so that path renders
// too. Every value is invented; nothing here talks to a database.
const sample = {
  days: 7, platform: "", repo: "", type: "", min_runs: 10,
  runs: 1200, success: 1100, failed: 70, blocked: 45, aborted: 20, unfinished: 10,
  all_time: 2195915, median_duration: 125,
  live_in_flight: 3, live_last_hour: 40, live_last_24h: 900,
  live_now: [{
    app: "valkey", type: "lxc", status: "installing", platform: "pve",
    os: "debian 13", repo: "community-scripts/ProxmoxVE",
    last_seen: "2026-08-18 10:00", duration: 1200,
  }],
  warnings: ["daily: some database error"],
  daily: [
    { day: "2026-08-17", runs: 600, success: 550, failed: 30, blocked: 20, aborted: 10, unfinished: 10 },
    { day: "2026-08-18", runs: 600, success: 550, failed: 40, blocked: 25, aborted: 10, unfinished: 0 },
  ],
  top_apps: [{ label: "docker", runs: 300, success: 290, failed: 8, blocked: 5, aborted: 2, unfinished: 0 }],
  worst_apps: [{ label: "foo", runs: 40, success: 10, failed: 28, blocked: 3, aborted: 2, unfinished: 0 }],
  by_platform: [
    { label: "pve", runs: 1000, success: 950, failed: 40, blocked: 30, aborted: 10, unfinished: 0 },
    { label: "incus", runs: 200, success: 150, failed: 30, blocked: 15, aborted: 10, unfinished: 10 },
  ],
  by_type: [], by_repo: [], by_os: [], by_host_version: [], by_repo_slug: [],
  categories: [{ label: "network", count: 30 }],
  exit_codes: [{ code: 127, desc: "command not found", count: 12, apps: 5 }],
  signatures: [{ category: "network", exit_code: 6, message: "curl failed", count: 9, apps: 3 }],
  by_privilege: [], by_cores: [], by_ram: [], by_disk: [], by_cpu: [],
  by_gpu: [], by_gpu_passthrough: [], by_arm: [], by_method: [], by_client: [],
  recent: [{
    run: "abc", app: "valkey", type: "lxc", status: "failed", exit_code: 127,
    category: "dependency", platform: "incus", os: "alpine 3.24",
    host_version: "incus 6.0", repo: "community-scripts/Incus", method: "default",
    cores: 2, ram: 1024, disk: 4, privileged: false, duration: 90,
    last_seen: "2026-08-18 09:00", error: "boom", failed_command: "apk add foo",
    failed_line: 42, arch: "amd64", kernel_version: "6.8", app_version: "1.0",
    cpu_vendor: "AMD", cpu_model: "EPYC", gpu_vendor: "", gpu_model: "",
    gpu_passthrough: "no", ram_speed: "3200", payload_version: 2,
  }],
};

let bad = 0;
const fail = (msg) => { console.error("  " + msg); bad++; };

render(sample);

// One assertion per section, on a value that could only have come from the
// payload -- an empty panel is the failure this exists to catch.
const checks = [
  ["kpis", "Runs"],
  // The tile and its count, so that adding the bucket to the payload without
  // rendering it still fails here.
  ["kpis", "Blocked"],
  ["kpis", "the host never let the install start"],
  ["live", "In flight"],
  ["warn", "could not be loaded"],
  ["chart", "title="],
  ["by_platform", "incus"],
  ["top_apps", "docker"],
  ["worst_apps", "foo"],
  ["categories", "network"],
  ["exit_codes", "command not found"],
  ["signatures", "curl failed"],
  ["recent", "valkey"],
  ["live_now", "valkey"],
];
for (const [id, needle] of checks) {
  if (!String(store[id]?.innerHTML || "").includes(needle)) {
    fail(`#${id} did not render ${JSON.stringify(needle)}`);
  }
}

openDetail(sample.recent[0]);
for (const needle of ["Exit code", "apk add foo", "Payload version", "alpine 3.24"]) {
  if (!String(store.mBody?.innerHTML || "").includes(needle)) {
    fail(`the detail dialog is missing ${JSON.stringify(needle)}`);
  }
}

// An empty response must not throw, and must not claim numbers it does not have.
render({ days: 1, min_runs: 5, runs: 0, success: 0, failed: 0, aborted: 0, unfinished: 0 });
if (!String(store.recent.innerHTML).includes("no runs")) {
  fail("an empty window should say so in the log");
}

if (bad) {
  console.error(`  ${bad} problem(s)`);
  process.exit(1);
}
console.log("  /new renders: every section, the detail dialog, and the empty case");
