const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { test } = require("node:test");
const context = vm.createContext({});
vm.runInContext(fs.readFileSync(path.join(__dirname, "../web/static/map-data.js"), "utf8"), context);
const helpers = vm.runInContext("ROSMapData", context);
const places = JSON.parse(fs.readFileSync(path.join(__dirname, "../web/static/map-places.json"), "utf8"));
const index = helpers.cityIndex(places);

test("matches Chinese city names and English names within the correct country", () => {
  const result = helpers.aggregate([{ src: "a", n: 4, blocked: 2 }, { src: "b", n: 3, blocked: 0 }], {
    a: { kind: "located", country_code: "CN", city: "深圳市" },
    b: { kind: "located", country_code: "CN", city: "Shenzhen" },
  }, places, index);
  assert.equal(result.points.length, 1);
  assert.equal(result.points[0].n, 7);
  assert.equal(result.points[0].blocked, 2);
  assert.ok(Math.abs(result.points[0].point[0] - 114.12) < 0.1);
  assert.equal(result.citySources, 2);
});

test("falls back honestly and does not place special or unknown addresses", () => {
  const result = helpers.aggregate([{ src: "a", n: 4, blocked: 0 }, { src: "b", n: 3, blocked: 0 }, { src: "c", n: 2, blocked: 0 }], {
    a: { kind: "located", country_code: "US", city: "does not exist" },
    b: { kind: "special", country_code: "CN", city: "深圳" },
    c: { kind: "unknown" },
  }, places, index);
  assert.equal(result.mapped, 4); assert.equal(result.excluded, 5);
  assert.equal(result.countrySources, 1);
  assert.equal(result.points[0].level, "国家/地区参考点");
});

test("ambiguous cities never get an arbitrary coordinate", () => {
  const fixture = { countries: { US: { name: "美国", point: [-100, 40] } }, cities: [["US", [-100, 40], ["Springfield"]], ["US", [-80, 35], ["Springfield"]]] };
  const result = helpers.aggregate([{ src: "a", n: 1, blocked: 0 }], { a: { kind: "located", country_code: "US", city: "Springfield" } }, fixture, helpers.cityIndex(fixture));
  assert.equal(result.countrySources, 1);
});

test("small regions have valid reference coordinates even without a 110m polygon", () => {
  for (const code of ["CN", "US", "SG", "HK", "MO", "JP", "DE", "AU"]) {
    const country = places.countries[code];
    assert.ok(country, code);
    assert.ok(Number.isFinite(country.point[0]) && Math.abs(country.point[0]) <= 180, code);
    assert.ok(Number.isFinite(country.point[1]) && Math.abs(country.point[1]) <= 90, code);
  }
});

test("CN city choices support Chinese and English search and stable coordinates", () => {
  const chinese = helpers.cityOptions(places, "CN", "深圳市");
  const english = helpers.cityOptions(places, "CN", "shenzhen");
  assert.equal(chinese.length, 1);
  assert.equal(english.length, 1);
  assert.equal(chinese[0].id, english[0].id);
  assert.match(chinese[0].label, /深圳/);
  assert.ok(Math.abs(chinese[0].point[0] - 114.12) < 0.1);
  assert.equal(helpers.cityOptions(places, "JP", "深圳").length, 0);
  assert.equal(helpers.cityOptions(places, "CN", "no-such-city").length, 0);
});

test("region labels and city aliases stay consistent", () => {
  for (const [code, expected, query] of [["TW", "中国台湾", "Taipei"], ["HK", "中国香港", "Hong Kong"], ["MO", "中国澳门", "Macau"]]) {
    assert.equal(helpers.parentCode(code), "CN");
    assert.equal(helpers.countryName(code, "unused"), expected);
    assert.equal(places.countries[code].name, expected);
    const result = helpers.aggregate([{src:"test",n:1,blocked:0}], {test:{kind:"located",country_code:code,city:query}}, places, index);
    assert.ok(result.points[0].label.startsWith(expected), result.points[0].label);
    assert.ok(helpers.cityOptions(places, "CN", query).length > 0, query);
  }
  assert.ok(helpers.cityOptions(places, "CN", "台北").some(item => item.label.includes("台湾")));
  assert.ok(index.has("CN:taipei"));
});

test("bundled geometry renders with both projections and valid spherical areas", () => {
  for (const file of ["d3-array-3.2.4.min.js", "d3-geo-3.1.1.min.js"]) {
    vm.runInContext(fs.readFileSync(path.join(__dirname, "../web/static", file), "utf8"), context);
  }
  const d3 = context.d3;
  const world = JSON.parse(fs.readFileSync(path.join(__dirname, "../web/static/map-world.json"), "utf8"));
  assert.equal(world.type, "FeatureCollection");
  assert.equal(world.features.length, 217);
  assert.equal(world.symbols.features[0].geometry.coordinates.length, 10);
  for (const feature of [...world.features, ...world.symbols.features]) {
    assert.equal(feature.type, "Feature");
    assert.ok(["Polygon", "MultiPolygon"].includes(feature.geometry.type));
    const area = d3.geoArea(feature);
    assert.ok(Number.isFinite(area) && area > 0 && area < Math.PI * 2, feature.properties.name || "symbol");
    assert.ok(d3.geoBounds(feature).flat().every(Number.isFinite));
    const polygons = feature.geometry.type === "Polygon" ? [feature.geometry.coordinates] : feature.geometry.coordinates;
    for (const polygon of polygons) {
      assert.ok(polygon.length > 0);
      for (const ring of polygon) {
        assert.ok(ring.length >= 4);
        assert.deepEqual(ring[0], ring.at(-1));
        assert.ok(ring.every(point => Number.isFinite(point[0]) && Math.abs(point[0]) <= 180 && Number.isFinite(point[1]) && Math.abs(point[1]) <= 90));
      }
    }
  }
  for (const projection of [d3.geoEquirectangular(), d3.geoOrthographic().rotate([-105, -20])]) {
    const path = d3.geoPath(projection);
    for (const layer of [world, world.symbols]) {
      const drawing = path(layer);
      assert.ok(drawing && drawing.length > 100);
      assert.doesNotMatch(drawing, /NaN|Infinity/);
      assert.ok(path.bounds(layer).flat().every(Number.isFinite));
    }
  }
  const reference = world.features.find(feature => feature.properties.name === "China");
  for (const point of [[120.96, 23.7], [79, 35], [91.86, 27.59], [94.5, 28.2]]) assert.ok(d3.geoContains(reference, point));
  assert.ok(reference.geometry.coordinates.flat(2).some(point => point[1] < 5));
  for (const point of [[119.6, 23.57], [123.473333, 25.743333], [124.558333, 25.921667], [122.08, 25.625], [121.55, 22.04], [121.49, 22.66]]) {
    assert.ok(d3.geoContains(reference, point), "missing reference geometry at " + point.join(","));
  }
});
