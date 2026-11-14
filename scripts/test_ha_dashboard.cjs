const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const { test } = require("node:test");

const yaml = fs.readFileSync(path.join(__dirname, "../docs/home-assistant-observatory.yaml"), "utf8").replace(/\r\n/g, "\n");
const blocks = Object.fromEntries([...yaml.matchAll(/^          (\w+): \|\n            \[\[\[\n([\s\S]*?)\n            \]\]\]/gm)].map(match => [match[1], new Function("states", match[2])]));
const key = name => "sensor.ros_watch_INSTANCE_" + name;
const states = {
  [key("access_records_12h")]: { state: "152" },
  [key("scan_sources_12h")]: { state: "27" },
  [key("inbound_records_12h")]: { state: "101" },
  [key("response_records_12h")]: { state: "50" },
  [key("unknown_records_12h")]: { state: "1" },
  [key("business_records_12h")]: { state: "80" },
  [key("top_sources")]: { state: "1.2.3.4", attributes: {
    items: [{ source: "all-source", count: 150, location: "澳大利亚" }],
    suspicious_items: [{ source: "<suspicious>", count: 15, location: "美国" }],
    business_items: [{ source: "normal-mqtt", count: 80, location: "中国" }],
  } },
  [key("top_ports")]: { state: "1883 / TCP", attributes: {
    items: [{ dport: 1883, proto: "TCP", n: 80 }],
    suspicious_items: [{ dport: 22, proto: "TCP", n: 15 }],
    business_items: [{ dport: 8883, proto: "TCP", n: 50 }],
  } },
  [key("recent_events")]: { state: "连续登录失败", attributes: { items: [{ label: "连续登录失败", source: "<img onerror=alert(1)>", location: "中国", last: 1800000000 }] } },
};

test("all HA dashboard fields render missing, available and unavailable data", () => {
  assert.equal(Object.keys(blocks).length, 8);
  for (const state of [{}, states, Object.fromEntries(Object.entries(states).map(([key,value]) => [key, { ...value, state: "unavailable" }]))]) {
    for (const render of Object.values(blocks)) {
      const html = render(state);
      assert.equal(typeof html, "string");
      assert.doesNotMatch(html, /undefined|NaN|Invalid Date/);
    }
  }
  assert.match(blocks.metrics(states), /27/);
  assert.match(blocks.traffic(states), /101/);
  assert.match(blocks.traffic(states), /可能与方向分类重叠/);
});

test("HA ranks show suspicious, business and all data; hostile text is escaped", () => {
  const sources = blocks.sources(states), ports = blocks.ports(states);
  assert.match(sources, /&lt;suspicious&gt;/);
  assert.match(sources, /normal-mqtt/);
  assert.match(sources, /all-source/);
  assert.ok(sources.indexOf("近期可疑来源") < sources.indexOf("正常业务来源"));
  for (const port of [22, 8883, 1883]) assert.match(ports, new RegExp(String(port)));
  const events = blocks.events(states);
  assert.match(events, /连续登录失败/);
  assert.match(events, /&lt;img/);
  assert.doesNotMatch(events, /<img/);
  const stale = { ...states, [key("top_sources")]: { ...states[key("top_sources")], state: "unavailable" } };
  assert.doesNotMatch(blocks.sources(stale), /normal-mqtt|all-source|suspicious&gt;/);
});

test("original rank items remain displayable before classified attributes arrive", () => {
  const legacy = { [key("top_sources")]: { state: "legacy", attributes: { items: [{ source: "legacy-source", count: 1 }] } } };
  assert.match(blocks.sources(legacy), /legacy-source/);
  assert.doesNotMatch(blocks.sources(legacy), /undefined|NaN/);
});
