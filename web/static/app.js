"use strict";
const $ = (id) => document.getElementById(id);
const format = (n) => new Intl.NumberFormat("zh-CN").format(n || 0);
const date = (ts) =>
  ts
    ? new Date(ts * 1000).toLocaleString("zh-CN", { hour12: false })
    : "尚无记录";
const clock = (ts) =>
  new Date(ts * 1000).toLocaleTimeString("zh-CN", {
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  });
const node = (tag, text, cls) => {
  const e = document.createElement(tag);
  if (text !== undefined) e.textContent = text;
  if (cls) e.className = cls;
  return e;
};
let view = "overview",
  next = null,
  cursor = null,
  loading = false,
  pending = false;
const filterNames = ["hours", "wan", "scope", "proto", "outcome", "direction"];
const viewNames = ["overview", "map", "events", "logs", "system", "settings"];
const settingsTabNames = ["connection", "logs", "account", "notifications", "mqtt"];
let settingsTab = "connection";
let receiverState = null, accessCoverage = null, systemCoverage = null, summaryUpdatedAt = null;
let autoRefresh = true, overviewScroll = 0, eventRequest = null;
function restoreLocation() {
  const query = new URLSearchParams(location.search);
  for (const key of filterNames) {
    const fallback = key === "hours" ? "12" : key === "direction" ? "inbound" : "";
    const value = query.get(key) ?? fallback;
    if (key === "wan" && value && value.length <= 256 && ![...$(key).options].some(option=>option.value===value)) $(key).add(new Option(value,value));
    $(key).value = [...$(key).options].some(option=>option.value===value) ? value : fallback;
  }
  for (const key of ["src","dport"]) $(key).value = query.get(key) || "";
  const order = query.get("scan_order");
  $("scan-order").value = ["latest","ports","count"].includes(order) ? order : "latest";
  cursor = /^[1-9][0-9]*$/.test(query.get("before") || "") ? query.get("before") : null;
  for (const key of ["hours","category","severity","event","device","username","source","q"]) {
    const value=query.get("logs_"+key);
    $("logs-"+key).value=value ?? (key==="hours" ? "12" : "");
  }
}
function writeLocation(push = false) {
  const fragment = view === "overview" ? "" : view === "settings" && settingsTab !== "connection" ? "#settings/"+settingsTab : "#"+view;
  const values=params(view === "events");
  if(view==="events" && cursor)values.set("before",cursor);
  if(view==="logs")for(const key of ["hours","category","severity","event","device","username","source","q"]){const value=$("logs-"+key).value.trim();if(value)values.set("logs_"+key,value);}
  const query = values.toString();
  const target = location.pathname + (query ? "?"+query : "") + fragment;
  if (target !== location.pathname+location.search+location.hash) history[push ? "pushState" : "replaceState"](null,"",target);
  window.ROSHome?.filters();
}
function updateFilterSummary() {
  $("filter-summary").replaceChildren(node("span",$("hours").selectedOptions[0].textContent+" · ","filter-range-label"),node("span",summaryUpdatedAt ? "更新于 "+clock(summaryUpdatedAt) : "正在更新…"));
}
function renderReceiverStatus() {
  const notice = $("notice");
  notice.replaceChildren();
  if (!receiverState) { notice.classList.add("hidden"); return; }
  const s = receiverState;
  const losses = ["rate_dropped", "queue_dropped", "storage_dropped", "oversized"].reduce((n, k) => n + (s.counters[k] || 0), 0);
  const problems = [];
  let target = "#system", action = "查看接收状态";
  if (!s.sources_configured && !s.demo) {
    problems.push("尚未配置日志来源，请添加允许发送日志的设备。");
    target = "#settings"; action = "配置日志来源";
  } else if (!s.demo && ((view === "overview" || view === "events") && accessCoverage && !accessCoverage.stored)) {
    problems.push("尚无访问记录，请检查日志发送目标和 WAN 接口映射。");
    target = "#settings"; action = "检查接入设置";
  } else if (!s.demo && view === "logs" && systemCoverage && !systemCoverage.system_logs) {
    problems.push("尚无系统事件，可在接入设置中补充系统日志。");
    target = "#settings"; action = "查看接入设置";
  }
  if (losses) problems.push("本次运行有 " + format(losses) + " 条报文未入库，请查看接收计数。");
  if (s.storage.space_state !== "normal") problems.push(storageMessage(s.storage));
  if (s.error) problems.push("接收服务异常：" + ({disk_low:"磁盘空间不足，暂停写入",storage_failed:"数据库写入失败",maintenance_failed:"数据清理异常",receiver_failed:"接收器已停止"}[s.error] || s.error) + "。");
  const danger = !!s.error || losses > 0 || s.storage.space_state !== "normal";
  if (danger) { target = "#system"; action = "查看接收状态"; }
  if (problems.length) {
    notice.append(node("span", problems.join(" ")));
    const link = node("a", action, "notice-link"); link.href = target; notice.append(link);
  }
  notice.classList.toggle("hidden", view === "settings" || !problems.length);
  notice.classList.toggle("danger", danger);
  $("connection").title = s.demo ? "演示模式 · 均为合成数据，与实际网络无关" : s.last_received ? "最后收到有效日志：" + date(s.last_received) : "尚未收到有效日志";
  $("connection").classList.toggle("stale", !!s.error || s.storage.space_state !== "normal");
  $("connection-text").textContent = s.demo ? "演示数据" : s.storage.space_state !== "normal" ? "存储空间异常" : s.error ? "接收异常" : s.last_received && Date.now()/1000-s.last_received < 120 ? "正在接收" : "等待日志";

}
function params(search = false) {
  const p = new URLSearchParams();
  for (const k of filterNames) if ($(k).value) p.set(k, $(k).value);
  if (search)
    for (const k of ["src", "dport"])
      if ($(k).value.trim()) p.set(k, $(k).value.trim());
  p.set("scan_order", $("scan-order").value);
  return p;
}
async function api(path, options = {}) {
  const r = await fetch(path, { ...options, credentials: "same-origin" });
  if (r.status === 401) {
    location.assign("/login");
    throw Error("登录已过期");
  }
  const data = await r.json();
  if (!r.ok) throw Error(data.error || "暂时无法获取数据");
  return data;
}
function fail(message) {
  $("error-banner").textContent = message;
  $("error-banner").classList.remove("hidden");
  $("connection").classList.add("stale");
  $("connection-text").textContent = "刷新失败";
}
function showView(name, push = false, keepCursor = false) {
  let requestedTab = null;
  if (name === "routeros") { name = "settings"; requestedTab = "connection"; }
  else if (name.startsWith("settings/")) { requestedTab = name.slice(9); name = "settings"; }
  if (requestedTab === "routeros") requestedTab = "connection";
  if (name === "settings" && requestedTab !== null)
    settingsTab = settingsTabNames.includes(requestedTab) ? requestedTab : "connection";
  if (!viewNames.includes(name)) name = "overview";
  if(name==="events" && !keepCursor) cursor=null;
  if (view === "overview" && name !== "overview") overviewScroll = window.scrollY;
  view = name;
  document.body.classList.toggle("map-page", name === "map");
  writeLocation(push);
  document.querySelectorAll(".mobile-nav-more").forEach(menu=>menu.open=false);
  const titles = {
    overview: "访问概览",
    map: "来源地图",
    events: "访问记录",
    logs: "系统日志",
    system: "接收状态",
    settings: "系统设置",
  };
  $("view-title").textContent = titles[name];
  updateFilterSummary();
  for (const v of viewNames)
    $(v + "-view").classList.toggle("hidden", name !== v);
  document
    .querySelectorAll("[data-view]")
    .forEach((b) => b.classList.toggle("active", b.dataset.view === name));
  window.scrollTo({ top: name === "overview" ? overviewScroll : 0, behavior: "instant" });

  document
    .querySelector(".filterbar")
    .classList.toggle("hidden", name === "settings" || name === "logs");
  $("refresh").classList.toggle("hidden", name === "settings");
  $("auto-refresh").classList.toggle("hidden", name === "settings");
  renderReceiverStatus();
  window.ROSMap?.setVisible(name === "map");
  if (name === "map") refresh();
  document.title = name === "settings" ? "系统设置 · ROS Watch" : "ROS Watch · 网络观察台";
  if (name === "settings") {
    for (const tab of settingsTabNames) {
      const selected = tab === settingsTab;
      const button = $("settings-tab-" + tab);
      button.setAttribute("aria-selected", String(selected));
      button.tabIndex = selected ? 0 : -1;
      $("settings-panel-" + tab).classList.toggle("hidden", !selected);
    }
    window.loadSettings?.();
  }
  if (name === "logs") refreshLogs(true);
  if (name === "events") {
    refreshEvents().catch((e) => fail(e.message));
  }
}
for (const button of document.querySelectorAll("[data-settings-tab]")) {
  button.addEventListener("click", () => showView("settings/" + button.dataset.settingsTab));
  button.addEventListener("keydown", event => {
    let index = settingsTabNames.indexOf(button.dataset.settingsTab);
    if (event.key === "ArrowRight") index = (index + 1) % settingsTabNames.length;
    else if (event.key === "ArrowLeft") index = (index + settingsTabNames.length - 1) % settingsTabNames.length;
    else if (event.key === "Home") index = 0;
    else if (event.key === "End") index = settingsTabNames.length - 1;
    else return;
    event.preventDefault();
    showView("settings/" + settingsTabNames[index]);
    $("settings-tab-" + settingsTabNames[index]).focus();
  });
}
function drill(field, value) {
  $(field === "src" ? "dport" : "src").value = "";
  $(field).value = String(value);
  showView("events", true);
  window.scrollTo({ top: 0, behavior: "smooth" });
}
function empty(container, text) {
  container.replaceChildren(node("div", text, "empty"));
}
function ipButton(ip) {
  const b = node("button", ip, "ip-button");
  b.type = "button";
  b.addEventListener("click", () => drill("src", ip));
  return b;
}
function svgNode(tag, attrs) {
  const e = document.createElementNS("http://www.w3.org/2000/svg", tag);
  for (const [k, v] of Object.entries(attrs)) e.setAttribute(k, String(v));
  return e;
}
function trend(rows) {
  const box = $("trend");
  box.replaceChildren();
  if (!rows.length) {
    empty(box, "暂无访问记录");
    return;
  }
  const width = 700,
    height = 200,
    left = 36,
    bottom = 175,
    top = 14,
    right = 690,
    max = Math.max(1, ...rows.map((r) => r.n));
  const svg = svgNode("svg", {
    viewBox: `0 0 ${width} ${height}`,
    preserveAspectRatio: "none",
  });
  const defs = svgNode("defs", {}),
    gradient = svgNode("linearGradient", {
      id: "trend-fill",
      x1: 0,
      x2: 0,
      y1: 0,
      y2: 1,
    });
  gradient.append(
    svgNode("stop", {
      offset: "0%",
      "stop-color": "#b6ee80",
      "stop-opacity": ".2",
    }),
    svgNode("stop", {
      offset: "100%",
      "stop-color": "#b6ee80",
      "stop-opacity": ".005",
    }),
  );
  defs.append(gradient);
  svg.append(defs);
  for (let i = 0; i < 4; i++) {
    const y = top + ((bottom - top) * i) / 3;
    svg.append(
      svgNode("line", {
        x1: left,
        x2: right,
        y1: y,
        y2: y,
        stroke: "#293440",
        "stroke-dasharray": "3 6",
      }),
    );
    const t = svgNode("text", {
      x: left - 8,
      y: y + 3,
      fill: "#6e8096",
      "font-size": 11,
      "text-anchor": "end",
    });
    t.textContent = format(Math.round(max * (1 - i / 3)));
    svg.append(t);
  }
  const points = rows.map((r, i) => [
    left + ((right - left) * i) / Math.max(1, rows.length - 1),
    bottom - ((bottom - top) * r.n) / max,
  ]);
  const line = points.map((p, i) => (i ? "L" : "M") + p.join(",")).join(" ");
  svg.append(
    svgNode("path", {
      d: line + ` L${right},${bottom} L${left},${bottom} Z`,
      fill: "url(#trend-fill)",
    }),
  );
  svg.append(
    svgNode("path", {
      d: line,
      fill: "none",
      stroke: "#b6ee80",
      "stroke-width": 2,
      "vector-effect": "non-scaling-stroke",
      "stroke-linejoin": "round",
    }),
  );
  for (let i = 0; i < rows.length; i++) {
    const circle = svgNode("circle", {
      cx: points[i][0],
      cy: points[i][1],
      r: 4,
      fill: "transparent",
    });
    const title = svgNode("title", {});
    title.textContent = `${date(rows[i].t)} · ${format(rows[i].n)} 条`;
    circle.append(title);
    svg.append(circle);
  }
  box.append(svg);
  const tooltip = node("div",undefined,"trend-tooltip hidden"); box.append(tooltip);
  const inspect = event => {
    const bounds=svg.getBoundingClientRect();
    const x=(event.clientX-bounds.left)/bounds.width*width;
    const index=Math.max(0,Math.min(rows.length-1,Math.round((x-left)/(right-left)*(rows.length-1))));
    tooltip.textContent=date(rows[index].t)+" · "+format(rows[index].n)+" 条";
    tooltip.style.left=Math.max(0,Math.min(bounds.width-240,event.clientX-bounds.left-100))+"px";
    tooltip.classList.remove("hidden");
  };
  svg.addEventListener("pointermove",inspect);svg.addEventListener("pointerdown",inspect);svg.addEventListener("pointerleave",()=>tooltip.classList.add("hidden"));
  const spansDays=rows[rows.length-1].t-rows[0].t>=86400;
  $("trend-axis").replaceChildren(
    ...[0, 0.25, 0.5, 0.75, 1].map((f) =>
      node("span", (spansDays ? new Date(rows[Math.round((rows.length-1)*f)].t*1000).toLocaleDateString("zh-CN",{month:"2-digit",day:"2-digit"})+" " : "") + clock(rows[Math.round((rows.length - 1) * f)].t)),
    ),
  );
}
function rankPorts(rows) {
  const box = $("port-rank");
  if (!rows.length) {
    empty(box, "暂无端口记录");
    return;
  }
  box.replaceChildren();
  const max = Math.max(...rows.map((r) => r.n));
  for (const r of rows) {
    const b = node("button", undefined, "rank-item");
    const track = node("div", undefined, "bar-track"),
      fill = node("div", undefined, "bar-fill");
    fill.style.width = (r.n / max) * 100 + "%";
    track.append(fill);
    const label=node("span", (r.dport ?? "无端口") + " / " + r.proto);
    const service=window.ROSHome.services[r.dport];
    if(service){const hint=node("small",service,"port-service");hint.title="常见端口用途，不代表实际服务识别";label.append(hint);}
    b.append(
      label,
      track,
      node("span", format(r.n)),
    );
    b.addEventListener("click", () => {
      $("src").value = "";
      if (r.dport !== null) $("dport").value = r.dport;
      $("proto").value = ["TCP", "UDP", "ICMP"].includes(r.proto)
        ? r.proto
        : "";
      showView("events", true);
    });
    box.append(b);
  }
}
const countryNames = typeof Intl.DisplayNames === "function"
  ? new Intl.DisplayNames(["zh-CN"], {type:"region"}) : null;
