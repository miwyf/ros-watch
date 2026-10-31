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
const filterNames = ["hours", "wan", "scope", "proto", "outcome"];
function params(search = false) {
  const p = new URLSearchParams();
  for (const k of filterNames) if ($(k).value) p.set(k, $(k).value);
  if (search)
    for (const k of ["src", "dport"])
      if ($(k).value.trim()) p.set(k, $(k).value.trim());
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
function showView(name) {
  view = name;
  window.scrollTo({ top: 0, behavior: "instant" });
  const titles = {
    overview: "外部访问，一目了然",
    events: "访问记录",
    system: "接收状态",
    settings: "系统设置",
  };
  $("page-title").replaceChildren(
    document.createTextNode(titles[name]),
    node("span", ".", "accent"),
  );
  document.querySelector(".subtitle").textContent =
    name === "settings"
      ? "参数保存后生效，重建容器后依然保留。"
      : "从连接尝试中发现变化，掌握你的网络边界。";
  for (const v of ["overview", "events", "system", "settings"])
    $(v + "-view").classList.toggle("hidden", name !== v);
  document
    .querySelectorAll("[data-view]")
    .forEach((b) => b.classList.toggle("active", b.dataset.view === name));
  $("view-title").textContent = {
    overview: "访问概览",
    events: "访问记录",
    system: "接收状态",
    settings: "系统设置",
  }[name];
  document
    .querySelector(".filterbar")
    .classList.toggle("hidden", name === "settings");
  $("refresh").classList.toggle("hidden", name === "settings");
  if (name === "settings") window.loadSettings?.();
  if (name === "events") {
    cursor = null;
    refreshEvents().catch((e) => fail(e.message));
  }
}
function drill(field, value) {
  $(field === "src" ? "dport" : "src").value = "";
  $(field).value = String(value);
  showView("events");
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
      "font-size": 9,
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
  $("trend-axis").replaceChildren(
    ...[0, 0.25, 0.5, 0.75, 1].map((f) =>
      node("span", clock(rows[Math.round((rows.length - 1) * f)].t)),
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
    b.append(
      node("span", (r.dport ?? "无端口") + " / " + r.proto),
      track,
      node("span", format(r.n)),
    );
    b.addEventListener("click", () => {
      $("src").value = "";
      if (r.dport !== null) $("dport").value = r.dport;
      $("proto").value = ["TCP", "UDP", "ICMP"].includes(r.proto)
        ? r.proto
        : "";
      showView("events");
    });
    box.append(b);
  }
}
function rankSources(rows) {
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
    td.append(ipButton(r.src));
    tr.append(
      td,
      node("td", format(r.n)),
      node("td", format(r.ports)),
      node("td", clock(r.last)),
    );
    box.append(tr);
  }
}
function scans(rows) {
  const box = $("scan-list");
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
      node("p", `峰值窗口涉及 ${r.ports} 个端口 · 最后 ${clock(r.last)}`),
    );
    wrap.append(
      node("span", "↗", "scan-icon"),
      info,
      node("span", format(r.n) + " 条"),
    );
    box.append(wrap);
  }
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
function status(s, coverage) {
  $("demo-banner").classList.toggle("hidden", !s.demo);
  const c = s.counters;
  const losses = [
    "rate_dropped",
    "queue_dropped",
    "storage_dropped",
    "oversized",
  ].reduce((n, k) => n + (c[k] || 0), 0);
  let text =
    s.sampling === "limited"
      ? "ROS 端按限速规则记录，当前统计为采样结果。"
      : s.sampling === "full"
        ? "按配置接收完整记录；UDP 传输仍可能丢失。"
        : "尚未确认 ROS 端是否限速，当前仅统计已收到的日志。";
  if (!s.sources_configured)
    text += " 尚未设置日志来源白名单，请在系统设置中填写后开始接收。";
  if (!coverage.stored)
    text +=
      " 尚未收到有效 WAN 日志，请检查发送目标、发送来源白名单及 WAN 接口映射。";
  if (coverage.oldest)
    text += " 可查记录起始于 " + date(coverage.oldest) + "。";
  if (losses) text += ` 本次运行有 ${format(losses)} 条报文因保护限制未入库。`;
  if (s.error)
    text +=
      " 接收服务异常：" +
      ({
        disk_low: "磁盘空间不足，暂停写入",
        storage_failed: "数据库写入失败",
        maintenance_failed: "数据清理异常",
        receiver_failed: "接收器已停止",
      }[s.error] || s.error);
  $("notice").textContent = text;
  $("notice").classList.toggle("danger", !!s.error || losses > 0);
  $("connection").classList.toggle("stale", !!s.error);
  $("connection-text").textContent = s.demo
    ? "演示数据"
    : s.error
      ? "接收异常"
      : s.last_received && Date.now() / 1000 - s.last_received < 120
        ? "正在接收"
        : "等待日志";
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
  dl("storage-status", [
    ["当前保留记录", format(coverage.stored)],
    ["最早保留时间", date(coverage.oldest)],
    ["最新保留时间", date(coverage.newest)],
    ["数据库及日志文件", (s.db_bytes / 1048576).toFixed(1) + " MB"],
    ["数据库容量设置", s.max_db_mb + " MB"],
    ["最长保留", s.retention_days + " 天"],
    ["本次运行已清理", format(s.pruned) + " 条"],
    ["时间依据", "服务器接收时间"],
    ["扫描识别", "固定时间窗口内的不同目标端口数"],
    ["统计口径", "日志观察次数，非成功连接数"],
  ]);
}
async function refreshEvents() {
  const p = params(true);
  if (cursor) p.set("before", cursor);
  const result = await api("/api/events?" + p);
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
    td.colSpan = 7;
    tr.append(td);
    box.append(tr);
    return;
  }
  for (const r of result.rows) {
    const tr = node("tr", undefined, "event-row");
    tr.tabIndex = 0;
    tr.setAttribute("aria-expanded", "false");
    tr.setAttribute("aria-label", date(r.ts) + " " + r.src + "，展开原始日志");
    const source = node("td");
    source.append(
      node("span", r.src, "ip-button"),
      node("span", r.sport !== null ? " : " + r.sport : "", "muted"),
    );
    const scope = r.scope === "router" ? "路由器自身" : "内网映射";
    const badge = node("td");
    badge.append(
      node(
        "span",
        r.outcome === "blocked" ? "拦截前记录" : "访问尝试",
        r.outcome === "blocked" ? "tag" : "tag neutral",
      ),
    );
    tr.append(
      node("td", date(r.ts)),
      source,
      node("td", r.dst + (r.dport !== null ? " : " + r.dport : "")),
      node("td", r.proto),
      node("td", r.wan),
      node("td", scope),
      badge,
    );
    const raw = node("tr", undefined, "raw-row hidden"),
      rawCell = node("td", r.raw);
    rawCell.colSpan = 7;
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
    const r = await api("/api/summary?" + params());
    $("error-banner").classList.add("hidden");
    $("metric-total").textContent = format(r.totals.observations);
    $("metric-sources").textContent = format(r.totals.sources);
    $("metric-ports").textContent = format(r.totals.ports);
    $("metric-scans").textContent =
      r.scans.length >= 20 ? "20+" : format(r.scans.length);
    trend(r.trend);
    rankPorts(r.ports);
    rankSources(r.sources);
    scans(r.scans);
    status(r.status, r.coverage);
    for (const w of r.wans)
      if (![...$("wan").options].some((o) => o.value === w.wan))
        $("wan").add(new Option(w.wan, w.wan));
    $("updated-at").textContent = "更新于 " + clock(r.generated_at);
    if (view === "events" && !cursor) await refreshEvents();
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
  .forEach((b) => b.addEventListener("click", () => showView(b.dataset.view)));
for (const id of filterNames)
  $(id).addEventListener("change", () => {
    cursor = null;
    refresh();
    if (view === "events") refreshEvents().catch((e) => fail(e.message));
  });
$("refresh").addEventListener("click", () => {
  if (view === "events") cursor = null;
  refresh();
});
$("reset").addEventListener("click", () => {
  for (const k of filterNames) $(k).value = k === "hours" ? "24" : "";
  $("src").value = "";
  $("dport").value = "";
  cursor = null;
  refresh();
});
$("search-form").addEventListener("submit", (e) => {
  e.preventDefault();
  cursor = null;
  refreshEvents().catch((e) => fail(e.message));
});
$("clear-search").addEventListener("click", () => {
  $("src").value = "";
  $("dport").value = "";
  cursor = null;
  refreshEvents().catch((e) => fail(e.message));
});
$("next-page").addEventListener("click", () => {
  cursor = next;
  refreshEvents().catch((e) => fail(e.message));
});
$("first-page").addEventListener("click", () => {
  cursor = null;
  refreshEvents().catch((e) => fail(e.message));
});
refresh();
setInterval(() => {
  if (!document.hidden) refresh();
}, 10000);
document.addEventListener("visibilitychange", () => {
  if (!document.hidden) refresh();
});
