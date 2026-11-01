"use strict";
const routerosFields = ["ros-install", "ros-upgrade", "ros-check", "ros-remove", "ros-remove-system"];
const routerosCopyButtons = [...document.querySelectorAll("[data-ros-copy]")];
const pageHost = window.location.hostname;
if (/^(?:\d{1,3}\.){3}\d{1,3}$/.test(pageHost) && !pageHost.startsWith("127.") && pageHost !== "0.0.0.0")
  $("ros-server").value = pageHost;
$("routeros-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const button = event.submitter;
  button.disabled = true;
  for (const field of routerosFields) $(field).value = "";
  for (const copy of routerosCopyButtons) copy.disabled = true;
  settingsMessage("ros-result", "正在生成指令…");
  try {
    const result = await postSettings("/api/routeros-script", {
      server_address: $("ros-server").value.trim(),
      syslog_port: Number($("ros-port").value),
    });
    $("ros-install").value = result.install;
    $("ros-upgrade").value = result.upgrade;
    $("ros-remove-system").value = result.remove_system;
    $("ros-check").value = result.check;
    $("ros-remove").value = result.remove;
    for (const copy of routerosCopyButtons) copy.disabled = false;
    settingsMessage("ros-result", "已生成。首次接入复制完整指令；已有 ROSWatch 的设备使用补充系统日志指令，它沿用现有日志目的地址和端口。");
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
