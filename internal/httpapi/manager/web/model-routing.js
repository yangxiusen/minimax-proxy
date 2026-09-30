"use strict";

(() => {
  const ui = window.managerUI;
  const { state, elements, formField: field, requestJSON: request, makeElement: el, localTime } = ui;
  const byID = (id) => document.getElementById(id);
  const json = (method, body) => ({ method, headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
  const labels = { ready: "可用", empty: "清单为空", error: "发现失败", stale: "清单已过期", pending: "待配置", uninitialized: "尚未发现", ambiguous: "协议冲突", unavailable: "不可用" };
  let protocols = null;
  let generation = 0;
  let modelState = freshModels();
  let routeGeneration = 0;
  let routePage = 1;
  let routePages = 1;
  let routeBusy = false;
  let routeLoading = false;

  function freshModels() {
    return { node: null, items: [], selected: new Set(), catalog: null, revision: null, loaded: false, loading: false, changed: false, connectionChanged: false, page: 1, error: "" };
  }

  function protocol() { return field("protocol_version").value; }
  function protocolName(id) { return protocols?.find((item)=>item.id===id)?.name || ({"tk2sd-v1":"tk2sd","h3-node-v1":"H3 内部节点","minimax-v2":"MiniMax 官方 V2","legacy-gradio-v1":"遗留 Gradio"})[id] || id || "未绑定"; }
  function source() {
    return protocols?.find((item) => item.id === protocol())?.model_source
      || ({ "tk2sd-v1": "discovered", "minimax-v2": "manual" }[protocol()] || "builtin");
  }
  function managed() {
    return protocol() === "tk2sd-v1" || Boolean(modelState.node?.model_catalog)
      || typeof modelState.node?.legacy_model_compat === "boolean" || (!modelState.node && Boolean(protocols));
  }
  function legacyLocked() { return modelState.node?.legacy_model_compat === true && !byID("exit-legacy-model").checked; }
  function validModel(id) { return typeof id === "string" && Array.from(id).length >= 1 && Array.from(id).length <= 128 && !/[\s\x00-\x1f\x7f*]/u.test(id); }
  function catalogSummary(catalog) {
    const expired = catalog.valid_until > 0 && catalog.valid_until <= Date.now() / 1000;
    return `${expired ? "清单已过期" : labels[catalog.status] || catalog.status || "尚未发现"} · 开放 ${catalog.enabled_count ?? 0} / ${catalog.model_count ?? 0}`;
  }
  function status(message, failed = false) {
    byID("model-catalog-status").textContent = message;
    byID("model-catalog-status").className = `form-status${failed ? " error" : ""}`;
  }
  function ensureProtocol(id) {
    if (!id || Array.from(field("protocol_version").options).some((item) => item.value === id)) return;
    const option = el("option", "", protocols?.find((item) => item.id === id)?.name || id);
    option.value = id;
    option.dataset.existingOnly = "true";
    field("protocol_version").append(option);
  }
  async function loadProtocols() {
    try {
      const response = await request("/manager/api/protocols");
      protocols = response.items || [];
      const current = protocol();
      field("protocol_version").replaceChildren(...protocols.filter((item) => item.creatable).map((item) => {
        const option = el("option", "", item.name);
        option.value = item.id;
        return option;
      }));
      if (state.editingNode) ensureProtocol(state.editingNode.protocol_version);
      field("protocol_version").value = current;
      if (!protocol()) field("protocol_version").selectedIndex = 0;
    } catch (error) {
      if (error.status !== 404 && error.status !== 405) throw error;
    }
  }

  function syncControls() {
    const active = managed();
    const locked = legacyLocked();
    const busy = state.nodeBusy || modelState.loading;
    byID("node-models").hidden = !active;
    const override = field("upstream_model");
    override.parentElement.hidden = protocol() === "tk2sd-v1" || (active && !modelState.node?.legacy_model_compat);
    override.readOnly = active;
    override.required = protocol() === "minimax-v2" && !active;
    override.disabled = state.nodeBusy || active || protocol() !== "minimax-v2";
    byID("model-source").textContent = ({ builtin: "内置模型", manual: "手动声明 · 未验证上游能力", discovered: "上游发现" })[source()] || source();
    byID("legacy-model-controls").hidden = !modelState.node?.legacy_model_compat;
    byID("exit-legacy-model").disabled = busy || Number(modelState.node?.active_tasks) > 0;
    byID("manual-model-field").hidden = source() !== "manual";
    byID("manual-models").disabled = busy || locked;
    byID("discover-models").hidden = source() !== "discovered";
    byID("discover-models").disabled = busy || locked;
    byID("reload-models").hidden = !modelState.node || modelState.connectionChanged;
    byID("reload-models").disabled = busy || locked;
    byID("node-model-items").querySelectorAll("input").forEach((input) => {
      input.disabled = busy || locked || (input.dataset.missing === "true" && !input.checked);
    });
    byID("model-search").disabled = busy;
    elements.saveNode.disabled = state.nodeBusy || (active && modelState.loading);
    elements.testNode.disabled = state.nodeBusy || modelState.loading;
	const hasModels=modelState.items.some((item)=>item.present!==false&&modelState.selected.has(item.model_id));
	field("enabled").disabled=busy||(active&&!hasModels);
	if(active&&!hasModels)field("enabled").checked=false;
    Array.from(field("protocol_version").options).forEach((option) => {
      option.disabled = option.dataset.existingOnly === "true" && option.value !== modelState.node?.protocol_version;
    });
    updateModelPager();
  }

  function filteredModels() {
    const query = byID("model-search").value;
    return modelState.items.filter((item) => item.model_id.includes(query));
  }
  function updateModelPager() {
    const total = filteredModels().length;
    const pages = Math.max(1, Math.ceil(total / 10));
    modelState.page = Math.min(modelState.page, pages);
    byID("models-page").textContent = `第 ${modelState.page} / ${pages} 页 · 共 ${total} 个`;
    byID("models-previous").disabled = state.nodeBusy || modelState.loading || modelState.page <= 1;
    byID("models-next").disabled = state.nodeBusy || modelState.loading || modelState.page >= pages;
  }
  function renderModels() {
    updateModelPager();
    const items = filteredModels().slice((modelState.page - 1) * 10, modelState.page * 10);
    byID("node-model-items").replaceChildren(...items.map((item) => {
      const label = el("label", "model-option");
      const checkbox = el("input");
      checkbox.type = "checkbox";
      checkbox.checked = modelState.selected.has(item.model_id);
      checkbox.dataset.missing = String(item.present === false);
      checkbox.addEventListener("change", () => {
        if (checkbox.checked) modelState.selected.add(item.model_id); else modelState.selected.delete(item.model_id);
        modelState.changed = true;
        state.formDirty = true;
        syncControls();
      });
      const text = el("span", "model-option-text");
      text.append(el("strong", "", item.model_id));
      const modes = (item.capabilities?.modes || []).map((mode) => {
        const durations=Array.isArray(mode.durations)?mode.durations:[];
        const continuous=durations.length>1&&durations.every((value,index)=>index===0||value===durations[index-1]+1);
        const duration=continuous?`${durations[0]}-${durations[durations.length-1]}`:durations.join(", ");
        return `${({t2va:"文生",i2va:"首帧图生",r2va:"素材参考"})[mode.scenario]||mode.scenario}${duration?` (${duration}秒)`:""}`;
      }).join(" · ");
      text.append(el("small", "muted", item.present === false ? "上游已移除" : modes || (item.verified ? "已验证" : "声明模型")));
      label.append(checkbox, text);
      return label;
    }));
    if (!items.length) byID("node-model-items").append(el("p", "muted", modelState.loading ? "正在加载模型..." : "没有匹配的模型"));
    syncControls();
  }

  // 完整读取最多 256 个模型，分页或搜索不会丢失其他页的开放选择。
  async function readCatalog(node, token) {
    const all = [];
    let first;
    for (let page = 1; page <= 3; page += 1) {
      const result = await request(`/manager/api/nodes/${encodeURIComponent(node.id)}/models?page_num=${page}&page_size=100&include_missing=true`);
      if (token !== generation) return null;
      if (!first) first = result;
      if (first.catalog_revision !== result.catalog_revision) throw new Error("模型清单已变化，请重新读取");
      all.push(...(result.items || []));
      if (all.length >= result.total) return { ...first, items: all };
    }
    throw new Error("模型清单超过支持数量，请检查节点配置");
  }
  function applyCatalog(catalog) {
    modelState.catalog = catalog;
    modelState.revision = catalog.catalog_revision;
    modelState.items = catalog.items || [];
    modelState.selected = new Set(modelState.items.filter((item) => item.enabled).map((item) => item.model_id));
    modelState.loaded = true;
    modelState.changed = false;
    modelState.error = "";
    if (source() === "manual") byID("manual-models").value = modelState.items.map((item) => item.model_id).join("\n");
    const expired = catalog.valid_until > 0 && catalog.valid_until <= Date.now() / 1000;
    status(`${expired ? "清单已过期" : labels[catalog.status] || catalog.status || "清单已读取"} · 最后成功 ${localTime(catalog.last_success_at)}${catalog.last_error_code ? ` · ${catalog.last_error_code}` : ""}`);
  }
  async function loadModels() {
    if (!modelState.node || !managed()) return;
    const token = ++generation;
    modelState.loading = true;
    status("正在读取模型清单...");
    renderModels();
    try {
      const catalog = await readCatalog(modelState.node, token);
      if (token !== generation || !catalog) return;
      applyCatalog(catalog);
    } catch (error) {
      if (token !== generation || error.message === "unauthorized") return;
      modelState.error = error.message;
      status(`${error.message} · 最后成功 ${localTime(modelState.catalog?.last_success_at)}`, true);
    } finally {
      if (token === generation) { modelState.loading = false; renderModels(); }
    }
  }
  function editNode(node) {
    generation += 1;
    modelState = freshModels();
    modelState.node = node;
    modelState.revision = node?.model_catalog?.revision ?? null;
    byID("exit-legacy-model").checked = false;
    byID("manual-models").value = "";
    byID("model-search").value = "";
    status("");
    if (managed() && node) loadModels();
    else initializeDraft();
    renderModels();
  }
  function initializeDraft() {
    if (source() === "builtin") {
      modelState.items = [{ model_id: "MiniMax-H3", verified: true }];
      modelState.selected = new Set(["MiniMax-H3"]);
      modelState.loaded = true;
      status("内置模型 MiniMax-H3");
    } else {
      status(source() === "manual" ? "请声明真实模型 ID" : "尚未发现模型");
    }
  }
  function connectionChanged(name) {
    if (name === "api_key" && source() === "manual") {
      modelState.connectionChanged = true;
      syncControls();
      return;
    }
    generation += 1;
    modelState.loading = false;
    modelState.connectionChanged = true;
    modelState.loaded = false;
    modelState.changed = true;
    modelState.catalog = null;
    modelState.items = [];
    modelState.selected.clear();
    byID("manual-models").value = "";
    initializeDraft();
    renderModels();
  }
  function connectionMatches() {
    const node = modelState.node;
    return node && node.protocol_version === protocol() && node.service_url === field("service_url").value.trim() && !field("api_key").value;
  }
  async function discover() {
    if (state.nodeBusy || modelState.loading || legacyLocked()) return;
    if (!field("service_url").reportValidity() || !field("api_key").reportValidity()) return;
    const saved = connectionMatches();
    if (saved && modelState.changed && !window.confirm("刷新将重新读取已保存的开放选择，放弃未保存的模型修改？")) return;
    const token = ++generation;
    ui.setNodeBusy(true);
    modelState.loading = true;
    status(saved ? "正在刷新已保存的模型清单..." : "正在发现草稿模型...");
    try {
      if (saved) {
        if (modelState.revision === null) throw new Error("请先重新读取模型清单");
        await request(`/manager/api/nodes/${encodeURIComponent(modelState.node.id)}/models/refresh`, json("POST", {
          version: modelState.node.version, catalog_revision: modelState.revision
        }));
        const catalog = await readCatalog(modelState.node, token);
        if (token !== generation || !catalog) return;
        applyCatalog(catalog);
      } else {
        const payload = { protocol_version: protocol(), service_url: field("service_url").value.trim(), request_timeout: field("request_timeout").value.trim() };
        if (field("api_key").value) payload.api_key = field("api_key").value;
        else if (modelState.node) { payload.id = modelState.node.id; payload.use_stored_api_key = true; }
        const result = await request("/manager/api/nodes/discover-models", json("POST", payload));
        if (token !== generation) return;
        const selected = modelState.selected;
		const firstDiscovery=!modelState.loaded;
        modelState.items = result.items || [];
        modelState.selected = new Set(modelState.items.filter((item) => selected.has(item.model_id)||(firstDiscovery&&item.enabled!==false)).map((item) => item.model_id));
        modelState.loaded = true;
        modelState.changed = true;
        modelState.error = "";
        status(modelState.items.length ? "草稿发现成功，请选择开放模型；保存时重新校验" : "上游返回空模型清单，不能启用节点");
      }
    } catch (error) {
      if (token !== generation || error.message === "unauthorized") return;
      modelState.error = error.message;
      status(`${error.message} · 最后成功 ${localTime(modelState.catalog?.last_success_at)}`, true);
    } finally {
      if (token === generation) modelState.loading = false;
      ui.setNodeBusy(false);
      renderModels();
    }
  }
  function prepareProbe(payload) {
    if (managed() || protocol() === "tk2sd-v1") delete payload.upstream_model;
  }
  function prepareSave(payload) {
    if (!managed()) return true;
    delete payload.upstream_model;
    const fail = (message) => { ui.setNodeFormStatus(message, "error"); return false; };
    if (legacyLocked()) {
      if (modelState.connectionChanged && (protocol() !== modelState.node.protocol_version || field("service_url").value.trim() !== modelState.node.service_url)) return fail("旧别名兼容节点更改实例前，请明确退出兼容模式");
      return true;
    }
    if (modelState.loading) return fail("模型清单正在加载，请稍后保存");
    const exiting = modelState.node?.legacy_model_compat === true;
    const needsModels = !modelState.node || modelState.changed || modelState.connectionChanged || exiting;
    if (payload.enabled && (!modelState.loaded || modelState.items.length === 0)) return fail("启用节点前须取得非空模型清单");
    const selected = Array.from(modelState.selected);
    if (payload.enabled && selected.length === 0) return fail("请至少开放一个模型");
    if (payload.enabled && !modelState.items.some((item) => item.present !== false && modelState.selected.has(item.model_id))) return fail("所选模型均已从上游移除，请刷新清单或停用节点");
    if (selected.length > 256 || selected.some((id) => !validModel(id))) return fail("模型 ID 必须为 1 至 128 字符，不含空白、控制符或 *；最多 256 个");
    if (source() === "discovered" && payload.enabled && modelState.catalog?.valid_until > 0 && modelState.catalog.valid_until <= Date.now() / 1000) return fail("模型清单已过期，请刷新后启用");
    if (needsModels && (modelState.loaded || selected.length)) {
      if (modelState.node && !Number.isInteger(modelState.revision)) return fail("缺少模型清单版本，请重新读取后保存");
      payload.enabled_models = selected;
      if (modelState.node) payload.catalog_revision = modelState.revision;
    }
    if (exiting) {
      if (!selected.length || !modelState.loaded) return fail("退出兼容模式必须声明真实模型");
      if (!window.confirm("确认退出旧别名兼容模式？旧别名将不再用于新请求，且不能恢复兼容标记。")) return false;
      payload.legacy_model_compat = false;
    }
    return true;
  }

  function routeStatus(message, failed = false) {
    byID("model-routes-status").textContent = message;
    byID("model-routes-status").className = `form-status${failed ? " error" : ""}`;
  }
  function syncRoutes() {
    byID("model-routes-dialog").querySelectorAll("button, select, input").forEach((control) => { control.disabled = routeBusy || routeLoading; });
    byID("routes-previous").disabled = routeBusy || routeLoading || routePage <= 1;
    byID("routes-next").disabled = routeBusy || routeLoading || routePage >= routePages;
    byID("model-route-items").querySelectorAll("button[data-no-candidates]").forEach((button) => { button.disabled = true; });
  }
  function choicesFor(candidates, current) {
    const select = el("select");
    select.setAttribute("aria-label", "指定协议");
    const placeholder = el("option", "", "选择协议");
    placeholder.value = "";
    select.append(placeholder);
    (candidates || []).forEach((candidate) => {
      const option = el("option", "", `${candidate.protocol_version} · 健康 ${candidate.healthy_nodes ?? 0} / ${(candidate.node_ids || []).length}`);
      option.value = candidate.protocol_version;
      select.append(option);
    });
    select.value = current || "";
    if (select.selectedIndex < 0) select.value = "";
    return select;
  }
  async function loadRoutes() {
    const token = ++routeGeneration;
    routeLoading = true;
    syncRoutes();
    routeStatus("正在读取模型线路...");
    const query = new URLSearchParams({ page_num: String(routePage), page_size: "20" });
    if (byID("route-search").value) query.set("q", byID("route-search").value);
    if (byID("route-state").value) query.set("state", byID("route-state").value);
    try {
      const result = await request(`/manager/api/model-routes?${query}`);
      if (token !== routeGeneration) return;
      routePages = Math.max(1, Math.ceil(result.total / 20));
      if (routePage > routePages) { routePage = routePages; return await loadRoutes(); }
      byID("routes-page").textContent = `第 ${routePage} / ${routePages} 页 · 共 ${result.total} 条`;
      byID("model-route-items").replaceChildren(...(result.items || []).map((route) => {
        const row = el("section", "model-route-row");
        const selection=({auto:"唯一协议",manual:"已指定协议",unresolved:"待指定协议"})[route.selection_mode]||"待指定协议";
        const reason=({multiple_protocols:"多个协议声明了同名模型",no_healthy_nodes:"所选协议暂无可用节点",no_protocol_nodes:"所选协议没有开放节点",route_not_selected:"尚未选择协议"})[route.wait_reason]||route.wait_reason;
        row.append(el("h3", "", route.model), el("p", "muted", `${labels[route.state] || route.state} · ${protocolName(route.protocol_version)} · ${selection}${reason ? ` · ${reason}` : ""}`));
        if (route.legacy_awaiting_tasks) row.append(el("p", "muted", `历史待绑定任务 ${route.legacy_awaiting_tasks} 个`));
        const controls = el("div", "model-toolbar");
        const select = choicesFor(route.candidates, route.protocol_version);
        const bind = el("button", "", "确认绑定");
        bind.type = "button";
        if (!route.candidates?.length) bind.dataset.noCandidates = "true";
        bind.addEventListener("click", async () => {
          if (routeBusy || routeLoading || !select.value) return;
          if (!window.confirm(`将 ${route.model} 绑定到 ${select.value}？仅影响新请求。所选协议不可用时不会切换其他协议，已有任务保持原线路。`)) return;
          routeBusy = true;
          syncRoutes();
          try {
            await request("/manager/api/model-routes", json("PUT", { model: route.model, protocol_version: select.value, version: route.version, routing_revision: result.routing_revision }));
            await loadRoutes();
            routeStatus("模型线路已绑定，仅影响新请求");
          } catch (error) {
            if (error.message !== "unauthorized") {
              if (error.status === 409) await loadRoutes();
              routeStatus(`${error.message}${error.status === 409 ? "，请核对最新线路后重新选择" : ""}`, true);
            }
          } finally { routeBusy = false; syncRoutes(); }
        });
        controls.append(select, bind);
        row.append(controls);
        return row;
      }));
      routeStatus(result.items?.length ? "" : "没有匹配的模型线路");
    } catch (error) {
      if (token !== routeGeneration || error.message === "unauthorized") return;
      byID("model-route-items").replaceChildren();
      routeStatus(error.message, true);
    } finally {
      if (token === routeGeneration) { routeLoading = false; syncRoutes(); }
    }
  }

  function renderTaskRouting(detail, container) {
    if (!detail.routing && !detail.remote) return;
    const section = el("section", "task-detail-section");
    section.append(el("h3", "", "模型线路与远程阶段"));
    const routing = detail.routing || {};
    section.append(ui.detailRow("模型", routing.model || detail.model), ui.detailRow("协议", routing.protocol_version), ui.detailRow("线路状态", routing.route_state));
    if (detail.remote) {
      const remote = detail.remote;
      section.append(ui.detailRow("远程阶段", remote.phase), ui.detailRow("节点", remote.node_id), ui.detailRow("取消状态", remote.cancel_state), ui.detailRow("素材上传", `${remote.uploaded_media ?? "--"} / ${remote.total_media ?? "--"}`), ui.detailRow("待对账", remote.reconciliation_required ? "是" : "否"), ui.detailRow("错误码", remote.last_error_code));
    }
    if (detail.delivery_error) section.append(ui.detailRow("结果获取", detail.delivery_error.message || detail.delivery_error.code));
    if (detail.ignored_request_fields?.length) section.append(ui.detailRow("忽略参数", detail.ignored_request_fields.join(", ")));
    container.prepend(section);
    if (routing.route_state !== "awaiting_route" || routing.can_bind_legacy_route !== true || !Number.isInteger(detail.version)) return;
    const open = el("button", "", "为历史任务指定协议");
    open.type = "button";
    section.append(open);
    open.addEventListener("click", async () => {
      open.disabled = true;
      const message = el("p", "form-status");
      section.append(message);
      try {
        let response;
        let route;
        for (let page = 1; ; page += 1) {
          response = await request(`/manager/api/model-routes?${new URLSearchParams({ q: routing.model || detail.model, page_num: String(page), page_size: "100" })}`);
          route = response.items?.find((item) => item.model === (routing.model || detail.model));
          if (route || page * 100 >= response.total) break;
        }
        if (!route?.candidates?.length) throw new Error("没有可绑定的模型协议");
        const select = choicesFor(route.candidates, "");
        const bind = el("button", "", "确认绑定此任务");
        bind.type = "button";
        section.append(select, bind);
        bind.addEventListener("click", async () => {
          if (!select.value || bind.disabled) return;
          if (!window.confirm(`仅为历史任务 ${detail.id} 绑定 ${select.value}？服务端将校验历史输入及未提交状态。`)) return;
          bind.disabled = true;
          select.disabled = true;
          try {
            await request(`/manager/api/tasks/${encodeURIComponent(detail.id)}/route`, json("POST", { protocol_version: select.value, version: detail.version, routing_revision: response.routing_revision }));
            const updated = await request(`/manager/api/tasks/${encodeURIComponent(detail.id)}`);
            if (section.isConnected) ui.renderTaskDetail(updated);
            ui.loadTasks();
          } catch (error) {
            if (error.message !== "unauthorized") message.textContent = `${error.message}。请重新打开任务详情后重试。`;
          }
        });
      } catch (error) {
        if (error.message !== "unauthorized") message.textContent = error.message;
        open.disabled = false;
      }
    });
  }

  ["service_url", "api_key"].forEach((name) => field(name).addEventListener("input", () => connectionChanged(name)));
  field("protocol_version").addEventListener("change", () => connectionChanged("protocol_version"));
  byID("exit-legacy-model").addEventListener("change", () => { modelState.changed = true; state.formDirty = true; syncControls(); });
  byID("manual-models").addEventListener("input", () => {
    const previous = new Set(modelState.items.map((item) => item.model_id));
    const ids = [...new Set(byID("manual-models").value.split(/\r?\n/).map((id) => id.trim()).filter(Boolean))];
    modelState.selected = new Set(ids.filter((id) => !previous.has(id) || modelState.selected.has(id)));
    modelState.items = ids.map((id) => ({ model_id: id, verified: false }));
    modelState.loaded = true;
    modelState.changed = true;
    state.formDirty = true;
    renderModels();
  });
  byID("model-search").addEventListener("input", () => { modelState.page = 1; renderModels(); });
  byID("models-previous").addEventListener("click", () => { modelState.page -= 1; renderModels(); });
  byID("models-next").addEventListener("click", () => { modelState.page += 1; renderModels(); });
  byID("discover-models").addEventListener("click", discover);
  byID("reload-models").addEventListener("click", () => {
    if (modelState.changed && !window.confirm("放弃未保存的模型修改并重新读取？")) return;
    loadModels();
  });
  byID("open-model-routes").addEventListener("click", () => { byID("model-routes-dialog").showModal(); loadRoutes(); });
  byID("close-model-routes").addEventListener("click", () => { if (!routeBusy) byID("model-routes-dialog").close(); });
  byID("model-routes-dialog").addEventListener("cancel", (event) => { if (routeBusy) event.preventDefault(); });
  byID("route-search-form").addEventListener("submit", (event) => { event.preventDefault(); routePage = 1; loadRoutes(); });
  byID("route-state").addEventListener("change", () => { routePage = 1; loadRoutes(); });
  byID("routes-previous").addEventListener("click", () => { routePage -= 1; loadRoutes(); });
  byID("routes-next").addEventListener("click", () => { routePage += 1; loadRoutes(); });
  window.modelRouting = { loadProtocols, ensureProtocol, editNode, syncControls, prepareSave, prepareProbe, catalogSummary, renderTaskRouting };
})();
