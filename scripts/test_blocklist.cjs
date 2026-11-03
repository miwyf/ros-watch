const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { test } = require("node:test");

const context = vm.createContext({ document: { addEventListener() {} } });
vm.runInContext(fs.readFileSync(path.join(__dirname, "../web/static/blocklist.js"), "utf8"), context);
const commands = context.buildBlockedSourceCommands;

test("single source uses the shared list and a precise unblock selector", () => {
  const result = commands(["80.82.65.5"]);
  assert.match(result.block, /list="blocked-sources" and address=80\.82\.65\.5/);
  assert.match(result.block, /\[:len .*\] = 0/);
  assert.match(result.block, /add list="blocked-sources" address=80\.82\.65\.5/);
  assert.doesNotMatch(result.block, /timeout=|firewall raw|firewall filter/);
  assert.equal(result.unblock, '/ip firewall address-list remove [find where list="blocked-sources" and address=80.82.65.5]');
});

test("batch preserves source order and deduplicates without changing other lists", () => {
  const result = commands(["198.51.100.12", "203.0.113.8", "198.51.100.12"]);
  assert.equal(result.block.split("\n").length, 2);
  assert.equal(result.unblock.split("\n").length, 2);
  assert.match(result.block.split("\n")[0], /address=198\.51\.100\.12/);
  assert.match(result.block.split("\n")[1], /address=203\.0\.113\.8/);
  assert.doesNotMatch(result.unblock, /remove \[find\]/);
});

test("rejects script injection, ranges, prefixes, IPv6 and noncanonical IPv4", () => {
  for (const value of ["", null, undefined, 1234, "example.com", "::1", "::ffff:80.82.65.5", "80.82.65.5/32", "80.82.65.0/24", "80.82.65.5-80.82.65.6", "256.0.0.1", "01.2.3.4", "1.2.3.4\n", '1.2.3.4; /ip firewall raw remove [find]', '1.2.3.4"', "$(whoami)"]) {
    assert.throws(() => commands([value]), /IPv4/);
    assert.throws(() => commands(["198.51.100.12", value]), /IPv4/);
  }
});

test("setup supports an empty RAW table and resolves positions by internal ID", () => {
  const result = commands([]);
  assert.equal(result.block, "");
  assert.equal(result.unblock, "");
  assert.match(result.setup, /\[:len \$rule\] = 0/);
  assert.match(result.setup, /\[:len \$rule\] > 1/);
  assert.equal((result.setup.match(/raw add/g) || []).length, 1);
  assert.match(result.setup, /src-address-list="blocked-sources" action=drop log=no/);
  assert.match(result.setup, /:local first \[:pick \[\/ip firewall raw find\] 0\]/);
  assert.match(result.setup, /\$rule != \$first/);
  assert.match(result.setup, /move \$rule destination=\$first/);
  assert.doesNotMatch(result.setup, /place-before=0|raw remove|address-list remove|ROS.?WATCH/i);
  assert.match(result.check, /address-list print detail where list="blocked-sources"/);
  assert.match(result.check, /raw print stats/);
});

test("selection survives refresh, drops sources outside the current view and excludes IPv6", () => {
  const elements = { "scan-select-all": {}, "scan-copy-selected": {} };
  context.document.getElementById = id => elements[id];
  context.updateScanSelection([{ src: "198.51.100.12" }, { src: "203.0.113.8" }, { src: "2001:db8::1" }]);
  assert.equal(elements["scan-select-all"].disabled, false);
  assert.equal(elements["scan-copy-selected"].disabled, true);
  vm.runInContext('selectedScanSources.add("198.51.100.12")', context);
  context.updateScanSelection([{ src: "203.0.113.8" }, { src: "198.51.100.12" }]);
  assert.equal(elements["scan-select-all"].indeterminate, true);
  assert.match(elements["scan-copy-selected"].textContent, /（1）/);
  context.updateScanSelection([{ src: "203.0.113.8" }]);
  assert.equal(elements["scan-copy-selected"].disabled, true);
  context.updateScanSelection([]);
  assert.equal(elements["scan-select-all"].disabled, true);
  assert.equal(elements["scan-select-all"].checked, false);
});
