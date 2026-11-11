"use strict";
window.ROSHome = (() => {
  let data = null, analysis = "suspicious", attention = "scan", selectedBusiness = null;
  const services = {22:"SSH",23:"Telnet",53:"DNS",80:"HTTP",443:"HTTPS",1883:"MQTT",8883:"MQTT TLS",3389:"RDP",8291:"WinBox",8728:"RouterOS API",8729:"RouterOS API TLS"};
  const directions = {inbound:"外部新连接",response:"已建立 / 响应",unknown:"方向未确认"};
  function relative(ts) {
    if (!ts) return "尚无记录";
    const seconds = Math.max(0, Math.floor(Date.now()/1000-ts));
    if (seconds < 60) return seconds + " 秒前";
    if (seconds < 3600) return Math.floor(seconds/60) + " 分钟前";
    if (seconds < 86400) return Math.floor(seconds/3600) + " 小时前";
    return new Date(ts*1000).toLocaleDateString("zh-CN", {month:"2-digit",day:"2-digit"});
  }
  function choose(buttons, key, value) {
    for (const button of document.querySelectorAll(buttons)) button.setAttribute("aria-pressed", String(button.dataset[key] === value));
  }
  function filters() {
    const box = $("filter-chips"); box.replaceChildren();
    let count = 0;
    for (const key of ["direction","scope","proto","outcome"]) {
      const control = $(key), defaultValue = key === "direction" ? "inbound" : "";
      if (control.value === defaultValue) continue;
      count++;
      const chip = node("button", control.selectedOptions[0].textContent + " ×", "filter-chip");
      chip.type = "button";
      chip.addEventListener("click", () => {control.value = defaultValue; control.dispatchEvent(new Event("change"));});
      box.append(chip);
    }
    $("filter-count").textContent = count ? "(" + count + ")" : "";
    box.classList.toggle("hidden", !count);
  }
  function showLogs(item, system = false) {
    for (const key of ["category","severity","event","device","username","source","q"]) $("logs-"+key).value = "";
    $("logs-hours").value = $("hours").value;
    if (system) {
      $("logs-category").value = item.category;
      if (item.device) $("logs-device").value = item.device;
    } else {
      $("logs-category").value = "login"; $("logs-event").value = "failure";
      $("logs-source").value = item.src; $("logs-username").value = item.username;
    }
    showView("logs", true);
  }
  function renderFocus() {
    if (!data) return;
    choose("[data-attention]", "attention", attention);
    $("scan-controls").classList.toggle("hidden", attention !== "scan");
    $("scan-list").classList.toggle("hidden", attention !== "scan");
    $("login-attention").classList.toggle("hidden", attention !== "login");
    $("system-attention").classList.toggle("hidden", attention !== "system");
    const failures = data.attention.filter(item => item.kind === "login_failure");
    for(const [tab,count] of [["login",failures.length],["system",data.system_events.length]]) {
      const button=document.querySelector('[data-attention="'+tab+'"]');
      let badge=button.querySelector(".tab-count");
      if(!badge){badge=node("span",undefined,"tab-count");button.append(badge);}
      badge.textContent=count ? String(count)+(count>=8 ? "+" : "") : "";
    }
    const count = attention === "scan" ? data.scan_count : attention === "login" ? failures.length : data.system_events.length;
    $("attention-title").textContent = attention === "scan" ? ($("scan-order").value === "latest" ? "最新可疑事件" : "疑似扫描来源") : attention === "login" ? "最新连续登录失败" : "近期系统事件";
    $("attention-caption").textContent = attention === "scan" ? ($("scan-order").value === "latest" ? "最新命中优先，点击来源查看记录" : "按所选方式排序，点击来源查看记录") : "与所选时间范围一致 · 系统事件不受 WAN、协议筛选影响";
    $("attention-count").textContent = count ? format(count) + (attention === "scan" ? " 个来源" : " 项") : "暂无命中";
    $("attention-count").className = "tag " + (count && attention !== "system" ? "amber" : "neutral");
    $("attention-panel").classList.toggle("no-alerts", !count);
    $("scan-limit-note").textContent = data.scan_count > 20 ? "显示其中 20 个来源" : "";
    for (const [id, items, isSystem] of [["login-attention",failures,false],["system-attention",data.system_events,true]]) {
      const list = $(id); list.replaceChildren();
      if (!items.length) {empty(list, isSystem ? "当前时间范围暂无相关系统事件" : "当前时间范围暂无连续登录失败"); continue;}
      for (const item of items) {
        const button = node("button", undefined, "attention-event"); button.type = "button";
        const title = node("strong", isSystem ? ({config:"配置变更",network:"网络状态",system:"系统事件"}[item.category] || "系统事件") : item.src);
        const stamp = node("span",relative(item.last),"event-time"); stamp.title = date(item.last);
        const detail = node("p", isSystem ? item.message : "账号 " + item.username + " · 同一 5 分钟窗口内 " + format(item.n) + " 次失败");
        button.append(title,stamp,detail); button.addEventListener("click",()=>showLogs(item,isSystem)); list.append(button);
      }
    }
  }
  function renderAnalysis() {
    if (!data) return;
    choose("[data-analysis]", "analysis", analysis);
    let sources, ports;
    if (analysis === "suspicious") {
      const grouped = new Map();
      for (const item of data.latest_scans) {
        const row = grouped.get(item.src);
        if (row) {row.n += item.n; row.ports = Math.max(row.ports,item.ports); row.last = Math.max(row.last,item.last);}
        else grouped.set(item.src,{...item});
      }
      sources = [...grouped.values()]; ports = data.suspicious_ports;
    } else {sources = analysis === "all" ? data.sources : data.business_sources; ports = analysis === "all" ? data.ports : data.business_ports;}
    $("sources-title").textContent = {suspicious:"可疑来源",all:"全部访问排行",business:"正常业务来源"}[analysis];
    $("source-limit").textContent = analysis === "suspicious" ? "最近 20 个" : "TOP 10";
    $("analysis-note").textContent = {suspicious:"仅展示命中扫描条件的近期来源",all:"按访问记录数量排序，包含正常业务",business:"精确匹配已确认业务，保留全部记录"}[analysis];
    $("ports-caption").textContent = analysis === "suspicious" ? "近期可疑来源的未标记服务" : analysis === "business" ? "已标记的正常业务服务" : "当前筛选范围 · 点击查看记录";
    rankSources(sources, data.locations); rankPorts(ports);
  }
  function render(r) {
    data = r;
    renderFocus(); renderAnalysis(); filters();
    document.querySelector(".scan-metric").classList.toggle("no-scans", !r.scan_count);
    $("metric-record-caption").textContent = $("direction").value === "inbound" ? "外部主动发起 · 日志条数" : $("direction").selectedOptions[0].textContent + " · 日志条数";
    const box = $("traffic-types"); box.replaceChildren();
    const counts = Object.fromEntries(r.traffic.map(item=>[item.direction,item.n]));
    for (const key of ["inbound","response","unknown"]) {
      const button = node("button",directions[key] + " " + format(counts[key]),"traffic-type"); button.type = "button";
      button.classList.toggle("active", $("direction").value === key);
      button.addEventListener("click",()=>{$("direction").value = key; $("direction").dispatchEvent(new Event("change"));}); box.append(button);
    }
    if (r.totals.business) box.append(node("span","已标记业务 " + format(r.totals.business) + " 条","business-count"));
    $("trend-bucket").textContent = "条 / " + Math.round(r.bucket_seconds/60) + " 分钟";
    $("trend-caption").textContent = $("direction").selectedOptions[0].textContent + " · 点击或触摸图表查看数值";
    $("trend-coverage").textContent = r.coverage.oldest ? "已有记录：" + date(r.coverage.oldest) + " 至 " + date(r.coverage.newest) : "尚无历史记录";
    if (!r.status.demo && r.status.last_received) $("connection-text").textContent += " · " + relative(r.status.last_received);
  }
  function postRule(rule) {
    return api("/api/business", {method:"POST",headers:{"Content-Type":"application/json","X-CSRF-Token":document.querySelector('meta[name="csrf-token"]').content},body:JSON.stringify(rule)});
  }
  async function refreshBusinessView() {
    await refresh();
    if(view === "events" && cursor) await refreshEvents();
  }
  function renderRules(rules) {
    const list = $("business-rules"); list.replaceChildren();
    if (!rules.length) {empty(list,"还没有正常业务标记。到访问记录展开具体连接，即可精确标记 MQTT 等业务。"); return;}
    for (const rule of rules) {
      const row = node("div",undefined,"business-rule"), info = node("div");
      info.append(node("strong",rule.name),node("p",rule.src + " → " + rule.dst + ":" + rule.dport + " / " + rule.proto));
      const remove = node("button","取消标记","text-button"); remove.type = "button";
      remove.addEventListener("click",async()=>{
        remove.disabled = true;
        try {const result=await postRule({id:rule.id,remove:true});renderRules(result.rules);$("business-message").textContent="已取消标记，访问历史保留。";await refreshBusinessView();}
        catch(error){$("business-message").textContent=error.message;remove.disabled=false;}
      });
      row.append(info,remove);list.append(row);
    }
  }
  async function openBusiness(event = null) {
    selectedBusiness = event;
    $("business-form").classList.toggle("hidden",!event);
    $("business-title").textContent = event ? "标记正常业务" : "正常业务标记";
    $("business-message").textContent = "";
    $("business-rules").classList.toggle("hidden",!!event);
    if (event) {
      $("business-connection").textContent = event.src + " → " + event.dst + ":" + event.dport + " / " + event.proto + " · " + event.wan;
      $("business-name").value = event.business || services[event.dport] || "正常业务";
    }
    const dialog = $("business-dialog"); if (!dialog.open) dialog.showModal();
    if (event) $("business-name").focus();
    else {try {renderRules((await api("/api/business")).rules);}catch(error){$("business-message").textContent=error.message;}}
  }
  function eventActions(event) {
    const toolbar = node("div",undefined,"event-actions");
    const copy = node("button","复制来源 IP","text-button"); copy.type="button";
    copy.addEventListener("click",async()=>{try{await navigator.clipboard.writeText(event.src);copy.textContent="已复制";}catch{copy.textContent="复制失败，请手动选择 IP";}});toolbar.append(copy);
    if (event.direction === "inbound" && ["TCP","UDP"].includes(event.proto) && event.dport > 0) {
      const mark=node("button",event.business ? "修改业务名称" : "标记正常业务","outline-button");mark.type="button";mark.addEventListener("click",()=>openBusiness(event));toolbar.append(mark);
    }
    toolbar.addEventListener("click",event=>event.stopPropagation());
    return toolbar;
  }
  function init() {
    for (const button of document.querySelectorAll("[data-attention]")) button.addEventListener("click",()=>{attention=button.dataset.attention;renderFocus();});
    for (const button of document.querySelectorAll("[data-analysis]")) button.addEventListener("click",()=>{analysis=button.dataset.analysis;renderAnalysis();});
    for (const button of document.querySelectorAll("[data-shortcut]")) button.addEventListener("click",()=>{
      const target=button.dataset.shortcut;
      if(target==="events"){showView("events",true);return;}
      if(target==="scans"){attention="scan";renderFocus();}
      $(target==="scans"?"attention-panel":target==="ports"?"ports-panel":"source-panel").scrollIntoView({behavior:"smooth",block:"start"});
    });
    $("business-manage").addEventListener("click",()=>openBusiness());
    $("business-close").addEventListener("click",()=>$("business-dialog").close());
    $("business-form").addEventListener("submit",async event=>{
      event.preventDefault();if(!selectedBusiness)return;
      const button=event.submitter;button.disabled=true;
      const e=selectedBusiness,rule={name:$("business-name").value,sender:e.sender,src:e.src,dst:e.dst,proto:e.proto,dport:e.dport,scope:e.scope,interface:e.interface};
      try {const result=await postRule(rule);$("business-form").classList.add("hidden");$("business-rules").classList.remove("hidden");renderRules(result.rules);$("business-message").textContent="已保存精确业务标记；同一来源的其他服务仍继续检测。";await refreshBusinessView();}
      catch(error){$("business-message").textContent=error.message;}
      finally{button.disabled=false;}
    });
    $("back-overview").addEventListener("click",()=>showView("overview",true));
    $("scan-order").addEventListener("change",()=>{writeLocation(true);refresh();});
  }
  return {init,render,filters,relative,eventActions,directions,services};
})();
