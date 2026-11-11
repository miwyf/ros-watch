"use strict";
window.ROSMap = (() => {
  const canvas = $("access-map"), context = canvas.getContext("2d");
  const background = document.createElement("canvas"), base = background.getContext("2d");
  const reducedMotion = matchMedia("(prefers-reduced-motion: reduce)");
  let visible = false, mode = "flat", paused = reducedMotion.matches, rotation = [-10, 0, 0];
  let width = 0, height = 0, ratio = 1, zoom = 1, pan = [0, 0], dirty = true;
  let projection, world, places, cities, resources, target, settingsLoaded = false;
  let data = { points: [], mapped: 0, excluded: 0 }, routes = [], frame = 0, lastFrame = 0;
  let pointer = null, hovered = null, hits = [], sequence = 0;

  function script(src) {
    return new Promise((resolve, reject) => {
      const element = document.createElement("script"); element.src = src;
      element.onload = resolve;
      element.onerror = () => { element.remove(); reject(Error("地图组件加载失败，请刷新重试")); };
      document.head.append(element);
    });
  }
  async function load() {
    if (!resources) resources = (async () => {
      await script("/static/d3-array-3.2.4.min.js");
      await script("/static/d3-geo-3.1.1.min.js");
      [world, places] = await Promise.all([api("/static/map-world.json"), api("/static/map-places.json")]);
      cities = ROSMapData.cityIndex(places);
      const countries = Object.keys(places.countries).filter(code => ROSMapData.parentCode(code) === code).map(code => ({ code, label: ROSMapData.countryName(code, countryNames?.of(code) || places.countries[code].name) }));
      countries.sort((a, b) => a.code === "CN" ? -1 : b.code === "CN" ? 1 : a.label.localeCompare(b.label, "zh-CN"));
      $("map-location-country").replaceChildren(new Option("请选择国家/地区", ""), ...countries.map(item => new Option(item.label + "（" + item.code + "）", item.code)));
    })().catch(error => { resources = null; throw error; });
    return resources;
  }
  function message(text) {
    $("map-message").textContent = text;
    $("map-message").classList.toggle("hidden", !text);
  }
  function applyTarget(value) {
    const firstTarget = !target;
    target = value;
    if (firstTarget && configured()) rotation = [-target.longitude, mode === "globe" ? -target.latitude : 0, 0];
    $("map-target-name").textContent = target.name;
    $("map-location-name").value = target.name;
    $("map-longitude").value = target.longitude ?? "";
    $("map-latitude").value = target.latitude ?? "";
    $("map-location-summary").textContent = configured() ? target.name + " · " + target.longitude + ", " + target.latitude : "尚未设置";
    restorePlaceSelection();
    buildRoutes();
    dirty = true;
    wake();
  }
  function configured() { return target?.longitude != null && target?.latitude != null; }
  function renderCities(selected = "") {
    const options = ROSMapData.cityOptions(places, "CN", $("map-city-search").value);
    const labels = new Map();
    for (const option of options) labels.set(option.label, (labels.get(option.label) || 0) + 1);
    $("map-location-city").replaceChildren(new Option(options.length ? "请选择城市" : "没有匹配城市，可手动设置", ""), ...options.map(item => new Option(item.label + (labels.get(item.label) > 1 ? "（" + item.point.join(", ") + "）" : ""), item.id)));
    $("map-location-city").value = selected;
  }
  function showCityPicker() {
    const china = $("map-location-country").value === "CN";
    $("map-city-search-field").classList.toggle("hidden", !china);
    $("map-city-field").classList.toggle("hidden", !china);
  }
  function restorePlaceSelection() {
    if (!places) return;
    const samePoint = point => configured() && Math.abs(point[0] - target.longitude) < 0.000001 && Math.abs(point[1] - target.latitude) < 0.000001;
    const city = places.cities.findIndex(([code, point]) => ROSMapData.parentCode(code) === "CN" && samePoint(point));
    const reference = Object.keys(places.countries).find(code => samePoint(places.countries[code].point));
    const country = city >= 0 ? "CN" : ROSMapData.parentCode(reference);
    $("map-location-country").value = country || (configured() ? "" : "CN");
    $("map-city-search").value = "";
    renderCities(city >= 0 ? String(city) : ""); showCityPicker();
    $("map-manual-location").open = configured() && !country;
    updateDraftCoordinates();
  }
  function updateDraftCoordinates() {
    const longitude = $("map-longitude").value, latitude = $("map-latitude").value;
    $("map-draft-coordinates").textContent = longitude !== "" && latitude !== "" ? longitude + ", " + latitude : "";
  }
  function choosePlace(name, point) {
    $("map-location-name").value = name;
    $("map-longitude").value = point[0]; $("map-latitude").value = point[1];
    $("map-manual-location").open = false;
    $("map-location-status").textContent = "已选择 " + name + "，点击保存位置生效";
    updateDraftCoordinates();
  }
  function buildRoutes() {
    routes = configured() && world ? data.points.map(point => {
      const interpolate = d3.geoInterpolate(point.point, [target.longitude, target.latitude]);
      return { point, interpolate, coordinates: Array.from({ length: 49 }, (_, j) => interpolate(j / 48)) };
    }) : [];
  }
  function updateMessage() {
    message(!configured() ? "请在下方设置 RouterOS 所在地，显示来源到本地的动态连线。" : !data.points.length ? "当前筛选范围没有可定位的来源。" : "");
  }
  function renderDetails(truncated = false, limit = 500) {
    $("map-point-count").textContent = format(data.points.length) + " 个来源地区";
    $("map-counts").textContent = "可定位记录 " + format(data.mapped) + " 条 · 无法定位 " + format(data.excluded) + " 条";
    $("map-coverage").textContent = "城市近似定位 " + format(data.citySources || 0) + " 个来源 IP，国家/地区参考点 " + format(data.countrySources || 0) + " 个。" + (truncated ? "仅展示记录数最多的前 " + limit + " 个来源 IP；其余来源未计入本图。" : "内网、特殊用途和未知地址不绘制。") + " 同一地区已合并。";
    const list = $("map-region-list"); list.replaceChildren();
    for (const point of data.points) {
      const button = node("button", undefined, "map-region"); button.type = "button";
      button.append(node("span", point.label), node("strong", format(point.n)));
      button.append(node("small", point.level + " · " + point.sources.length + " 个 IP"));
      button.title = point.label + "：" + point.n + " 条记录，含 " + point.blocked + " 条拦截前记录；点击定位";
      button.addEventListener("click", () => {
        if (mode === "globe") rotation = [-point.point[0], -point.point[1], 0];
        else { rotation = [-point.point[0], 0, 0]; pan = [0, 0]; }
        hovered = point; dirty = true; wake();
      });
      list.append(button);
    }
    if (!data.points.length) list.append(node("p", "暂无可定位来源", "empty"));
    updateMessage();
  }
  async function refresh() {
    const request = ++sequence;
    try {
      const [result, value] = await Promise.all([
        api("/api/map?" + params()),
        settingsLoaded ? Promise.resolve(null) : api("/api/map/settings"),
        load(),
      ]);
      if (request !== sequence || !visible) return;
      if (value) { settingsLoaded = true; applyTarget(value); }
      data = ROSMapData.aggregate(result.sources, result.locations, places, cities);
      hovered = null;
      buildRoutes(); renderDetails(result.truncated, result.source_limit);
      dirty = true; resize(); wake();
    } catch (error) {
      if (request === sequence && visible) message("地图更新失败：" + error.message);
      throw error;
    }
  }
  function resize() {
    if (!visible) return;
    const rect = canvas.getBoundingClientRect();
    const nextRatio = Math.min(devicePixelRatio || 1, 2);
    if (rect.width === width && rect.height === height && nextRatio === ratio) return;
    width = rect.width; height = rect.height; ratio = nextRatio;
    for (const surface of [canvas, background]) { surface.width = Math.round(width * ratio); surface.height = Math.round(height * ratio); }
    context.setTransform(ratio, 0, 0, ratio, 0, 0); base.setTransform(ratio, 0, 0, ratio, 0, 0);
    dirty = true; wake();
  }
  function drawBase() {
    const radius = Math.min(width, height) * 0.395 * zoom;
    projection = mode === "globe" ? d3.geoOrthographic().rotate(rotation).scale(radius) : d3.geoEquirectangular().rotate([rotation[0], 0]).scale(Math.max(width / (2 * Math.PI), height / Math.PI) * zoom);
    projection.translate([width / 2 + pan[0], height / 2 + pan[1]]).precision(0.5);
    const path = d3.geoPath(projection, base);
    base.clearRect(0, 0, width, height);
    const sky = base.createRadialGradient(width * 0.48, height * 0.45, 20, width / 2, height / 2, width * 0.7);
    sky.addColorStop(0, "#122c3d"); sky.addColorStop(1, "#080f19"); base.fillStyle = sky; base.fillRect(0, 0, width, height);
    if (mode === "globe") {
      base.beginPath(); path({ type: "Sphere" }); base.fillStyle = "#0c1e2b"; base.fill();
      base.strokeStyle = "#25516a"; base.lineWidth = 1; base.stroke();
    }
    base.beginPath(); path(d3.geoGraticule10()); base.strokeStyle = "#1a3646"; base.lineWidth = 0.6; base.stroke();
    base.beginPath(); path(world); base.fillStyle = "#1b3c49"; base.fill();
    base.beginPath(); path(world); base.strokeStyle = "#326072"; base.lineWidth = 0.7; base.stroke();
    base.beginPath(); path(world.symbols); base.fillStyle = "#7296a6"; base.strokeStyle = "#7296a6"; base.lineWidth = 0.8; base.fill(); base.stroke();
    if (mode === "globe") {
      const [cx, cy] = projection.translate();
      const shade = base.createRadialGradient(cx - radius * 0.4, cy - radius * 0.5, radius * 0.1, cx, cy, radius);
      shade.addColorStop(0, "#9ad8ed0d"); shade.addColorStop(0.75, "#00000000"); shade.addColorStop(1, "#000710a0");
      base.beginPath(); path({ type: "Sphere" }); base.fillStyle = shade; base.fill();
    }
    dirty = false;
  }
  function front(point) {
    if (mode === "flat") return true;
    return d3.geoDistance(point, [-rotation[0], -rotation[1]]) < Math.PI / 2 - 0.005;
  }
  function dot(point, color, radius, pulse = 0) {
    if (!front(point)) return;
    const p = projection(point);
    context.fillStyle = color; context.shadowColor = color; context.shadowBlur = 10;
    context.beginPath(); context.arc(p[0], p[1], radius, 0, Math.PI * 2); context.fill(); context.shadowBlur = 0;
    if (pulse) { context.strokeStyle = color; context.globalAlpha = 1 - pulse; context.beginPath(); context.arc(p[0], p[1], radius + pulse * 12, 0, Math.PI * 2); context.stroke(); context.globalAlpha = 1; }
    return p;
  }
  function draw(time) {
    if (!projection || dirty) drawBase();
    context.clearRect(0, 0, width, height); context.drawImage(background, 0, 0, width, height);
    const path = d3.geoPath(projection, context);
    hits = [];
    for (let i = 0; i < routes.length; i++) {
      const { point, interpolate, coordinates } = routes[i];
      const color = point.blocked ? "#f4bc74" : "#64cfde";
      context.beginPath(); path({ type: "LineString", coordinates });
      context.strokeStyle = color; context.globalAlpha = hovered && hovered !== point ? 0.12 : 0.38; context.lineWidth = Math.min(2.4, 0.8 + Math.log10(point.n + 1) * 0.35); context.stroke(); context.globalAlpha = 1;
      const phase = paused ? 0.65 : (time / 3300 + i * 0.173) % 1;
      const p = dot(point.point, color, Math.min(5.5, 2.1 + Math.log10(point.n + 1) * 0.65));
      if (p) hits.push({ point, x: p[0], y: p[1] });
      dot(interpolate(phase), color, 2.5);
    }
    // Origins remain visible before a destination has been configured.
    if (!configured()) for (const point of data.points) {
      const p = dot(point.point, "#64cfde", 3);
      if (p) hits.push({ point, x: p[0], y: p[1] });
    }
    if (configured()) {
      const p = dot([target.longitude, target.latitude], "#b6ee80", 5, paused ? 0 : (time / 1900) % 1);
      if (p) {
        context.font = '600 12px "Microsoft YaHei",sans-serif'; context.fillStyle = "#d5f6ba";
        const tw = context.measureText(target.name).width;
        context.fillText(target.name, Math.max(10, Math.min(width - tw - 10, p[0] + 12)), Math.max(20, p[1] - 12));
      }
    }
    if (hovered) {
      const hit = hits.find(h => h.point === hovered);
      if (hit) {
        const label = hovered.label + " · " + format(hovered.n) + " 条 · " + hovered.level;
        context.font = '12px "Microsoft YaHei",sans-serif';
        const box = Math.min(width - 24, context.measureText(label).width + 24);
        const x = Math.max(12, Math.min(width - box - 12, hit.x - box / 2));
        const y = Math.max(14, Math.min(height - 44, hit.y - 44));
        context.fillStyle = "#0a1525ee"; context.fillRect(x, y, box, 32);
        context.fillStyle = "#edf5fc"; context.fillText(label, x + 12, y + 21, box - 24);
      }
    }
  }
  function animate(time) {
    frame = 0;
    if (!visible || document.hidden || !world || !width) return;
    if (time - lastFrame >= 40 || dirty || paused) { draw(time); lastFrame = time; }
    if (!paused) frame = requestAnimationFrame(animate);
  }
  function wake() { if (visible && !document.hidden && world && !frame) frame = requestAnimationFrame(animate); }
  function setVisible(value) {
    visible = value;
    if (frame) cancelAnimationFrame(frame);
    frame = 0;
    if (!value) { sequence++; return; }
    resize(); wake();
  }
  for (const name of ["flat", "globe"]) $("map-" + name).addEventListener("click", () => {
    mode = name; zoom = 1; pan = [0, 0];
    rotation = configured() ? [-target.longitude, mode === "globe" ? -target.latitude : 0, 0] : mode === "globe" ? [-105, -20, 0] : [-10, 0, 0];
    $("map-flat").setAttribute("aria-pressed", String(mode === "flat")); $("map-globe").setAttribute("aria-pressed", String(mode === "globe"));
    $("map-hint").textContent = mode === "globe" ? "拖动或方向键旋转地球 · 滚轮或 + / − 缩放" : "滚轮或 + / − 缩放 · 拖动平移";
    hovered = null; dirty = true; wake();
  });
  function updateMotion() { $("map-motion").textContent = paused ? "播放动画" : "暂停动画"; $("map-motion").setAttribute("aria-pressed", String(paused)); }
  $("map-motion").addEventListener("click", () => { paused = !paused; updateMotion(); wake(); });
  reducedMotion.addEventListener("change", event => { if (event.matches) { paused = true; updateMotion(); wake(); } });
  updateMotion();
  $("map-home").addEventListener("click", () => {
    zoom = 1; pan = [0, 0]; rotation = configured() ? [-target.longitude, mode === "globe" ? -target.latitude : 0, 0] : [-10, 0, 0]; dirty = true; hovered = null; wake();
  });
  function scale(factor) { zoom = Math.max(0.7, Math.min(4, zoom * factor)); dirty = true; wake(); }
  canvas.addEventListener("wheel", event => { event.preventDefault(); scale(event.deltaY < 0 ? 1.1 : 1 / 1.1); }, { passive: false });
  canvas.addEventListener("keydown", event => {
    if (["+", "=", "-"].includes(event.key)) { event.preventDefault(); scale(event.key === "-" ? 1 / 1.1 : 1.1); return; }
    const delta = { ArrowLeft: [-8, 0], ArrowRight: [8, 0], ArrowUp: [0, -8], ArrowDown: [0, 8] }[event.key];
    if (!delta) return;
    event.preventDefault();
    if (mode === "globe") { rotation[0] += delta[0]; rotation[1] = Math.max(-85, Math.min(85, rotation[1] - delta[1])); }
    else { pan[0] += delta[0] * 4; pan[1] += delta[1] * 4; }
    dirty = true; wake();
  });
  canvas.addEventListener("pointerdown", event => { pointer = { x: event.clientX, y: event.clientY }; canvas.setPointerCapture(event.pointerId); });
  canvas.addEventListener("pointermove", event => {
    if (pointer) {
      const dx = event.clientX - pointer.x, dy = event.clientY - pointer.y;
      if (mode === "globe") { rotation[0] += dx * 0.3 / zoom; rotation[1] = Math.max(-85, Math.min(85, rotation[1] - dy * 0.3 / zoom)); }
      else { pan[0] += dx; pan[1] += dy; }
      pointer = { x: event.clientX, y: event.clientY }; hovered = null; dirty = true;
    } else {
      const rect = canvas.getBoundingClientRect(), x = event.clientX - rect.left, y = event.clientY - rect.top;
      hovered = hits.find(hit => Math.hypot(hit.x - x, hit.y - y) < 12)?.point || null;
    }
    wake();
  });
  for (const event of ["pointerup", "pointercancel", "lostpointercapture"]) canvas.addEventListener(event, () => { pointer = null; });
  canvas.addEventListener("pointerleave", () => { hovered = null; wake(); });
  async function save(value) {
    const result = await api("/api/map/settings", { method: "POST", headers: { "Content-Type": "application/json", "X-CSRF-Token": document.querySelector('meta[name="csrf-token"]').content }, body: JSON.stringify(value) });
    settingsLoaded = true; applyTarget(result); updateMessage();
    $("map-location-status").textContent = "位置已保存";
    $("map-home").click();
  }
  $("map-location-country").addEventListener("change", () => {
    const country = $("map-location-country").value;
    $("map-city-search").value = "";
    renderCities(); showCityPicker();
    if (country && country !== "CN") {
      choosePlace(countryNames?.of(country) || places.countries[country].name, places.countries[country].point);
    } else {
      $("map-location-name").value = "我的 RouterOS";
      $("map-longitude").value = ""; $("map-latitude").value = "";
      $("map-manual-location").open = false; updateDraftCoordinates();
      $("map-location-status").textContent = country === "CN" ? "请选择所在城市" : "请选择国家/地区";
    }
  });
  $("map-city-search").addEventListener("input", () => renderCities($("map-location-city").value));
  $("map-location-city").addEventListener("change", () => {
    const selected = $("map-location-city").value;
    if (!selected) return;
    const [country, point, names] = places.cities[Number(selected)];
    if (ROSMapData.parentCode(country) === "CN") choosePlace(ROSMapData.placeName(country, names), point);
  });
  for (const field of ["map-longitude", "map-latitude"]) $(field).addEventListener("input", updateDraftCoordinates);
  $("map-location-form").addEventListener("submit", async event => {
    event.preventDefault();
    if (!$("map-manual-location").open) {
      if (!$("map-location-country").value) { $("map-location-status").textContent = "请选择国家/地区"; $("map-location-country").focus(); return; }
      if ($("map-location-country").value === "CN" && !$("map-location-city").value) { $("map-location-status").textContent = "请选择所在城市"; $("map-location-city").focus(); return; }
    }
    if (!$("map-longitude").value.trim() || !$("map-latitude").value.trim()) { $("map-location-status").textContent = "请选择位置或填写完整经纬度"; return; }
    try { await save({ name: $("map-location-name").value, longitude: Number($("map-longitude").value), latitude: Number($("map-latitude").value) }); }
    catch (error) { $("map-location-status").textContent = error.message; }
  });
  $("map-location-clear").addEventListener("click", async () => {
    try { await save({ name: $("map-location-name").value || "我的 RouterOS", longitude: null, latitude: null }); }
    catch (error) { $("map-location-status").textContent = error.message; }
  });
  new ResizeObserver(resize).observe(canvas);
  document.addEventListener("visibilitychange", () => { if (document.hidden && frame) { cancelAnimationFrame(frame); frame = 0; } else wake(); });
  return { refresh, setVisible };
})();
