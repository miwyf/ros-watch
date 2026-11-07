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

const routerosFields = ["ros-install", "ros-repair", "ros-upgrade", "ros-check", "ros-remove", "ros-remove-system"];
const routerosCopyButtons = [...document.querySelectorAll("[data-ros-copy]")];
const routerosGeneratedCopyButtons = routerosCopyButtons.filter(button => routerosFields.includes(button.dataset.rosCopy));
const routerosWANCopyButtons = routerosCopyButtons.filter(button => ["ros-wan-check", "ros-wan-example"].includes(button.dataset.rosCopy));
function updateRouterOSWANCommands() {
  $("ros-wan-check").value = "";
  $("ros-wan-example").value = "";
  for (const button of routerosWANCopyButtons) button.disabled = true;
  try {
    const commands = buildWANInterfaceCommands($("set-wan_map").value);
    $("ros-wan-check").value = commands.check;
    $("ros-wan-example").value = commands.add;
    for (const button of routerosWANCopyButtons) button.disabled = false;
    settingsMessage("ros-wan-result", "当前接口：" + commands.interfaces.join("、") + "。已按当前填写内容生成，未保存的修改也会使用。");
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
$("routeros-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const button = event.submitter;
  button.disabled = true;
  for (const field of routerosFields) $(field).value = "";
  for (const copy of routerosGeneratedCopyButtons) copy.disabled = true;
  settingsMessage("ros-result", "正在生成指令…");
  try {
    const result = await postSettings("/api/routeros-script", {
      server_address: $("ros-server").value.trim(),
      syslog_port: Number($("ros-port").value),
    });
    $("ros-install").value = result.install;
    $("ros-repair").value = result.repair;
    $("ros-upgrade").value = result.upgrade;
    $("ros-remove-system").value = result.remove_system;
    $("ros-check").value = result.check;
    $("ros-remove").value = result.remove;
    for (const copy of routerosGeneratedCopyButtons) copy.disabled = false;
    settingsMessage("ros-result", "已生成。包括最外层大括号，整段一次粘贴执行。首次接入用完整指令；已有观察规则但日志转发未建好用“仅修复日志转发”；已有 ROSWatch 用“补充系统日志”。");
  } catch (error) {
    settingsMessage("ros-result", error.message, true);
  } finally {
    button.disabled = false;
  }
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
    settingsMessage("ros-result", "已复制，请在 ROS 终端粘贴执行。");
  } catch {
    field.focus();
    field.select();
    settingsMessage("ros-result", "浏览器未允许自动复制，请全选或长按代码手动复制。", true);
  }
});
