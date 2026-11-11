const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { test } = require("node:test");

function page() {
  const elements = {};
  const element = id => elements[id] ||= {
    value: "", checked: false, disabled: false, textContent: "", handlers: {},
    classList: { toggle() {} }, addEventListener(name, handler) { this.handlers[name] = handler; },
  };
  const messages = [];
  const context = vm.createContext({
    $: element, window: { loadSettings() {} }, settingsTab: "mqtt", document: { hidden: false },
    settingsMessage: (...args) => messages.push(args), dl() {}, date: String, format: String, setInterval() {},
  });
  vm.runInContext(fs.readFileSync(path.join(__dirname, "../web/static/mqtt.js"), "utf8"), context);
  return { context, element, messages };
}

function data() {
  return { settings: { enabled: true, broker: "tcp://broker:1883", username: "fixture", device_name: "ROS Watch", discovery_prefix: "homeassistant", password_set: true }, status: { connected: true, published: 1 } };
}

test("MQTT password stays blank; loading other tabs preserves MQTT edits", async () => {
  const { context, element } = page();
  context.api = async () => data();
  await context.window.loadSettings();
  await context.loadMQTT();
  assert.equal(element("mqtt-password").value, "");
  assert.match(element("mqtt-password-hint").textContent, /留空保留/);
  element("mqtt-device_name").value = "未保存的名称";
  await context.window.loadSettings();
  assert.equal(element("mqtt-device_name").value, "未保存的名称");
});

test("MQTT publish rejects unsaved drafts; save does not return the password", async () => {
  const { context, element, messages } = page();
  context.populateMQTT(data());
  let publishes = 0;
  context.postSettings = async (url, body) => {
    if (url === "/api/mqtt/publish") { publishes++; return { message: "published" }; }
    assert.equal(body.password, "fixture-new-secret");
    return { settings: data().settings, message: "saved" };
  };
  context.api = async () => data();
  element("mqtt-password").value = "fixture-new-secret";
  element("mqtt-form").handlers.input();
  await element("mqtt-publish").handlers.click();
  assert.equal(publishes, 0);
  assert.match(messages.at(-1)[1], /先保存/);
  await element("mqtt-form").handlers.submit({ preventDefault() {}, submitter: {} });
  assert.equal(element("mqtt-password").value, "");
  await element("mqtt-publish").handlers.click();
  assert.equal(publishes, 1);
});
