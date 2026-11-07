const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { test } = require("node:test");

function page() {
  const elements = {};
  const element = id => elements[id] ||= {
    value: "", disabled: false, textContent: "", handlers: {},
    classList: { toggle() {} }, open: false,
    reportValidity() { return true; },
    showModal() { this.open = true; },
    close() { this.open = false; this.handlers.close?.(); },
    addEventListener(event, handler) { this.handlers[event] = handler; },
  };
  const buttons = ["ros-wan-check", "ros-wan-example", "ros-script"].map(id => ({
    dataset: { rosCopy: id }, disabled: true,
    addEventListener(event, handler) { this.handler = handler; },
  }));
  let copied = "";
  const context = vm.createContext({
    $: element,
    window: { location: { hostname: "127.0.0.1" } },
    document: { querySelectorAll: selector => selector === "[data-ros-copy]" ? buttons : [] },
    navigator: { clipboard: { async writeText(value) { copied = value; } } },
    setInterval() {}, renderStorage() {}, dl() {},
  });
  for (const script of ["settings", "routeros"])
    vm.runInContext(fs.readFileSync(path.join(__dirname, `../web/static/${script}.js`), "utf8"), context);
  return { context, element, buttons, copied: () => copied };
}

test("WAN commands use every interface on the left and ignore display names", () => {
  const { context } = page();
  const commands = context.buildWANInterfaceCommands(" pppoe-out1 = 主线\r\n\npppoe-out2=备用线路 ");
  assert.deepEqual(Array.from(commands.interfaces), ["pppoe-out1", "pppoe-out2"]);
  assert.match(commands.add, /interface="pppoe-out1"/);
  assert.match(commands.add, /interface="pppoe-out2"/);
  assert.doesNotMatch(commands.add + commands.check, /主线|备用线路/);
  assert(commands.add.indexOf('name="pppoe-out2"') < commands.add.indexOf("/interface list add"));
  assert(commands.add.startsWith("{\n") && commands.add.endsWith("\n}"));
  assert.doesNotMatch(commands.add, /pppoe-client|\/ip firewall|\/ip route/);
});

test("command dialogs generate and copy the complete selected script; failures clear stale output", async () => {
  const { context, element, buttons, copied } = page();
  element("set-wan_map").value = "ether1=WAN";
  element("set-wan_map").handlers.input();
  element("ros-server").value = "192.0.2.21";
  element("ros-port").value = "15514";
  const scripts = { install: "{\ninstall and system logs\n}", check: "check", remove: "remove", upgrade: "upgrade", remove_system: "remove system" };
  let complete, sent;
  context.postSettings = (url, body) => { sent = body; return new Promise(resolve => { complete = resolve; }); };
  const opening = context.openRouterOSCommand("install");
  const copyButton = buttons.find(button => button.dataset.rosCopy === "ros-script");
  const wanButtons = buttons.filter(button => button.dataset.rosCopy.startsWith("ros-wan-"));
  assert(copyButton.disabled);
  assert(wanButtons.every(button => !button.disabled));
  assert.equal(sent.server_address, "192.0.2.21");
  assert.equal(sent.syslog_port, 15514);
  complete(scripts);
  await opening;
  assert.equal(element("ros-script").value, scripts.install);
  assert(!copyButton.disabled);
  await copyButton.handler();
  assert.equal(copied(), scripts.install);
  context.postSettings = async () => { throw Error("synthetic generation failure"); };
  await context.openRouterOSCommand("check");
  assert.equal(element("ros-script").value, "");
  assert(copyButton.disabled);
  assert.match(element("ros-copy-result").textContent, /synthetic generation failure/);
  assert(wanButtons.every(button => !button.disabled));
});

test("auxiliary commands need no server; closed dialogs discard delayed responses", async () => {
  const { context, element } = page();
  let complete;
  context.postSettings = (url, body) => {
    assert.equal(body.command, "check");
    assert(!Object.hasOwn(body, "server_address"));
    return new Promise(resolve => { complete = resolve; });
  };
  const opening = context.openRouterOSCommand("check");
  element("routeros-dialog").close();
  complete({ check: "late check output" });
  await opening;
  assert.equal(element("ros-script").value, "");
  assert(!element("routeros-dialog").open);
});

test("one, several, renamed and non-PPPoE interfaces replace fixed examples", () => {
  const { context } = page();
  for (const mapping of ["ether1=外网", "wan-primary=主线\nvlan100=VLAN\npppoe-out3=拨号"]) {
    const commands = context.buildWANInterfaceCommands(mapping);
    assert.doesNotMatch(commands.add, /pppoe-out1|pppoe-out2/);
    for (const name of commands.interfaces) {
      assert(commands.check.includes(`/interface print where name="${name}"`));
      assert(commands.add.includes(`interface="${name}"`));
    }
    assert.equal((commands.add.match(/interface list member add/g) || []).length, commands.interfaces.length);
  }
});