function locationNode(location) {
  let text = location?.label || "未知地区";
  if (location?.kind === "located") {
    let country = location.country;
    if (countryNames && /^[A-Z]{2}$/.test(location.country_code))
      country = countryNames.of(location.country_code) || country;
    country = ROSMapData.countryName(location.country_code, country);
    const parts = [country, location.region, location.city]
      .filter(value => !["TW", "HK", "MO"].includes(location.country_code) || !["台湾", "台湾省", "香港", "澳门", "香港特别行政区", "澳门特别行政区"].includes(value))
      .filter((value, index, all) => value && all.indexOf(value) === index);
    text = parts.join(" · ") || text;
    if (location.isp) text += " · " + location.isp;
  }
  const label = node("span", text, "ip-location");
  label.title = "IP 出口归属地，仅供参考，不代表实际物理位置";
  return label;
}
function rankSources(rows, locations = {}) {
  const box = $("source-rank");
  box.replaceChildren();
  if (!rows.length) {
    const tr = node("tr"),
      td = node("td", "当前范围暂无记录", "empty");
    td.colSpan = 4;
    tr.append(td);
    box.append(tr);
    return;
  }
  for (const r of rows) {
    const tr = node("tr"),
      td = node("td");
    td.append(ipButton(r.src), locationNode(locations[r.src]));
    tr.append(
      td,
      node("td", format(r.n)),
      node("td", format(r.ports)),
      node("td", clock(r.last)),
    );
    box.append(tr);
  }
}
function scans(rows, locations = {}, windowSeconds = 300) {
  const box = $("scan-list");
  const scroll=box.scrollTop;
  updateScanSelection(rows);
  if (!rows.length) {
    empty(box, "当前范围未识别到符合阈值的扫描行为");
    return;
  }
  box.replaceChildren();
  for (const r of rows) {
    const wrap = node("div", undefined, "scan-item"),
      info = node("div", undefined, "scan-info");
    info.append(
      ipButton(r.src),
      locationNode(locations[r.src]),
      node("p", `${Math.round(windowSeconds/60)} 分钟窗口涉及 ${r.ports} 个端口`),
    );
    const actions = node("div", undefined, "scan-actions");
    const stamp=node("strong",window.ROSHome.relative(r.last),"scan-time");stamp.title=date(r.last);
    actions.append(stamp,node("span", format(r.n) + " 条"), scanSourceBlockButton(r.src));
    wrap.append(
      scanSourceCheckbox(r.src),
      info,
      actions,
    );
    box.append(wrap);
  }
  box.scrollTop=scroll;
}
function dl(id, items) {
  const box = $(id);
  box.replaceChildren(
    ...items.map(([k, v]) => {
      const d = node("div");
      d.append(node("dt", k), node("dd", v));
      return d;
    }),
  );
}
function storageMessage(s) {
  if (!s) return "正在读取空间状态…";
  if (s.space_state === "low_space") return s.disk_low_action === "prune" ? "磁盘空间不足，自动清理仍未恢复到最低可用空间，已暂停写入。请释放其他磁盘空间；暂停期间的新日志可能丢失。" : "磁盘空间不足，已暂停写入。现有日志仍按保留天数管理；腾出空间后自动恢复，暂停期间的新日志可能丢失。";
  if (s.space_state === "unknown") return "无法读取磁盘可用空间，已暂停写入，请检查存储状态。";
  return "空间正常。平时按保留天数清理过期日志。";
}
function byteSize(n) {
  if (n === null || n === undefined) return "未知";
  return n >= 1073741824 ? (n / 1073741824).toFixed(2) + " GB" : (n / 1048576).toFixed(1) + " MB";
}
function renderStorage(s) {
  if (!s) return;
  dl("settings-storage", [
    ["数据库已占用", byteSize(s.database_bytes)],
    ["磁盘可用空间", byteSize(s.disk_free_bytes)],
    ["空间状态", {normal:"正常",low_space:"不足，暂停写入",unknown:"无法读取，暂停写入"}[s.space_state]],
    ["当前保留记录", format(s.records)],
    ["实际最早记录", date(s.oldest)],
    ["最新记录", date(s.newest)],
    ["保留期限", s.retention_days + " 天"],
    ["空间不足时", s.disk_low_action === "prune" ? "自动清理最旧日志" : "暂停写入"],
    ["空间不足累计清理", format(s.space_pruned) + " 条"],
    ["最近空间清理", date(s.last_space_cleanup)],
  ]);
  $("settings-storage-summary").textContent = "已用 " + byteSize(s.database_bytes) + " · 可用 " + byteSize(s.disk_free_bytes);
  $("settings-storage-notice").textContent = storageMessage(s) + (s.space_pruned ? " 曾因空间不足提前清理 " + format(s.space_pruned) + " 条；实际可查范围以上述时间为准。" : "");
  $("settings-storage-notice").classList.toggle("error", s.space_state !== "normal");
}
function status(s, coverage) {
  receiverState = s;
  accessCoverage = coverage;
  renderReceiverStatus();
  const c = s.counters;
  $("scan-rule").textContent =
    `每 ${Math.round(s.scan_window / 60)} 分钟固定窗口 ≥ ${s.scan_ports} 个端口`;
  dl("receiver-status", [
    ["服务启动时间", date(s.started_at)],
    ["最后收到有效日志", date(s.last_received)],
    ["最后成功写入", date(s.last_written)],
    ["收到 UDP 报文", format(c.received)],
    ["已写入记录", format(c.written)],
    ["忽略的非目标日志", format(c.ignored)],
    ["拒绝的非白名单来源", format(c.untrusted)],
    ["处理限速丢弃", format(c.rate_dropped)],
    ["队列满丢弃", format(c.queue_dropped)],
    ["存储失败丢弃", format(c.storage_dropped)],
    ["超长报文丢弃", format(c.oversized)],
    ["当前排队", format(s.queue)],
  ]);
  renderStorage(s.storage);
  dl("storage-status", [
    ["当前保留记录（原文与访问合计）", format(s.storage.records)],
    ["实际最早记录", date(s.storage.oldest)],
    ["最新记录", date(s.storage.newest)],
    ["数据库已占用", byteSize(s.storage.database_bytes)],
    ["磁盘可用空间", byteSize(s.storage.disk_free_bytes)],
    ["空间状态", {normal:"正常",low_space:"不足，暂停写入",unknown:"无法读取"}[s.storage.space_state]],
    ["最长保留", s.retention_days + " 天"],
    ["本次运行已清理", format(s.pruned) + " 条"],
    ["时间依据", "服务器接收时间"],
    ["扫描识别", "固定时间窗口内的不同目标端口数"],
    ["统计口径", "日志观察次数，非成功连接数"],
    ["ROS 端记录方式", {limited:"按限速规则记录，统计为采样结果",full:"按配置记录，UDP 仍可能丢失",unknown:"尚未确认是否限速，仅统计已收到日志"}[s.sampling] || "尚未确认是否限速，仅统计已收到日志"],
  ]);
}
async function refreshEvents() {
  if(eventRequest) eventRequest.abort();
  const request=new AbortController();eventRequest=request;
  const p = params(true);
  if (cursor) p.set("before", cursor);
  let result;
  try{result=await api("/api/events?"+p,{signal:request.signal});}catch(error){if(error.name==="AbortError")return;throw error;}
  if(request!==eventRequest||view!=="events")return;
  const expanded=new Set([...document.querySelectorAll("#events-list .event-row[aria-expanded=true]")].map(row=>row.dataset.eventId));
  const focused=document.activeElement.closest?.(".event-row")?.dataset.eventId;
  next = result.next;
  $("next-page").disabled = !next;
  $("first-page").disabled = !cursor;
  $("page-info").textContent =
    (cursor ? "历史记录" : "最新记录") + ` · ${result.rows.length} 条`;
  $("export").href = "/api/export?" + params(true);
  const box = $("events-list");
  box.replaceChildren();
  if (!result.rows.length) {
    const tr = node("tr"),
      td = node("td", "暂无匹配记录，尝试调整筛选条件。", "empty");
    td.colSpan = 8;
    tr.append(td);
    box.append(tr);
    return;
  }
  for (const r of result.rows) {
    const tr = node("tr", undefined, "event-row");
    tr.tabIndex = 0;
    tr.dataset.eventId=String(r.id);
    tr.setAttribute("aria-expanded", String(expanded.has(String(r.id))));
    tr.setAttribute("aria-label", date(r.ts) + " " + r.src + "，展开原始日志");
    const source = node("td");
    source.append(
      node("span", r.src, "ip-button"),
      node("span", r.sport !== null ? " : " + r.sport : "", "muted"),
      locationNode(r.src_location),
    );
    const scope = r.scope === "router" ? "路由器自身" : r.direction === "inbound" ? "内网服务" : "转发通信";
    const badge = node("td");
    badge.append(
      node(
        "span",
        r.outcome === "blocked" ? "拦截前记录" : "访问尝试",
        r.outcome === "blocked" ? "tag" : "tag neutral",
      ),
    );
    const traffic=node("td");traffic.append(node("span",window.ROSHome.directions[r.direction] || "方向未确认","tag neutral"));
    if(r.business)traffic.append(node("span",r.business,"business-tag"));
    tr.append(
      node("td", date(r.ts)),
      source,
      node("td", r.dst + (r.dport !== null ? " : " + r.dport : "")),
      node("td", r.proto),
      node("td", r.wan),
      node("td", scope),
      traffic,
      badge,
    );
    const raw = node("tr", undefined, "raw-row hidden"),
      rawCell = node("td");
    rawCell.append(window.ROSHome.eventActions(r),node("pre",r.raw));
    rawCell.colSpan = 8;
    if(expanded.has(String(r.id)))raw.classList.remove("hidden");
    raw.append(rawCell);
    const toggle = () => {
      raw.classList.toggle("hidden");
      tr.setAttribute(
        "aria-expanded",
        String(!raw.classList.contains("hidden")),
      );
    };
    tr.addEventListener("click", toggle);
    tr.addEventListener("keydown", (e) => {
      if (e.key === "Enter" || e.key === " ") {
        e.preventDefault();
        toggle();
      }
    });
    box.append(tr, raw);
    if(focused===String(r.id))tr.focus({preventScroll:true});
  }
}
async function refresh() {
  if (loading) {
    pending = true;
    return;
  }
  loading = true;
  document.body.classList.add("loading");
  try {
    if (view === "logs") { if (!logCursor) await refreshLogs(); return; }
    const r = await api("/api/summary?" + params());
    if (view === "logs") return;
    $("error-banner").classList.add("hidden");
    $("metric-total").textContent = format(r.totals.observations);
    $("metric-sources").textContent = format(r.totals.sources);
    $("metric-ports").textContent = format(r.totals.ports);
    $("metric-scans").textContent = format(r.scan_count);
    trend(r.trend);
    scans(r.scans, r.locations,r.status.scan_window);
    status(r.status, r.coverage);
    window.ROSHome.render(r);
    summaryUpdatedAt = r.generated_at;
    updateFilterSummary();
    for (const w of r.wans)
      if (![...$("wan").options].some((o) => o.value === w.wan))
        $("wan").add(new Option(w.wan, w.wan));
    if (view === "events" && !cursor) await refreshEvents();
    if (view === "map") await window.ROSMap.refresh();
  } catch (e) {
    fail(e.message);
  } finally {
    loading = false;
    document.body.classList.remove("loading");
    if (pending) {
      pending = false;
      refresh();
    }
  }
}
document
  .querySelectorAll("[data-view]")
  .forEach((b) => b.addEventListener("click", () => showView(b.dataset.view,true)));
