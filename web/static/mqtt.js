"use strict";
let mqttLoaded = false, mqttLoading = false, mqttDirty = false, mqttSaving = false;
const mqttFields = ["broker", "username", "device_name", "discovery_prefix"];
function renderMQTTStatus(state) {
  $("mqtt-status-summary").textContent = state.last_error || (state.connected ? "已连接" : "未连接");
  $("mqtt-status-summary").classList.toggle("error", Boolean(state.last_error));
  dl("mqtt-status", [["Broker 连接", state.connected ? "已连接" : "未连接"], ["成功发布轮次", format(state.published)], ["最近成功发布", date(state.last_success)], ["最近错误", state.last_error || "无"]]);
}
function populateMQTT(data) {
  const s = data.settings;
  $("mqtt-enabled").checked = s.enabled;
  for (const key of mqttFields) $("mqtt-" + key).value = s[key];
  $("mqtt-password").value = "";
  $("mqtt-clear-password").checked = false;
  $("mqtt-clear-field").classList.toggle("hidden", !s.password_set);
  $("mqtt-password-hint").textContent = s.password_set ? "密码已保存，留空保留；需要移除时勾选清除。" : "可留空，按 Broker 配置填写。";
  renderMQTTStatus(data.status);
  mqttLoaded = true; mqttDirty = false;
}
async function loadMQTT() {
  if (mqttLoaded || mqttLoading) return;
  mqttLoading = true;
  try { populateMQTT(await api("/api/mqtt")); }
  catch (error) { settingsMessage("mqtt-result", error.message, true); }
  finally { mqttLoading = false; }
}
const loadOtherSettings = window.loadSettings;
window.loadSettings = function () { loadOtherSettings(); if (settingsTab === "mqtt") loadMQTT(); };
$("mqtt-form").addEventListener("input", () => { mqttDirty = true; });
$("mqtt-form").addEventListener("change", () => { mqttDirty = true; });
$("mqtt-form").addEventListener("submit", async event => {
  event.preventDefault();
  if (mqttSaving) return;
  mqttSaving = true; event.submitter.disabled = true;
  try {
    if (!mqttLoaded) throw Error("设置尚未加载，请稍后重试");
    const body = { enabled: $("mqtt-enabled").checked, password: $("mqtt-password").value, clear_password: $("mqtt-clear-password").checked };
    for (const key of mqttFields) body[key] = $("mqtt-" + key).value;
    const result = await postSettings("/api/mqtt", body);
    populateMQTT({ settings: result.settings, status: (await api("/api/mqtt")).status });
    settingsMessage("mqtt-result", result.message);
  } catch (error) { settingsMessage("mqtt-result", error.message, true); }
  finally { mqttSaving = false; event.submitter.disabled = false; }
});
$("mqtt-publish").addEventListener("click", async () => {
  const button = $("mqtt-publish"); button.disabled = true;
  try {
    if (!mqttLoaded || mqttDirty || mqttSaving) throw Error("请先保存接入设置，再立即发布");
    const result = await postSettings("/api/mqtt/publish", {});
    settingsMessage("mqtt-result", result.message);
  } catch (error) { settingsMessage("mqtt-result", error.message, true); }
  finally { button.disabled = false; try { renderMQTTStatus((await api("/api/mqtt")).status); } catch (_) {} }
});
setInterval(async () => {
  if (document.hidden || view !== "settings" || settingsTab !== "mqtt") return;
  try { renderMQTTStatus((await api("/api/mqtt")).status); } catch (_) {}
}, 10000);
