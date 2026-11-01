"use strict";
const runtimeNumbers = [
  "retention_days",
  "min_free_mb",
  "queue_size",
  "ingest_per_second",
  "scan_ports",
  "scan_window",
];
const csrfToken = () =>
  document.querySelector('meta[name="csrf-token"]').content;
function settingsMessage(id, text, error = false) {
  $(id).textContent = text;
  $(id).className = error ? "error" : "success";
}
function postSettings(path, body) {
  return api(path, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      "X-CSRF-Token": csrfToken(),
    },
    body: JSON.stringify(body),
  });
}
let settingsLoaded = false;
let settingsLoading = false;
window.loadSettings = async function () {
  if (settingsLoaded || settingsLoading) return;
  settingsLoading = true;
  try {
    const [runtime, account, notifications] = await Promise.all([
      api("/api/settings"),
      api("/api/account"),
      api("/api/notifications"),
    ]);
    populateNotifications(notifications);
    const s = runtime.settings;
    renderStorage(runtime.storage);
    $("set-allowed_senders").value = s.allowed_senders
      .split(",")
      .filter(Boolean)
      .join("\n");
    $("set-wan_map").value = Object.entries(s.wan_map)
      .map(([key, value]) => key + "=" + value)
      .join("\n");
    for (const key of runtimeNumbers) $("set-" + key).value = s[key];
    $("set-sampling").value = s.sampling;
    $("set-disk_low_action").value = s.disk_low_action;
    $("account-username").value = account.username;
    settingsLoaded = true;
  } catch (e) {
    settingsMessage("runtime-result", e.message, true);
  } finally { settingsLoading = false; }
};
$("runtime-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  const button = e.submitter;
  button.disabled = true;
  try {
    const mapping = Object.create(null);
    for (const line of $("set-wan_map")
      .value.split(/\r?\n/)
      .map((l) => l.trim())
      .filter(Boolean)) {
      const i = line.indexOf("=");
      if (i < 1 || !line.slice(i + 1).trim())
        throw Error("WAN 映射每行须为 接口名称=显示名称");
      const key = line.slice(0, i).trim();
      if (Object.hasOwn(mapping, key)) throw Error("WAN 接口名称不能重复");
      mapping[key] = line.slice(i + 1).trim();
    }
    const body = {
      allowed_senders: $("set-allowed_senders").value,
      wan_map: mapping,
      sampling: $("set-sampling").value,
      disk_low_action: $("set-disk_low_action").value,
    };
    for (const key of runtimeNumbers) body[key] = Number($("set-" + key).value);
    const result = await postSettings("/api/settings", body);
    settingsMessage("runtime-result", result.message);
    refresh();
  } catch (error) {
    settingsMessage("runtime-result", error.message, true);
  } finally {
    button.disabled = false;
  }
});
$("account-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  const button = e.submitter;
  button.disabled = true;
  try {
    const body = {
      username: $("account-username").value.trim(),
      current_password: $("account-current").value,
      new_password: $("account-new").value,
      confirm_password: $("account-confirm").value,
    };
    if (body.new_password !== body.confirm_password)
      throw Error("两次新密码不一致");
    const result = await postSettings("/api/account", body);
    $("account-form").reset();
    location.assign("/login?changed=1");
  } catch (error) {
    settingsMessage("account-result", error.message, true);
    button.disabled = false;
  }
});

let notificationSavedPlatform = "";
const notificationFlags = ["login_failures", "login_success", "storage", "scans"];
function notificationVisibility() {
  const wecom = $("notify-platform").value === "wecom";
  $("notify-secret-field").classList.toggle("hidden", wecom);
  if (wecom) $("notify-secret").value = "";
  $("notify-trusted-field").classList.toggle("hidden", !$("notify-login_success").checked);
}
function renderNotificationStatus(state) {
  $("notification-status-summary").textContent = state.last_error ? "最近发送失败：" + state.last_error : state.last_success ? "最近成功：" + date(state.last_success) : "尚未发送";
  $("notification-status-summary").classList.toggle("error", Boolean(state.last_error));
  const timestamp = value => value ? new Date(value * 1000).toLocaleString() : "暂无";
  dl("notification-status", [
    ["平台接受次数", state.sent], ["发送失败次数", state.failed],
    ["冷却抑制次数", state.suppressed], ["通知队列丢弃", state.dropped],
    ["通知观察丢弃", state.observed_dropped],
    ["最近尝试", timestamp(state.last_attempt)], ["最近成功", timestamp(state.last_success)],
    ["最近发送错误", state.last_error || "无"],
  ]);
}
function populateNotifications(data) {
  const s = data.settings;
  notificationSavedPlatform = s.platform;
  $("notify-enabled").checked = s.enabled;
  $("notify-platform").value = s.platform;
  $("notify-webhook").value = "";
  $("notify-secret").value = "";
  $("notify-clear-secret").checked = false;
  $("notify-clear-field").classList.toggle("hidden", !s.secret_set);
  $("notify-webhook-hint").textContent = s.webhook_set ? "地址已保存，留空保留。更换平台时请重新填写。" : "保存后可测试发送。";
  $("notify-secret-hint").textContent = s.secret_set ? "密钥已保存，留空保留；需要移除时勾选清除。" : "机器人启用加签时填写。";
  for (const flag of notificationFlags) $("notify-" + flag).checked = s[flag];
  $("notify-trusted_admins").value = s.trusted_admins.split(",").filter(Boolean).join("\n");
  renderNotificationStatus(data.status);
  notificationVisibility();
}
$("notify-platform").addEventListener("change", notificationVisibility);
$("notify-login_success").addEventListener("change", notificationVisibility);
$("notification-form").addEventListener("submit", async event => {
  event.preventDefault();
  const button = event.submitter;
  button.disabled = true;
  try {
    const body = {
      enabled: $("notify-enabled").checked, platform: $("notify-platform").value,
      webhook: $("notify-webhook").value.trim(), secret: $("notify-secret").value.trim(),
      clear_secret: $("notify-clear-secret").checked, trusted_admins: $("notify-trusted_admins").value,
    };
    for (const flag of notificationFlags) body[flag] = $("notify-" + flag).checked;
    const result = await postSettings("/api/notifications", body);
    populateNotifications(await api("/api/notifications"));
    settingsMessage("notification-result", result.message);
  } catch (error) {
    settingsMessage("notification-result", error.message, true);
  } finally { button.disabled = false; }
});
$("notification-test").addEventListener("click", async () => {
  const button = $("notification-test");
  button.disabled = true;
  try {
    if ($("notify-platform").value !== notificationSavedPlatform || $("notify-webhook").value || $("notify-secret").value || $("notify-clear-secret").checked)
      throw Error("请先保存通知设置，再测试发送");
    const result = await postSettings("/api/notifications/test", {});
    settingsMessage("notification-result", result.message);
  } catch (error) {
    settingsMessage("notification-result", error.message, true);
  } finally {
    button.disabled = false;
    try { renderNotificationStatus((await api("/api/notifications")).status); } catch (_) {}
  }
});
setInterval(async () => {
  if (document.hidden || $("settings-view").classList.contains("hidden")) return;
  try { renderNotificationStatus((await api("/api/notifications")).status); } catch (_) {}
}, 30000);
