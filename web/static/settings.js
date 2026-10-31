"use strict";
const runtimeNumbers = [
  "retention_days",
  "max_rows",
  "max_db_mb",
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
window.loadSettings = async function () {
  try {
    const [runtime, account] = await Promise.all([
      api("/api/settings"),
      api("/api/account"),
    ]);
    const s = runtime.settings;
    $("set-allowed_senders").value = s.allowed_senders
      .split(",")
      .filter(Boolean)
      .join("\n");
    $("set-wan_map").value = Object.entries(s.wan_map)
      .map(([key, value]) => key + "=" + value)
      .join("\n");
    for (const key of runtimeNumbers) $("set-" + key).value = s[key];
    $("set-sampling").value = s.sampling;
    $("account-username").value = account.username;
  } catch (e) {
    settingsMessage("runtime-result", e.message, true);
  }
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
