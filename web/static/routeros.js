"use strict";
function buildWANInterfaceCommands(text) {
  const interfaces = Object.keys(parseWANMapping(text));
  if (!interfaces.length) throw Error("请先填写“WAN 接口显示名称”，每行格式为 接口名称=显示名称");
  if (interfaces.length > 32 || interfaces.some(name => [...name].length > 64 || /[\x00-\x1f\x7f]/.test(name)))
    throw Error("WAN 接口须为 1–64 位有效文字，最多 32 个接口");
  const quote = name => '"' + name.replace(/[\\"$]/g, character => "\\" + character) + '"';
  const check = [
    '/interface list print detail where name="WAN"',
    '/interface list member print detail where list="WAN"',
    ...interfaces.map(name => `/interface print where name=${quote(name)}`),
  ].join("\n");
  const add = [
    "{",
    ...interfaces.map(name => `  :if ([:len [/interface find where name=${quote(name)}]] = 0) do={ :error "WAN interface not found; check WAN mapping first" }`),
    '  :if ([:len [/interface list find where name="WAN"]] = 0) do={ /interface list add name=WAN }',
    ...interfaces.map(name => `  :if ([:len [/interface list member find where list="WAN" and interface=${quote(name)}]] = 0) do={ /interface list member add list=WAN interface=${quote(name)} }`),
    '  /interface list member print detail where list="WAN"',
    "}",
  ].join("\n");
  return {interfaces, check, add};
}

const routerosDialog = $("routeros-dialog");
const routerosCopyButtons = [...document.querySelectorAll("[data-ros-copy]")];
const routerosWANCopyButtons = routerosCopyButtons.filter(button => button.dataset.rosCopy.startsWith("ros-wan-"));
const routerosCommandCopyButton = routerosCopyButtons.find(button => button.dataset.rosCopy === "ros-script");
let routerosRequest = 0;
function updateRouterOSWANCommands() {
  $("ros-wan-check").value = "";
  $("ros-wan-example").value = "";
  for (const button of routerosWANCopyButtons) button.disabled = true;
  try {
    const commands = buildWANInterfaceCommands($("set-wan_map").value);
    $("ros-wan-check").value = commands.check;
    $("ros-wan-example").value = commands.add;
    for (const button of routerosWANCopyButtons) button.disabled = false;
    settingsMessage("ros-wan-result", "接口：" + commands.interfaces.join("、"));
  } catch (error) {
    settingsMessage("ros-wan-result", error.message, true);
  }
}
window.updateRouterOSWANCommands = updateRouterOSWANCommands;
$("set-wan_map").addEventListener("input", updateRouterOSWANCommands);
if (settingsLoaded || $("set-wan_map").value.trim()) updateRouterOSWANCommands();
const pageHost = window.location.hostname;
if (/^(?:\d{1,3}\.){3}\d{1,3}$/.test(pageHost) && !pageHost.startsWith("127.") && pageHost !== "0.0.0.0")
  $("ros-server").value = pageHost;
const routerosCommandInfo = {
  install: ["一键接入指令", "包含访问日志和系统日志。整段复制到 ROS 终端执行，包含最外层大括号。"],
  check: ["接入状态", "复制到 ROS 终端查看，不修改配置。"],
  remove: ["撤销指令", "停止 ROS Watch 日志采集，保留 WAN 接口成员。"],
  upgrade: ["仅补充系统日志", "已有接入时使用。整段复制到 ROS 终端执行，包含最外层大括号。"],
  remove_system: ["仅撤销系统日志", "保留防火墙访问日志采集。"],
  wan: ["WAN 接口配置", "外网接口已在 WAN 列表中可跳过。添加指令包含最外层大括号，整段执行。"],
  more: ["更多操作", ""],
};
async function openRouterOSCommand(kind) {
  if (kind === "install" && !$("routeros-form").reportValidity()) return;
  const request = ++routerosRequest;
  const [title, note] = routerosCommandInfo[kind];
  $("ros-dialog-title").textContent = title;
  $("ros-dialog-note").textContent = note;
  settingsMessage("ros-copy-result", "");
  const command = kind !== "wan" && kind !== "more";
  $("ros-command-view").classList.toggle("hidden", !command);
  $("ros-wan-view").classList.toggle("hidden", kind !== "wan");
  $("ros-more-view").classList.toggle("hidden", kind !== "more");
  $("ros-script").value = "";
  routerosCommandCopyButton.disabled = true;
  if (kind === "wan") updateRouterOSWANCommands();
  if (!routerosDialog.open) routerosDialog.showModal();
  if (!command) return;
  settingsMessage("ros-copy-result", "正在生成…");
  try {
    const body = { command: kind };
    if (kind === "install") {
      body.server_address = $("ros-server").value.trim();
      body.syslog_port = Number($("ros-port").value);
    }
    const result = await postSettings("/api/routeros-script", body);
    if (request !== routerosRequest || !routerosDialog.open) return;
    $("ros-script").value = result[kind];
    $("ros-script").scrollTop = 0;
    $("ros-script").scrollLeft = 0;
    routerosCommandCopyButton.disabled = false;
    settingsMessage("ros-copy-result", "");
  } catch (error) {
    if (request === routerosRequest && routerosDialog.open)
      settingsMessage("ros-copy-result", error.message, true);
  }
}
$("routeros-form").addEventListener("submit", event => {
  event.preventDefault();
  openRouterOSCommand("install");
});
for (const button of document.querySelectorAll("[data-ros-open]")) {
  if (button.dataset.rosOpen !== "install")
    button.addEventListener("click", () => openRouterOSCommand(button.dataset.rosOpen));
}
$("ros-dialog-close").addEventListener("click", () => routerosDialog.close());
routerosDialog.addEventListener("close", () => { ++routerosRequest; });
$("ros-open-blocklist").addEventListener("click", () => {
  routerosDialog.close();
  openSourceBlocklist();
});
for (const button of routerosCopyButtons) button.addEventListener("click", async () => {
  const field = $(button.dataset.rosCopy);
  if (!field.value) return;
  try {
    if (navigator.clipboard?.writeText) await navigator.clipboard.writeText(field.value);
    else {
      field.focus();
      field.select();
      field.setSelectionRange(0, field.value.length);
      if (!document.execCommand("copy")) throw Error("manual copy required");
    }
    settingsMessage("ros-copy-result", "已复制");
  } catch {
    field.focus();
    field.select();
    settingsMessage("ros-copy-result", "请全选代码手动复制", true);
  }
});
