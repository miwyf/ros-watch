"use strict";
(() => {
  const presets = [
    {name:"DeepSeek", url:"https://api.deepseek.com", models:["deepseek-flash", "deepseek-v4-pro"]},
    {name:"通义千问", url:"https://dashscope.aliyuncs.com/compatible-mode/v1", models:["qwen-plus", "qwen3.7-plus"]},
    {name:"Kimi 国内", url:"https://api.moonshot.cn/v1", models:["kimi-k2.6"]},
    {name:"Kimi 国际", url:"https://api.moonshot.ai/v1", models:["kimi-k2.6", "kimi-k3.1-pro"]},
    {name:"OpenAI 官方", url:"https://api.openai.com/v1", models:["gpt-5-mini", "gpt-4.1-mini"]},
    {name:"智谱 GLM", url:"https://open.bigmodel.cn/api/paas/v4", models:["glm-4.7"]},
    {name:"Google Gemini", url:"https://generativelanguage.googleapis.com/v1beta/openai", models:["gemini-4-hydrogen"]},
    {name:"硅基流动", url:"https://api.siliconflow.cn/v1", models:["Pro/zai-org/GLM-5.1"]},
    {name:"OpenRouter", url:"https://openrouter.ai/api/v1", models:["openai/gpt-5-mini"]},
    {name:"本地 Ollama", url:"http://127.0.0.1:11434/v1", models:["qwen3:8b"]},
    {name:"OpenAI 兼容", url:"", models:[]},
  ];
  const byId = id => document.getElementById(id);
  const dialog = byId("ai-config-dialog");
  const thread = byId("ai-thread");
  const messages = byId("ai-messages");
  const question = byId("ai-question");
  const model = byId("ai-model");
  const edit = byId("ai-edit-profile");
  const picker = byId("ai-provider-picker");
  const addProfile = byId("ai-add-profile");
  const status = byId("ai-status");
  const configStatus = byId("ai-config-result");
  const modelStatus = byId("ai-model-status");
  const refreshModels = byId("ai-refresh-models");
  const modelInput = byId("ai-profile-model");
  const modelChoices = byId("ai-model-choices");
  const modelResults = byId("ai-model-results");
  const baseInput = byId("ai-profile-url");
  const customAuth = byId("ai-custom-auth");
  const customKey = byId("ai-custom-key");
  const customTest = byId("ai-custom-test");
  const customStatus = byId("ai-custom-result");
  let settings, loading, draft, editing, configBusy = false;
  let conversation = "", pending = null;
  let modelRequest = null;

  function messageStatus(element, text, error = false) {
    element.textContent = text;
    element.classList.toggle("error", error);
  }

  function post(path, body, signal) {
    return api(path, {
      method: "POST", signal,
      headers: {"Content-Type":"application/json", "X-CSRF-Token":document.querySelector('meta[name="csrf-token"]').content},
      body: JSON.stringify(body),
    });
  }

  const baseKey = url => (url || "").trim().replace(/\/+$/, "").replace(/\/chat\/completions$/, "");
  const presetForURL = url => presets.find(preset => preset.url && preset.url === baseKey(url));

  function options(element, profiles, selected, emptyLabel) {
    element.replaceChildren();
    if (!profiles.length) element.add(new Option(emptyLabel, ""));
    const names = profiles.map(profile => profile.name === "OpenAI" && presetForURL(profile.base_url)?.name === "OpenAI 官方"
      ? "OpenAI 官方" : profile.name || "新连接");
    const labels = profiles.map((profile, index) => names.filter(name => name === names[index]).length > 1
      ? [names[index], profile.model].filter(Boolean).join(" · ") : names[index]);
    for (const [index, profile] of profiles.entries()) {
      const label = labels[index];
      const order = labels.slice(0, index).filter(other => other === label).length;
      element.add(new Option(order ? `${label} (${order + 1})` : label, profile.id));
    }
    element.value = selected || profiles[0]?.id || "";
  }

  function buttons() {
    byId("ai-send").disabled = !!pending || !model.value || !question.value.trim();
    byId("ai-stop").classList.toggle("hidden", !pending);
    model.disabled = !!pending || !settings?.profiles.length;
    byId("ai-new").disabled = !!pending;
    byId("ai-configure").disabled = !!pending;
  }

  function resetConversation() {
    conversation = "";
    messages.replaceChildren();
    byId("ai-welcome").classList.remove("hidden");
    messageStatus(status, "");
  }

  function setSettings(value) {
    const previous = settings?.profiles.find(p => p.id === model.value);
    settings = value;
    const selected = settings.profiles.find(p => p.id === previous?.id)
      || settings.profiles.find(p => p.id === settings.default_profile);
    options(model, settings.profiles, selected?.id, "未配置");
    const sameModel = previous && selected && previous.id === selected.id
      && previous.base_url === selected.base_url && previous.model === selected.model;
    if (!sameModel) resetConversation();
    byId("ai-welcome-copy").textContent = selected
      ? "按时间、来源和端口分析访问记录，或追问上一轮结果。"
      : "先配置一个支持工具调用的模型连接。";
    buttons();
  }

  function loadSettings(force = false) {
    if (loading) return loading;
    if (settings && !force) return Promise.resolve();
    loading = api("/api/ai/settings").then(setSettings).finally(() => { loading = null; });
    return loading;
  }

  function showProfile(id) {
    const p = draft.profiles.find(profile => profile.id === id);
    editing = p?.id;
    byId("ai-profile-fields").classList.toggle("hidden", !p);
    modelInput.value = p?.model || "";
    baseInput.value = p?.base_url || "";
    resetModelResults();
    if (!p) return;
    byId("ai-profile-name").value = p.name || "";
    byId("ai-profile-key").value = p.api_key || "";
    byId("ai-profile-key").placeholder = p.key_set ? "已保存，留空沿用原 Key"
      : baseInput.value.startsWith("https:") ? "填写该平台的 API Key" : "无密钥的本地服务可留空";
    byId("ai-profile-default").checked = p.id === draft.default_profile;
    byId("ai-profile-clear").checked = !!p.clear_key;
    byId("ai-profile-clear").disabled = !p.key_set;
    byId("ai-profile-clear").closest("label").classList.toggle("hidden", !p.key_set);
    byId("ai-test-profile").disabled = configBusy || !settings.profiles.some(x => x.id === p.id);
  }

  function editProfiles(selected = editing) {
    options(edit, draft.profiles, selected, "暂无连接");
    showProfile(edit.value);
    addProfile.disabled = configBusy || draft.profiles.length >= 10;
    if (addProfile.disabled) showPicker(false);
  }

  function rememberProfile() {
    const p = draft.profiles.find(profile => profile.id === editing);
    if (!p) return;
    p.name = byId("ai-profile-name").value.trim();
    p.model = modelInput.value.trim();
    p.base_url = baseInput.value.trim();
    p.api_key = byId("ai-profile-key").value.trim();
    p.clear_key = byId("ai-profile-clear").checked;
    if (byId("ai-profile-default").checked) draft.default_profile = p.id;
    else if (draft.default_profile === p.id) draft.default_profile = "";
  }

  const intelFields = {
    greynoise:"ai-grey-on", abuseipdb:"ai-abuse-on", spamhaus:"ai-drop-on",
    clear_grey:"ai-grey-clear", clear_abuse:"ai-abuse-clear",
  };
  const customIntelFields = {
    classification:["ai-custom-classification", "分类"],
    listed:["ai-custom-listed", "名单命中"],
    confidence:["ai-custom-confidence", "评分"],
    reported_count:["ai-custom-reported-count", "举报数"],
    last_seen:["ai-custom-last-seen", "最后出现"],
  };

  function showCustomKey() {
    const saved = settings.reputation.custom?.key_set;
    const show = customAuth.value !== "none";
    byId("ai-custom-key-label").classList.toggle("hidden", !show);
    byId("ai-custom-clear").closest("label").classList.toggle("hidden", !show || !saved);
  }

  function loadDialog(selected) {
    draft = structuredClone(settings);
    editProfiles(selected);
    showPicker(!draft.profiles.length);
    const rep = draft.reputation;
    for (const [key,id] of Object.entries(intelFields)) byId(id).checked = !!rep[key];
    byId("ai-grey-key").value = "";
    byId("ai-abuse-key").value = "";
    byId("ai-grey-key").placeholder = rep.grey_key_set ? "已保存，留空沿用原 Key" : "可选";
    byId("ai-abuse-key").placeholder = rep.abuse_key_set ? "已保存，留空沿用原 Key" : "启用时必填";
    byId("ai-grey-clear").disabled = !rep.grey_key_set;
    byId("ai-abuse-clear").disabled = !rep.abuse_key_set;
    byId("ai-grey-clear").closest("label").classList.toggle("hidden", !rep.grey_key_set);
    byId("ai-abuse-clear").closest("label").classList.toggle("hidden", !rep.abuse_key_set);
    const custom = rep.custom || {};
    byId("ai-custom-on").checked = !!custom.enabled;
    byId("ai-custom-url").value = custom.url || "";
    customAuth.value = custom.auth || "none";
    customKey.value = "";
    customKey.placeholder = custom.key_set ? "已保存，留空沿用原 Key" : "填写该接口的 API Key";
    byId("ai-custom-clear").checked = false;
    for (const [field,[id]] of Object.entries(customIntelFields)) byId(id).value = custom.fields?.[field] || "";
    showCustomKey();
    customTest.disabled = configBusy || !custom.enabled;
    messageStatus(customStatus, "");
  }

  async function openDialog() {
    if (dialog.open) return;
    try {
      await loadSettings(true);
      if (dialog.open) return;
      messageStatus(configStatus, "");
      loadDialog(model.value);
      dialog.showModal();
    } catch (error) {
      messageStatus(status, error.message, true);
    }
  }

  function setConfigBusy(busy) {
    configBusy = busy;
    refreshModels.disabled = busy || !!modelRequest;
    customTest.disabled = busy || !settings.reputation.custom?.enabled;
    for (const id of ["ai-save-config","ai-config-close","ai-edit-profile","ai-add-profile","ai-remove-profile","ai-test-profile"])
      byId(id).disabled = busy;
    if (!busy) {
      addProfile.disabled = draft.profiles.length >= 10;
      byId("ai-test-profile").disabled = !settings.profiles.some(p => p.id === editing);
    }
    for (const button of picker.querySelectorAll("button")) button.disabled = busy;
  }

  function resetModelResults() {
    modelRequest?.abort();
    modelRequest = null;
    refreshModels.disabled = configBusy;
    showModelOptions(presetForURL(baseInput.value)?.models || []);
    messageStatus(modelStatus, "");
  }

  function updateModelSelection() {
    const selected = modelInput.value.trim();
    for (const button of modelChoices.children) {
      button.setAttribute("aria-pressed", String(button.value === selected));
    }
    modelResults.value = selected;
    if (modelResults.selectedIndex === -1) modelResults.selectedIndex = 0;
  }

  function showModelOptions(models) {
    modelChoices.replaceChildren();
    modelResults.replaceChildren();
    const large = models.length > 6;
    modelResults.classList.toggle("hidden", !large);
    if (large) {
      modelResults.replaceChildren(new Option(`从 ${models.length} 个模型中选择`, ""), ...models.map(id => new Option(id, id)));
    } else {
      for (const id of models) {
        const button = document.createElement("button");
        button.type = "button";
        button.className = "outline-button";
        button.textContent = button.value = id;
        button.addEventListener("click", () => {
          modelInput.value = id;
          updateModelSelection();
        });
        modelChoices.append(button);
      }
    }
    updateModelSelection();
  }

  for (const id of ["ai-profile-url", "ai-profile-key", "ai-profile-clear"])
    byId(id).addEventListener("input", resetModelResults);
  refreshModels.addEventListener("click", async () => {
    if (configBusy || modelRequest || !editing) return;
    resetModelResults();
    const controller = new AbortController();
    modelRequest = controller;
    refreshModels.disabled = true;
    messageStatus(modelStatus, "正在读取模型列表…");
    try {
      const {models, total} = await post("/api/ai/models", {
        id:editing, base_url:baseInput.value.trim(),
        api_key:byId("ai-profile-key").value.trim(), clear_key:byId("ai-profile-clear").checked,
      }, controller.signal);
      if (modelRequest !== controller) return;
      if (!models.length) {
        messageStatus(modelStatus, "服务未返回模型，可手动填写模型 ID。");
        return;
      }
      showModelOptions(models);
      messageStatus(modelStatus, total === models.length
        ? `已获取 ${total} 个模型`
        : `显示 ${models.length} 个模型，共 ${total} 个`);
    } catch (error) {
      if (modelRequest === controller) messageStatus(modelStatus, error.message, true);
    } finally {
      if (modelRequest === controller) {
        modelRequest = null;
        refreshModels.disabled = configBusy;
      }
    }
  });
  modelResults.addEventListener("change", () => {
    if (modelResults.value) modelInput.value = modelResults.value;
  });
  modelInput.addEventListener("input", updateModelSelection);

  edit.addEventListener("change", () => {
    rememberProfile();
    editProfiles(edit.value);
    showPicker(false);
  });
  function showPicker(visible) {
    picker.classList.toggle("hidden", !visible);
    addProfile.setAttribute("aria-expanded", String(visible));
  }
  addProfile.addEventListener("click", () => {
    const open = picker.classList.contains("hidden");
    showPicker(open);
    if (open) picker.querySelector("button")?.focus();
  });
  for (const preset of presets) {
    const button = document.createElement("button");
    button.type = "button";
    button.className = "outline-button";
    button.textContent = preset.name;
    button.addEventListener("click", () => {
      if (configBusy || draft.profiles.length >= 10) return;
      rememberProfile();
      const matches = preset.url ? draft.profiles.filter(p => baseKey(p.base_url) === preset.url) : [];
      const existing = matches.find(p => p.id === editing) || matches[0];
      if (existing) {
        editProfiles(existing.id);
        showPicker(false);
        messageStatus(configStatus, "已打开现有连接");
        modelInput.focus();
        return;
      }
      const id = Array.from(crypto.getRandomValues(new Uint8Array(12)), v => v.toString(16).padStart(2,"0")).join("");
      draft.profiles.push({id,name:preset.name,base_url:preset.url,model:preset.models[0] || ""});
      if (!draft.default_profile) draft.default_profile = id;
      editProfiles(id);
      showPicker(false);
      messageStatus(configStatus, "");
      byId(!preset.url ? "ai-profile-url" : preset.url.startsWith("http:") ? "ai-profile-model" : "ai-profile-key").focus();
    });
    picker.append(button);
  }
  byId("ai-remove-profile").addEventListener("click", () => {
    draft.profiles = draft.profiles.filter(p => p.id !== editing);
    if (draft.default_profile === editing) draft.default_profile = draft.profiles[0]?.id || "";
    editProfiles(draft.default_profile);
    if (!draft.profiles.length) showPicker(true);
  });
  byId("ai-config-close").addEventListener("click", () => dialog.close());
  dialog.addEventListener("cancel", event => { if (configBusy) event.preventDefault(); });
  dialog.addEventListener("close", () => {
    resetModelResults();
    draft = undefined;
    for (const id of ["ai-profile-key","ai-grey-key","ai-abuse-key","ai-custom-key","ai-custom-test-ip"]) byId(id).value = "";
  });
  byId("ai-config-form").addEventListener("submit", async event => {
    event.preventDefault();
    if (configBusy || !draft) return;
    rememberProfile();
    for (const [key,id] of Object.entries(intelFields)) draft.reputation[key] = byId(id).checked;
    draft.reputation.grey_key = byId("ai-grey-key").value.trim();
    draft.reputation.abuse_key = byId("ai-abuse-key").value.trim();
    draft.reputation.custom = {
      enabled:byId("ai-custom-on").checked,
      url:byId("ai-custom-url").value.trim(),
      auth:customAuth.value,
      api_key:customKey.value.trim(),
      clear_key:byId("ai-custom-clear").checked,
      fields:Object.fromEntries(Object.entries(customIntelFields).map(([field,[id]]) => [field,byId(id).value.trim()])),
    };
    const changedKey = draft.profiles.some(p => (p.api_key || p.clear_key) && model.value === p.id);
    setConfigBusy(true);
    messageStatus(configStatus, "保存中…");
    try {
      setSettings(await post("/api/ai/settings", draft));
      if (changedKey && !pending) resetConversation();
      loadDialog(editing);
      messageStatus(configStatus, "已保存");
    } catch (error) {
      messageStatus(configStatus, error.message, true);
    } finally {
      setConfigBusy(false);
    }
  });
  byId("ai-test-profile").addEventListener("click", async () => {
    if (configBusy || !editing) return;
    setConfigBusy(true);
    messageStatus(configStatus, "正在测试已保存连接…");
    try {
      const result = await post("/api/ai/test", {profile:editing});
      messageStatus(configStatus, result.message);
    } catch (error) {
      messageStatus(configStatus, error.message, true);
    } finally {
      setConfigBusy(false);
    }
  });

  customAuth.addEventListener("change", showCustomKey);
  customTest.addEventListener("click", async () => {
    const ip = byId("ai-custom-test-ip").value.trim();
    if (configBusy) return;
    if (!ip) {
      messageStatus(customStatus, "请输入测试用公网 IP。", true);
      customStatus.scrollIntoView({block:"nearest"});
      return;
    }
    setConfigBusy(true);
    messageStatus(customStatus, "正在查询已保存的接口…");
    try {
      const data = await post("/api/ai/reputation/custom/test", {ip});
      const descriptions = {
        queried:"查询成功", partial:"部分字段缺失或格式不符", no_record:"接口返回无记录",
        authentication_failed:"认证失败", rate_limited:"接口限流", unavailable:"接口不可用或超时",
        invalid_response:"响应或配置的字段路径无效",
      };
      const lines = [descriptions[data.status] || "未知查询状态"];
      for (const [field,value] of Object.entries(data.result || {})) {
        const display = typeof value === "boolean" ? (value ? "是" : "否") : String(value);
        lines.push(`${customIntelFields[field][1]}  ${display}`);
      }
      messageStatus(customStatus, lines.join("\n"), !["queried","no_record"].includes(data.status));
    } catch (error) {
      messageStatus(customStatus, error.message, true);
    } finally {
      setConfigBusy(false);
      customStatus.scrollIntoView({block:"nearest"});
    }
  });

  function addMessage(role, title, content, evidence = [], sources = []) {
    const box = document.createElement("article");
    box.className = "ai-message " + role;
    const label = document.createElement("strong");
    label.textContent = title;
    const p = document.createElement("p");
    p.textContent = content;
    box.append(label, p);
    if (sources.length && sources.every(isBlockableSource)) {
      const commands = document.createElement("div");
      commands.className = "ai-block-command";
      const code = document.createElement("pre");
      code.textContent = buildBlockedSourceCommands(sources).block;
      const copy = document.createElement("button");
      copy.type = "button";
      copy.className = "outline-button";
      copy.textContent = "复制封禁指令";
      copy.addEventListener("click", () => openSourceBlocklist(sources, true));
      commands.append(code, copy);
      box.append(commands);
    }
    if (evidence.length) {
      const refs = document.createElement("div");
      refs.className = "ai-evidence";
      for (const ref of evidence) {
        if (!ref.url.startsWith("/?") && !ref.url.startsWith("https://")) continue;
        const link = document.createElement("a");
        link.textContent = ref.label;
        link.href = ref.url;
        link.target = "_blank";
        link.rel = "noopener noreferrer";
        refs.append(link);
      }
      box.append(refs);
    }
    messages.append(box);
    thread.scrollTop = thread.scrollHeight;
    return box;
  }

  byId("ai-chat-form").addEventListener("submit", async event => {
    event.preventDefault();
    const text = question.value.trim();
    if (!text || !model.value || pending) return;
    const controller = new AbortController();
    pending = controller;
    byId("ai-welcome").classList.add("hidden");
    const userMessage = addMessage("user", "你", text);
    question.value = "";
    messageStatus(status, "正在查询并分析…");
    buttons();
    try {
      const res = await post("/api/ai/chat", {
        conversation, profile:model.value, question:text, timezone_offset_minutes:-new Date().getTimezoneOffset(),
      }, controller.signal);
      conversation = res.conversation;
      addMessage("assistant", res.model, res.answer, res.evidence || [], res.block_sources || []);
      messageStatus(status, "");
    } catch (error) {
      userMessage.remove();
      question.value = text;
      if (!messages.children.length) byId("ai-welcome").classList.remove("hidden");
      messageStatus(status, error.name === "AbortError" ? "已停止" : error.message, error.name !== "AbortError");
    } finally {
      pending = null;
      buttons();
    }
  });

  question.addEventListener("input", buttons);
  question.addEventListener("keydown", event => {
    if (event.key === "Enter" && !event.shiftKey && !event.isComposing && event.keyCode !== 229) {
      event.preventDefault();
      byId("ai-chat-form").requestSubmit();
    }
  });
  document.querySelectorAll("[data-ai-prompt]").forEach(button => button.addEventListener("click", () => {
    if (pending) return;
    question.value = button.dataset.aiPrompt;
    buttons();
    question.focus();
  }));
  model.addEventListener("change", () => { resetConversation(); buttons(); });
  byId("ai-configure").addEventListener("click", openDialog);
  byId("ai-new").addEventListener("click", () => { resetConversation(); question.value=""; buttons(); question.focus(); });
  byId("ai-stop").addEventListener("click", () => pending?.abort());

  window.ROSAI = {enter:() => loadSettings().catch(e => messageStatus(status, e.message, true))};
  if (view === "ai") window.ROSAI.enter();
})();
