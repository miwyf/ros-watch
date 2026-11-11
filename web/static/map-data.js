"use strict";
const ROSMapData = (() => {
  const chinaRegions = { TW: "中国台湾", HK: "中国香港", MO: "中国澳门" };
  const parentCode = code => Object.hasOwn(chinaRegions, code) ? "CN" : code;
  const countryName = (code, fallback) => code === "CN" ? "中国" : chinaRegions[code] || fallback;
  const cityName = names => names.find(name => /[\u3400-\u9fff]/.test(name)) || names[0];
  const placeName = (code, names) => countryName(code, code) + " · " + cityName(names);
  const normalize = s => String(s || "").normalize("NFKD").replace(/[\u0300-\u036f]/g, "").toLowerCase().replace(/市$/, "").replace(/[\s'’.-]/g, "");
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
  function aggregate(rows, locations, places, index) {
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
      if (!groups.has(key)) groups.set(key, { key, point, label, level: city ? "城市近似位置" : "国家/地区参考点", n: 0, blocked: 0, sources: [] });
      const group = groups.get(key);
      group.n += row.n; group.blocked += row.blocked; group.sources.push(row.src);
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
  return { normalize, cityIndex, aggregate, cityOptions, parentCode, countryName, placeName };
})();
