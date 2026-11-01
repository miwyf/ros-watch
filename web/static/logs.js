"use strict";
let logCursor = null, logNext = null, logRequest = null;
const logFields = ["hours", "category", "severity", "event", "device", "username", "source", "q"];
const logCategoryNames = { login: "登录", config: "配置变更", network: "接口 / 拨号", script: "脚本", system: "系统", firewall: "防火墙", other: "其他" };
const logEventNames = { success: "登录成功", failure: "登录失败", logout: "退出登录", change: "配置变更", state: "网络状态", reboot: "重启", script: "脚本事件", access: "访问尝试", message: "消息" };
const logSeverityNames = { info: "信息", notice: "通知", warning: "警告", error: "错误", critical: "严重", alert: "告警", emergency: "紧急", debug: "调试", unknown: "未知" };
function logParams() {
  const p = new URLSearchParams();
  for (const key of logFields) { const value = $("logs-" + key).value.trim(); if (value) p.set(key, value); }
  return p;
}
async function refreshLogs(reset = false) {
  if (reset) logCursor = null;
  if (logRequest) logRequest.abort();
  const request = new AbortController(); logRequest = request;
  const p = logParams(); $("logs-export").href = "/api/logs/export?" + p;
  if (logCursor) p.set("before", logCursor);
  try {
    const r = await api("/api/logs?" + p, { signal: request.signal });
    if (request !== logRequest) return;
    $("error-banner").classList.add("hidden");
    $("logs-total").textContent = format(r.totals.total);
    $("logs-failures").textContent = format(r.totals.failures);
    $("logs-successes").textContent = format(r.totals.successes);
    $("logs-changes").textContent = format(r.totals.changes) + " / " + format(r.totals.warnings);
    logNext = r.next; $("logs-next").disabled = !logNext;
    $("logs-page").textContent = (logCursor ? "较早记录" : "最新记录") + " · " + r.rows.length + " 条";
    const c = r.status.counters;
    const losses = ["rate_dropped", "queue_dropped", "storage_dropped", "oversized"].reduce((n, k) => n + (c[k] || 0), 0);
    $("logs-coverage").textContent = "原始日志 " + format(r.coverage.stored) + " 条；覆盖 " + date(r.coverage.oldest) + " 至 " + date(r.coverage.newest) + "。UDP 可能丢包，与访问记录均按设置的保留天数清理。";
    $("notice").textContent = !r.status.sources_configured ? "请先在系统设置保存路由器日志来源白名单。" : r.coverage.system_logs ? "已收到系统日志。未知格式保留原文；UDP 传输可能丢包。" : "尚未收到系统事件，请在 RouterOS 接入页生成并执行补充系统日志指令。防火墙原文沿用现有接入。";
    if (losses) $("notice").textContent += " 本次运行有 " + format(losses) + " 条报文因保护限制未入库。";
    renderStorage(r.status.storage);
    if (r.status.storage.space_pruned) $("notice").textContent += " 曾因空间不足提前清理 " + format(r.status.storage.space_pruned) + " 条，最近 " + date(r.status.storage.last_space_cleanup) + "。";
    if (r.status.storage.space_state !== "normal") $("notice").textContent += " " + storageMessage(r.status.storage);
    if (r.status.error) $("notice").textContent += " 接收异常，请查看接收状态。";
    $("notice").classList.toggle("danger", !!r.status.error || losses > 0 || r.status.storage.space_state !== "normal");
    $("connection").classList.toggle("stale", !!r.status.error || r.status.storage.space_state !== "normal");
    $("connection-text").textContent = r.status.storage.space_state !== "normal" ? "存储空间异常" : r.status.error ? "接收异常" : r.status.last_received && Date.now()/1000-r.status.last_received < 120 ? "正在接收" : "等待日志";
    $("demo-banner").classList.toggle("hidden", !r.status.demo);
    const repeated = $("logs-repeated"); repeated.replaceChildren();
    for (const f of r.failures) repeated.append(node("p", "连续失败提示：" + f.source + " / " + f.username + "，同一 5 分钟固定窗口内 " + format(f.attempts) + " 次；最后 " + date(f.last), "notice danger"));
    const list = $("logs-list"); list.replaceChildren();
    if (!r.rows.length) {
      const row=node("tr"), cell=node("td","当前筛选没有记录。新接入不会补回 ROS 已覆盖的历史日志。","empty"); cell.colSpan=6;row.append(cell);list.append(row);
    }
    for (const l of r.rows) {
      const tr=node("tr",undefined,"event-row"), detail=node("tr",undefined,"raw-row hidden"), cell=node("td");cell.colSpan=6;
      const button=node("button", l.message || "（空消息）", "text-button log-message");button.type="button";button.setAttribute("aria-expanded","false");
      const toggle=()=>{detail.classList.toggle("hidden");button.setAttribute("aria-expanded",String(!detail.classList.contains("hidden")));};
      tr.append(node("td",date(l.ts)),node("td",l.device || l.sender),node("td",(logCategoryNames[l.category]||l.category)+" / "+(logSeverityNames[l.severity]||l.severity)),node("td",(l.username||"—")+" / "+(l.source||"未注明")),node("td",(logEventNames[l.event]||l.event)+(l.method?" / "+l.method:"")));
      const message=node("td");message.append(button);tr.append(message);tr.addEventListener("click",toggle);
      cell.append(node("div","发送设备："+l.sender+"；原设备时间："+(l.device_time||"未注明")+"；类别标记："+(l.topic||"未注明")),node("pre",l.raw));detail.append(cell);list.append(tr,detail);
    }
  } catch (error) { if (error.name !== "AbortError") fail(error.message); }
  finally { if (logRequest === request) logRequest = null; }
}
$("logs-form").addEventListener("submit", e => { e.preventDefault(); refreshLogs(true); });
for (const key of ["hours","category","severity","event"]) $("logs-"+key).addEventListener("change",()=>refreshLogs(true));
$("logs-clear").addEventListener("click",()=>{for(const key of logFields) $("logs-"+key).value=key==="hours"?"24":"";refreshLogs(true);});
$("logs-next").addEventListener("click",()=>{logCursor=logNext;refreshLogs();});
$("logs-first").addEventListener("click",()=>refreshLogs(true));