for (const id of filterNames)
  $(id).addEventListener("change", () => {
    cursor = null;
    writeLocation(true);
    refresh();
    if (view === "events") refreshEvents().catch((e) => fail(e.message));
  });
$("refresh").addEventListener("click", () => {
  if (view === "logs") { refreshLogs(true); return; }
  if (view === "events") cursor = null;
  writeLocation(false);
  refresh();
});
$("reset").addEventListener("click", () => {
  for (const k of filterNames) $(k).value = k === "hours" ? "12" : k === "direction" ? "inbound" : "";
  $("src").value = "";
  $("dport").value = "";
  cursor = null;
  writeLocation(true);
  refresh();
});
$("search-form").addEventListener("submit", (e) => {
  e.preventDefault();
  cursor = null;
  writeLocation(true);
  refreshEvents().catch((e) => fail(e.message));
});
$("clear-search").addEventListener("click", () => {
  $("src").value = "";
  $("dport").value = "";
  cursor = null;
  writeLocation(true);
  refreshEvents().catch((e) => fail(e.message));
});
$("next-page").addEventListener("click", () => {
  cursor = next;
  writeLocation(true);
  refreshEvents().catch((e) => fail(e.message));
});
$("first-page").addEventListener("click", () => {
  cursor = null;
  writeLocation(true);
  refreshEvents().catch((e) => fail(e.message));
});
window.ROSHome.init();
restoreLocation();
$("auto-refresh").addEventListener("click",()=>{
  autoRefresh=!autoRefresh;
  $("auto-refresh").textContent=autoRefresh ? "暂停更新" : "恢复更新";
  $("auto-refresh").setAttribute("aria-pressed",String(!autoRefresh));
  $("auto-refresh").title=autoRefresh ? "暂停自动更新，方便阅读记录" : "自动更新已暂停，点击恢复";
  if(autoRefresh)refresh();
});
window.addEventListener("DOMContentLoaded", () => {
  const initialView = window.location.hash.slice(1);
  if (initialView) showView(initialView,false,true);
});
window.addEventListener("hashchange", () => {restoreLocation();showView(window.location.hash.slice(1),false,true);});
window.addEventListener("popstate",()=>{restoreLocation();showView(location.hash.slice(1),false,true);refresh();});
refresh();
setInterval(() => {
  if (!document.hidden && autoRefresh) refresh();
}, 10000);
document.addEventListener("visibilitychange", () => {
  if (!document.hidden && autoRefresh) refresh();
});
