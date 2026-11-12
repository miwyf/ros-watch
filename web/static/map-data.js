"use strict";
const ROSMapData = (() => {
  const chinaRegions = { TW: "中国台湾", HK: "中国香港", MO: "中国澳门" };
  const parentCode = code => Object.hasOwn(chinaRegions, code) ? "CN" : code;
  const countryName = (code, fallback) => code === "CN" ? "中国" : chinaRegions[code] || fallback;
  const cityName = names => names.find(name => /[\u3400-\u9fff]/.test(name)) || names[0];
  const placeName = (code, names) => countryName(code, code) + " · " + cityName(names);
  const normalize = s => String(s || "").normalize("NFKD").replace(/[\u0300-\u036f]/g, "").toLowerCase().replace(/市$/, "").replace(/[\s'’.-]/g, "");
  const threats = {
    unknown: { key: "unknown", rank: -1, label: "等级未知", color: "#8b9aaf" },
    low: { key: "low", rank: 0, label: "一般", color: "#64cfde" },
    medium: { key: "medium", rank: 1, label: "需关注", color: "#f0d264" },
    high: { key: "high", rank: 2, label: "疑似扫描", color: "#f49b56" },
    critical: { key: "critical", rank: 3, label: "高强度扫描", color: "#f26d7d" },
  };
  function threat(ports, threshold) {
    if (!Number.isInteger(ports) || ports < 0 || !Number.isInteger(threshold) || threshold < 2) return threats.unknown;
    if (ports >= threshold * 3) return threats.critical;
    if (ports >= threshold) return threats.high;
    if (ports >= Math.max(2, Math.ceil(threshold / 2))) return threats.medium;
    return threats.low;
  }
  function cometTrail(phase) {
    const head = Math.max(0, Math.min(1, phase));
    return Array.from({ length: 8 }, (_, i) => [Math.max(0, head - (8 - i) * 0.018), Math.max(0, head - (7 - i) * 0.018), (i + 1) / 8])
      .filter(([start, end]) => end > start);
  }
  function threatBursts(previous, next) {
    const old = new Map(previous.map(point => [point.key, point]));
    return next.filter(point => {
      const before = old.get(point.key);
      return point.threat.rank >= 2 && (!before || point.n > before.n || point.threat.rank > before.threat.rank);
    }).map(point => point.key);
  }
  function cityIndex(places) {
    const index = new Map();
    for (const [country, point, names] of places.cities) {
      for (const name of names) {
        for (const code of new Set([country, parentCode(country)])) {
          const key = code + ":" + normalize(name);
          if (!index.has(key)) index.set(key, point);
          else {
            const old = index.get(key);
            // Ambiguous names must fall back to a country reference point.
            if (old && Math.hypot(old[0] - point[0], old[1] - point[1]) > 0.5) index.set(key, null);
          }
        }
      }
    }
    return index;
  }
  function aggregate(rows, locations, places, index, scanPorts) {
    const groups = new Map();
    let excluded = 0, mapped = 0, citySources = 0, countrySources = 0;
    for (const row of rows) {
      const location = locations[row.src];
      const country = location?.kind === "located" && places.countries[location.country_code];
      if (!country) { excluded += row.n; continue; }
      const city = location.city && index.get(location.country_code + ":" + normalize(location.city));
      const point = city || country.point;
      const displayCountry = countryName(location.country_code, country.name);
      const label = city ? [displayCountry, location.city].filter(Boolean).join(" · ") : displayCountry;
      const key = location.country_code + ":" + point.join(",");
      if (!groups.has(key)) groups.set(key, { key, point, label, level: city ? "城市近似位置" : "国家/地区参考点", n: 0, blocked: 0, sources: [], peakPorts: null, threat: threats.unknown });
      const group = groups.get(key);
      group.n += row.n; group.blocked += row.blocked; group.sources.push(row.src);
      const risk = threat(row.peak_ports, scanPorts);
      if (risk.rank > group.threat.rank) group.threat = risk;
      if (risk !== threats.unknown) group.peakPorts = Math.max(group.peakPorts ?? 0, row.peak_ports);
      mapped += row.n;
      if (city) citySources++; else countrySources++;
    }
    return { points: [...groups.values()].sort((a, b) => b.n - a.n), excluded, mapped, citySources, countrySources };
  }
  function cityOptions(places, country, query = "") {
    const search = normalize(query);
    return places.cities.flatMap(([code, point, names], id) => {
      if (parentCode(code) !== country || !names.some(name => normalize(name).includes(search))) return [];
      const label = cityName(names) + (code === "TW" ? "（台湾）" : "");
      return [{ id: String(id), label, point }];
    }).sort((a, b) => a.label.localeCompare(b.label, "zh-CN"));
  }
  return { normalize, cityIndex, aggregate, cityOptions, parentCode, countryName, placeName, threat, cometTrail, threatBursts };
})();