test("mapping rejects empty, duplicate, malformed and unsupported interface values", () => {
  const { context } = page();
  for (const value of ["", "\n", "ether1", "ether1=", "=WAN", "ether1=主线\nether1=备用", "a".repeat(65) + "=WAN", "ether\u0000name=WAN", "ether\u007fname=WAN", Array.from({ length: 33 }, (_, i) => `ether${i}=WAN`).join("\n")])
    assert.throws(() => context.buildWANInterfaceCommands(value), /WAN/);
});

test("RouterOS string literals escape quotes, backslashes and variable expansion", () => {
  const { context } = page();
  const name = 'uplink"; :error $payload; \\ test';
  const literal = String.raw`"uplink\"; :error \$payload; \\ test"`;
  const commands = context.buildWANInterfaceCommands(name + "=显示名称");
  assert(commands.check.includes("name=" + literal));
  assert(commands.add.includes("interface=" + literal));
  assert.doesNotMatch(commands.add, /(?<!\\)\$payload/);
});

test("settings load generates commands; input updates drafts and clears stale commands on errors", async () => {
  const { context, element, buttons, copied } = page();
  context.api = async url => {
    if (url === "/api/settings") return { settings: { allowed_senders: "", wan_map: { "pppoe-out1": "WAN1", "pppoe-out2": "WAN2" } }, storage: {} };
    if (url === "/api/account") return { username: "admin" };
    return { settings: { platform: "feishu", trusted_admins: "" }, status: {} };
  };
  await context.window.loadSettings();
  assert(element("ros-wan-example").value.includes('interface="pppoe-out2"'));
  const wanButtons = buttons.filter(button => button.dataset.rosCopy.startsWith("ros-wan-"));
  assert(wanButtons.every(button => !button.disabled));
  element("set-wan_map").value = "ether1=新外网";
  element("set-wan_map").handlers.input();
  assert(element("ros-wan-example").value.includes('interface="ether1"'));
  assert.doesNotMatch(element("ros-wan-example").value, /pppoe-out/);
  await wanButtons[1].handler();
  assert.equal(copied(), element("ros-wan-example").value);
  context.postSettings = async () => { throw Error("synthetic generation failure"); };
  await context.openRouterOSCommand("install");
  assert(wanButtons.every(button => !button.disabled));
  assert(element("ros-wan-example").value.includes('interface="ether1"'));
  element("set-wan_map").value = "ether1=";
  element("set-wan_map").handlers.input();
  assert.equal(element("ros-wan-example").value, "");
  assert.equal(element("ros-wan-check").value, "");
  assert(wanButtons.every(button => button.disabled));
  element("set-wan_map").value = "vlan200=线路";
  element("set-wan_map").handlers.input();
  assert(wanButtons.every(button => !button.disabled));
  assert.match(element("ros-wan-result").textContent, /vlan200/);
});

test("connection and log settings save independently and preserve other-tab drafts", async () => {
  const { context, element } = page();
  let saved = { allowed_senders: "192.0.2.1", wan_map: { ether1: "WAN" }, retention_days: 7, min_free_mb: 1024, queue_size: 4096, ingest_per_second: 500, scan_ports: 15, scan_window: 300, sampling: "limited", disk_low_action: "prune" };
  context.api = async url => {
    if (url === "/api/settings") return { settings: saved, storage: {} };
    if (url === "/api/account") return { username: "admin" };
    return { settings: { platform: "feishu", trusted_admins: "" }, status: {} };
  };
  context.refresh = () => {};
  await context.window.loadSettings();
  context.postSettings = async (url, body) => { saved = JSON.parse(JSON.stringify(body)); return { message: "saved" }; };
  element("set-retention_days").value = "14";
  element("set-allowed_senders").value = "192.0.2.2";
  element("set-wan_map").value = "pppoe-out1=主线";
  // Another client updated a log setting; saving connection settings must keep it.
  saved.scan_ports = 25;
  await element("connection-form").handlers.submit({ preventDefault() {} });
  assert.equal(saved.allowed_senders, "192.0.2.2");
  assert.equal(saved.wan_map["pppoe-out1"], "主线");
  assert.equal(saved.retention_days, 7);
  assert.equal(saved.scan_ports, 25);
  assert.equal(element("set-retention_days").value, "14");
  element("set-wan_map").value = "unfinished invalid draft";
  element("set-allowed_senders").value = "unfinished address";
  await element("runtime-form").handlers.submit({ preventDefault() {} });
  assert.equal(saved.retention_days, 14);
  assert.equal(saved.allowed_senders, "192.0.2.2");
  assert.equal(saved.wan_map["pppoe-out1"], "主线");
  assert.equal(element("set-wan_map").value, "unfinished invalid draft");
  assert.equal(element("runtime-result").textContent, "已保存");
});
