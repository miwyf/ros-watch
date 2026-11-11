"use strict";

const blockedSourceList = "blocked-sources";
const selectedScanSources = new Set();
let visibleScanSources = [];

function isBlockableSource(address) {
  return typeof address === "string" && /^(?:\d{1,3}\.){3}\d{1,3}$/.test(address) &&
    address.split(".").every(part => String(Number(part)) === part && Number(part) <= 255);
}

function buildBlockedSourceCommands(addresses) {
  const sources = [...new Set(addresses)];
  if (!sources.every(isBlockableSource)) throw Error("当前只支持生成单个 IPv4 地址的封禁指令");
  const match = address => `list="${blockedSourceList}" and address=${address}`;
  return {
    block: sources.map(address =>
      `:if ([:len [/ip firewall address-list find where ${match(address)}]] = 0) do={ /ip firewall address-list add list="${blockedSourceList}" address=${address} comment="Manual block: port scan"; }`
    ).join("\n"),
    unblock: sources.map(address =>
      `/ip firewall address-list remove [find where ${match(address)}]`
    ).join("\n"),
    setup: `{
    :local rule [/ip firewall raw find where comment="Drop blocked sources" and chain=prerouting and src-address-list="${blockedSourceList}" and action=drop];
    :if ([:len $rule] > 1) do={ :error "Multiple blocked-sources rules found; check RAW rules first"; };
    :if ([:len $rule] = 0) do={
        /ip firewall raw add chain=prerouting src-address-list="${blockedSourceList}" action=drop log=no comment="Drop blocked sources";
        :set rule [/ip firewall raw find where comment="Drop blocked sources" and chain=prerouting and src-address-list="${blockedSourceList}" and action=drop];
    };
    :set rule [:pick $rule 0];
    /ip firewall raw set $rule disabled=no;
    :local first [:pick [/ip firewall raw find] 0];
    :if ($rule != $first) do={ /ip firewall raw move $rule destination=$first; };
}`,
    check: `/ip firewall address-list print detail where list="${blockedSourceList}"
/ip firewall raw print stats where chain=prerouting and src-address-list="${blockedSourceList}" and action=drop`,
  };
}

function updateScanSelection(rows) {
  visibleScanSources = [...new Set(rows.map(row => row.src).filter(isBlockableSource))];
  for (const address of selectedScanSources)
    if (!visibleScanSources.includes(address)) selectedScanSources.delete(address);
  updateScanSelectionControls();
}

function updateScanSelectionControls() {
  const all = document.getElementById("scan-select-all");
  all.disabled = !visibleScanSources.length;
  all.checked = visibleScanSources.length > 0 && selectedScanSources.size === visibleScanSources.length;
  all.indeterminate = selectedScanSources.size > 0 && !all.checked;
  const copy = document.getElementById("scan-copy-selected");
  copy.disabled = selectedScanSources.size === 0;
  copy.textContent = selectedScanSources.size ? `复制所选封禁命令（${selectedScanSources.size}）` : "复制所选封禁命令";
}

function scanSourceCheckbox(address) {
  const input = document.createElement("input");
  input.type = "checkbox";
  input.className = "scan-source-select";
  input.dataset.source = address;
  input.setAttribute("aria-label", "选择来源 " + address);
  input.disabled = !isBlockableSource(address);
  input.checked = selectedScanSources.has(address);
  input.addEventListener("change", () => {
    if (input.checked) selectedScanSources.add(address);
    else selectedScanSources.delete(address);
    updateScanSelectionControls();
  });
  return input;
}

function scanSourceBlockButton(address) {
  const button = document.createElement("button");
  button.type = "button";
  button.className = "text-button scan-block-button";
  button.textContent = "复制封禁命令";
  button.disabled = !isBlockableSource(address);
  if (button.disabled) button.title = "当前仅支持 IPv4 来源";
  button.addEventListener("click", () => openSourceBlocklist([address], true));
  return button;
}

function sourceBlocklistMessage(text, error = false) {
  const status = document.getElementById("blocklist-result");
  status.textContent = text;
  status.classList.toggle("danger", error);
}

async function copySourceBlocklist(fieldID) {
  const field = document.getElementById(fieldID);
  if (!field.value) return;
  // Keep the text available for HTTP deployments and denied clipboard access.
  const fallback = () => {
    field.closest("details")?.setAttribute("open", "");
    field.focus();
    field.select();
    field.setSelectionRange(0, field.value.length);
    field.scrollLeft = 0;
    return document.execCommand("copy");
  };
  try {
    if (navigator.clipboard?.writeText) {
      try { await navigator.clipboard.writeText(field.value); }
      catch { if (!fallback()) throw Error("manual copy required"); }
    } else if (!fallback()) throw Error("manual copy required");
    sourceBlocklistMessage("已复制，待在 RouterOS 终端执行。网页未读取路由器名单，无法确认执行状态。");
  } catch {
    field.closest("details")?.setAttribute("open", "");
    field.focus();
    field.select();
    field.scrollLeft = 0;
    sourceBlocklistMessage("浏览器未允许自动复制，请全选或长按代码手动复制，再到 RouterOS 终端执行。", true);
  }
}

function openSourceBlocklist(addresses = [], copy = false) {
  const commands = buildBlockedSourceCommands(addresses);
  const dialog = document.getElementById("blocklist-dialog");
  document.getElementById("blocklist-title").textContent = addresses.length ? "来源 IP 封禁指令" : "来源黑名单配置";
  document.getElementById("blocklist-targets").textContent = [...new Set(addresses)].join("、");
  document.getElementById("blocklist-address-commands").classList.toggle("hidden", !addresses.length);
  document.getElementById("blocklist-setup-details").open = !addresses.length;
  document.getElementById("blocklist-unblock-details").open = false;
  document.getElementById("blocklist-check-details").open = false;
  for (const [name, text] of Object.entries(commands)) document.getElementById("blocklist-" + name).value = text;
  sourceBlocklistMessage(addresses.length ? "首次使用请先执行下方配置指令，再添加来源 IP。" : "执行一次首次配置，以后只需往 blocked-sources 名单添加 IP。");
  if (!dialog.open) dialog.showModal();
  if (copy) copySourceBlocklist("blocklist-block");
}

document.addEventListener("DOMContentLoaded", () => {
  document.getElementById("scan-select-all").addEventListener("change", event => {
    selectedScanSources.clear();
    if (event.target.checked) for (const address of visibleScanSources) selectedScanSources.add(address);
    for (const input of document.querySelectorAll(".scan-source-select"))
      input.checked = selectedScanSources.has(input.dataset.source);
    updateScanSelectionControls();
  });
  document.getElementById("scan-copy-selected").addEventListener("click", () =>
    openSourceBlocklist(visibleScanSources.filter(address => selectedScanSources.has(address)), true));
  for (const button of document.querySelectorAll("[data-blocklist-open]"))
    button.addEventListener("click", () => openSourceBlocklist());
  for (const button of document.querySelectorAll("[data-blocklist-copy]"))
    button.addEventListener("click", () => copySourceBlocklist(button.dataset.blocklistCopy));
  document.getElementById("blocklist-close").addEventListener("click", () => document.getElementById("blocklist-dialog").close());
});
