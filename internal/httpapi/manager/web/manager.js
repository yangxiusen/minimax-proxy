"use strict";

const elements = {
  freshness: document.getElementById("freshness"),
  taskTotal: document.getElementById("task-total"),
  taskRows: document.getElementById("task-rows"),
  statusFilter: document.getElementById("status-filter"),
  upstreamFilter: document.getElementById("upstream-filter"),
  searchFilter: document.getElementById("search-filter"),
  pageSize: document.getElementById("page-size"),
  previousPage: document.getElementById("previous-page"),
  nextPage: document.getElementById("next-page"),
  pageSummary: document.getElementById("page-summary"),
  healthSummary: document.getElementById("health-summary"),
  nodeList: document.getElementById("node-list"),
  nodeDetail: document.getElementById("node-detail"),
  tasksSection: document.getElementById("tasks-section"),
  configDialog: document.getElementById("node-config-dialog"),
  configForm: document.getElementById("node-config-form"),
  configNodeList: document.getElementById("config-node-list"),
  formStatus: document.getElementById("node-form-status"),
  deleteNode: document.getElementById("delete-node"),
  testNode: document.getElementById("test-node"),
  saveNode: document.getElementById("save-node")
};

const state = {
  pageNum: 1,
  totalPages: 1,
  selectedNodeID: "",
  snapshot: null,
  tasksLoaded: false,
  polling: false,
  taskActions: new Set(),
  configuredNodes: [],
  editingNode: null,
  formDirty: false,
  nodeBusy: false
};
let taskRequestGeneration = 0;

const statusLabels = {
  queued: "排队",
  running: "运行中",
  succeeded: "成功",
  failed: "失败",
  cancelled: "已取消"
};
const healthLabels = { healthy: "健康", unhealthy: "连接失败", unknown: "未知" };
const runtimeLabels = { running: "运行中", idle: "空闲", unknown: "状态未知" };
const phaseLabels = { dispatching: "提交中", recovering: "恢复中", retrying: "重试中", cancelling: "中止中" };

function makeElement(tag, className, text) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
}

function statusPill(value, label) {
  return makeElement("span", `status-pill status-${value || "unknown"}`, label || statusLabels[value] || "未知");
}

function localTime(seconds) {
  if (!Number.isFinite(seconds) || seconds <= 0) return "--";
  return new Date(seconds * 1000).toLocaleString("zh-CN", { hour12: false });
}

function duration(seconds) {
  if (!Number.isFinite(seconds) || seconds < 0) return "--";
  const hours = Math.floor(seconds / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  const remaining = Math.floor(seconds % 60);
  return hours > 0
    ? `${String(hours).padStart(2, "0")}:${String(minutes).padStart(2, "0")}:${String(remaining).padStart(2, "0")}`
    : `${String(minutes).padStart(2, "0")}:${String(remaining).padStart(2, "0")}`;
}

function handleUnauthorized(response) {
  if (response.status !== 401) return false;
  window.location.replace("/manager/login");
  return true;
}

async function requestJSON(url, options = {}) {
  const headers = new Headers(options.headers || {});
  headers.set("Accept", "application/json");
  const response = await fetch(url, { ...options, headers });
  if (handleUnauthorized(response)) throw new Error("unauthorized");
  if (!response.ok) {
    let payload = null;
    try { payload = await response.json(); } catch (_) { payload = null; }
    const error = new Error(payload?.error?.message || `HTTP ${response.status}`);
    error.status = response.status;
    error.payload = payload;
    throw error;
  }
  if (response.status === 204) return null;
  return response.json();
}

function setFreshness(snapshot, failed) {
  elements.freshness.className = "freshness";
  if (failed) {
    elements.freshness.classList.add(state.snapshot ? "stale" : "error");
    elements.freshness.textContent = state.snapshot ? "刷新失败 · 显示缓存" : "数据获取失败";
    return;
  }
  const updatedAt = Number(snapshot.updated_at);
  if (updatedAt <= 0) {
    elements.freshness.classList.add("stale");
    elements.freshness.textContent = "暂无采集数据";
    return;
  }
  const staleAfter = Math.max(1, Number(snapshot.stale_after_seconds) || 15);
  const isStale = Date.now() / 1000 - updatedAt > staleAfter;
  if (isStale) elements.freshness.classList.add("stale");
  elements.freshness.textContent = `${isStale ? "缓存数据" : "最后更新"} ${localTime(updatedAt)}`;
}

function updateUpstreamOptions(upstreams) {
  const selected = elements.upstreamFilter.value;
  const options = [makeElement("option", "", "全部")];
  options[0].value = "";
  upstreams.forEach((upstream) => {
    const option = makeElement("option", "", upstream.id || "未命名实例");
    option.value = upstream.id || "";
    options.push(option);
  });
  elements.upstreamFilter.replaceChildren(...options);
  if (upstreams.some((item) => item.id === selected)) elements.upstreamFilter.value = selected;
}

function renderHealthSummary(summary) {
  const healthy = makeElement("span", "summary-healthy", `● ${Number(summary.healthy) || 0} 个健康`);
  const unhealthy = makeElement("span", "summary-unhealthy", `● ${Number(summary.unhealthy) || 0} 个异常`);
  const unknown = makeElement("span", "summary-unknown", `● ${Number(summary.unknown) || 0} 个未知`);
  elements.healthSummary.replaceChildren(healthy, unhealthy, unknown);
}

function nodeStatusText(node) {
  if (node.applying) return "配置应用中";
  if (node.enabled === false) return "已停用";
  return `${healthLabels[node.health] || "未知"} · ${runtimeLabels[node.runtime] || "状态未知"}`;
}

function renderNodes(upstreams) {
  if (!upstreams.length) {
    state.selectedNodeID = "";
    elements.nodeList.replaceChildren(makeElement("p", "inline-state", "暂无私有实例"));
    elements.nodeDetail.replaceChildren(makeElement("div", "detail-placeholder", "暂无节点详情"));
    return;
  }
  if (!upstreams.some((item) => item.id === state.selectedNodeID)) state.selectedNodeID = upstreams[0].id;
  const buttons = upstreams.map((node) => {
    const button = makeElement("button", `node-button ${node.health || "unknown"}${node.enabled === false ? " disabled" : ""}${node.id === state.selectedNodeID ? " active" : ""}`);
    button.type = "button";
    button.append(
      makeElement("span", "node-name", node.id || "未命名实例"),
      makeElement("span", `node-state summary-${node.health || "unknown"}`, `● ${nodeStatusText(node)}`),
      makeElement("span", "node-meta", node.private_queue == null ? "私有队列未知" : `私有队列 ${node.private_queue}`)
    );
    button.addEventListener("click", () => {
      state.selectedNodeID = node.id;
      renderNodes(upstreams);
    });
    return button;
  });
  elements.nodeList.replaceChildren(...buttons);
  renderNodeDetail(upstreams.find((item) => item.id === state.selectedNodeID));
}

function metric(name, rawValue) {
  const value = typeof rawValue === "number" && Number.isFinite(rawValue) ? Math.max(0, Math.min(100, rawValue)) : null;
  const box = makeElement("div", "metric");
  const line = makeElement("div", "metric-line");
  line.append(makeElement("span", "muted", name), makeElement("strong", "", value == null ? "--" : `${Math.round(value)}%`));
  const bar = makeElement("div", `progress${value != null && value >= 90 ? " danger" : value != null && value >= 75 ? " warning" : ""}`);
  const fill = makeElement("span");
  fill.style.width = `${value == null ? 0 : value}%`;
  bar.append(fill);
  box.append(line, bar);
  return box;
}

function renderCurrentTask(node) {
  const box = makeElement("div", "task-box");
  box.append(makeElement("span", "box-label", "当前任务"));
  if (!node.current_task) {
    box.append(makeElement("span", "muted", node.runtime === "running" ? "任务信息采集中" : "当前无运行任务"));
    return box;
  }
  const line = makeElement("div", "task-primary");
  line.append(makeElement("span", "task-id", node.current_task.id || "--"));
  const elapsed = node.current_task.started_at > 0 ? duration(Math.max(0, Date.now() / 1000 - node.current_task.started_at)) : "--";
  line.append(statusPill(node.current_task.status, `${statusLabels[node.current_task.status] || node.current_task.status || "未知"} · ${elapsed}`));
  box.append(line);
  return box;
}

function renderQueue(node) {
  const box = makeElement("div", "task-box");
  box.append(makeElement("span", "box-label", "私有服务队列"));
  box.append(makeElement("strong", "queue-value", node.private_queue == null ? "队列未知" : `${node.private_queue} 个任务`));
  const error = node.last_error;
  box.append(makeElement("p", `error-summary${error ? " has-error" : ""}`, error ? `最近错误：${error.code || "未知错误"} · ${error.summary || "无摘要"}` : "最近错误：无"));
  return box;
}

function renderRecent(node) {
  const recent = makeElement("section", "recent");
  const head = makeElement("div", "recent-head");
  head.append(makeElement("h3", "", `${node.id || "节点"} 最近一条已结束任务`));
  const filterButton = makeElement("button", "record-button", "查看完整记录");
  filterButton.type = "button";
  filterButton.addEventListener("click", () => {
    elements.upstreamFilter.value = node.id || "";
    state.pageNum = 1;
    loadTasks();
    elements.tasksSection.scrollIntoView({ behavior: "smooth", block: "start" });
  });
  head.append(filterButton);
  recent.append(head);
  if (!node.latest_finished_task) {
    recent.append(makeElement("p", "inline-state", "暂无已结束任务"));
    return recent;
  }
  const scroll = makeElement("div", "table-scroll");
  const labels = makeElement("div", "recent-row head");
  ["任务 ID", "客户", "状态", "耗时", "完成时间"].forEach((label) => labels.append(makeElement("span", "", label)));
  const item = node.latest_finished_task;
  const row = makeElement("div", "recent-row");
  row.append(
    makeElement("span", "task-id", item.id || "--"),
    makeElement("span", "", item.api_key_id || "--"),
    statusPill(item.status),
    makeElement("span", "", duration(item.duration_seconds)),
    makeElement("span", "", localTime(item.finished_at))
  );
  scroll.append(labels, row);
  recent.append(scroll);
  return recent;
}

function renderNodeDetail(node) {
  if (!node) return;
  const head = makeElement("div", "detail-head");
  const identity = makeElement("div");
  identity.append(
    makeElement("h3", "", node.id || "未命名实例"),
    makeElement("div", "detail-subtitle", `${node.address || "地址未知"} · 最后检查 ${localTime(node.checked_at)}`)
  );
  head.append(identity, statusPill(node.enabled === false ? "idle" : node.applying ? "queued" : node.health, nodeStatusText(node)));
  const metrics = makeElement("div", "metrics");
  metrics.append(metric("CPU", node.cpu_percent), metric("内存", node.memory_percent), metric("GPU", node.gpu_percent), metric("显存", node.vram_percent));
  const tasks = makeElement("div", "node-tasks");
  tasks.append(renderCurrentTask(node), renderQueue(node));
  elements.nodeDetail.replaceChildren(head, metrics, tasks, renderRecent(node));
}

function renderSnapshot(snapshot) {
  const upstreams = Array.isArray(snapshot.upstreams) ? snapshot.upstreams : [];
  renderHealthSummary(snapshot.summary || {});
  updateUpstreamOptions(upstreams);
  renderNodes(upstreams);
  setFreshness(snapshot, false);
}

function taskQuery() {
  const params = new URLSearchParams({ page_num: String(state.pageNum), page_size: elements.pageSize.value });
  if (elements.statusFilter.value) params.set("status", elements.statusFilter.value);
  if (elements.upstreamFilter.value) params.set("upstream_id", elements.upstreamFilter.value);
  const search = elements.searchFilter.value.trim();
  if (search) params.set("search", search);
  return params;
}

function renderTasks(response) {
  const items = Array.isArray(response.items) ? response.items : [];
  const total = Number(response.total) || 0;
  const pageSize = Number(response.page_size) || Number(elements.pageSize.value) || 10;
  state.totalPages = Math.max(1, Math.ceil(total / pageSize));
  state.pageNum = Math.min(Math.max(1, Number(response.page_num) || state.pageNum), state.totalPages);
  elements.taskTotal.textContent = `共 ${total} 条`;
  elements.pageSummary.textContent = `第 ${state.pageNum} / ${state.totalPages} 页`;
  elements.previousPage.disabled = state.pageNum <= 1;
  elements.nextPage.disabled = state.pageNum >= state.totalPages;
  if (!items.length) {
    elements.taskRows.replaceChildren(makeElement("p", "table-state", "暂无任务"));
    return;
  }
  const rows = items.map((item) => {
    const row = makeElement("div", "task-row");
    row.setAttribute("role", "row");
    const phaseLabel = phaseLabels[item.phase];
    const actions = makeElement("span", "task-actions");
    if (item.video_url) {
      const play = makeElement("button", "task-action play-action", "播放");
      play.type = "button";
      play.title = "播放生成视频";
      play.addEventListener("click", () => window.open(item.video_url, "_blank", "noopener,noreferrer"));
      actions.append(play);
    }
    if (item.can_cancel) {
      const cancel = makeElement("button", "task-action cancel-action", "中止");
      cancel.type = "button";
      cancel.title = "中止任务";
      cancel.disabled = state.taskActions.has(item.id);
      cancel.addEventListener("click", () => runTaskAction(item, "cancel", cancel));
      actions.append(cancel);
    }
    if (item.can_delete) {
      const remove = makeElement("button", "task-action delete-action", "删除");
      remove.type = "button";
      remove.title = "删除任务记录";
      remove.disabled = state.taskActions.has(item.id);
      remove.addEventListener("click", () => runTaskAction(item, "delete", remove));
      actions.append(remove);
    }
    if (!actions.childElementCount) actions.append(makeElement("span", "muted", "--"));
    row.append(
      makeElement("span", "", item.id || "--"),
      makeElement("span", "", item.api_key_id || "--"),
      statusPill(item.status, phaseLabel || statusLabels[item.status]),
      makeElement("span", "", item.upstream_id || "--"),
      makeElement("span", "", item.scenario || "--"),
      makeElement("span", "", item.resolution || "--"),
      makeElement("span", "", duration(item.duration_seconds)),
      makeElement("span", "", localTime(item.created_at)),
      actions
    );
    return row;
  });
  elements.taskRows.replaceChildren(...rows);
}

async function runTaskAction(item, action, button) {
  const label = action === "cancel" ? "中止" : "删除";
  if (!window.confirm(`确认${label}任务 ${item.id}？`)) return;
  state.taskActions.add(item.id);
  button.disabled = true;
  try {
    const url = action === "cancel" ? `/manager/api/tasks/${encodeURIComponent(item.id)}/cancel` : `/manager/api/tasks/${encodeURIComponent(item.id)}`;
    await requestJSON(url, { method: action === "cancel" ? "POST" : "DELETE" });
    await Promise.all([loadSnapshot(), loadTasks()]);
  } catch (error) {
    if (error.message !== "unauthorized") window.alert(`${label}任务失败，请刷新后重试`);
  } finally {
    state.taskActions.delete(item.id);
    button.disabled = false;
  }
}

async function loadSnapshot() {
  try {
    const snapshot = await requestJSON("/manager/api/snapshot");
    state.snapshot = snapshot;
    renderSnapshot(snapshot);
  } catch (error) {
    if (error.message !== "unauthorized") setFreshness(state.snapshot || {}, true);
  }
}

async function loadTasks() {
	const requestGeneration = ++taskRequestGeneration;
  if (!state.tasksLoaded) elements.taskRows.replaceChildren(makeElement("p", "table-state", "正在加载任务..."));
  try {
    const response = await requestJSON(`/manager/api/tasks?${taskQuery().toString()}`);
	if (requestGeneration !== taskRequestGeneration) return;
    state.tasksLoaded = true;
    renderTasks(response);
  } catch (error) {
	if (requestGeneration !== taskRequestGeneration) return;
    if (error.message === "unauthorized") return;
    if (state.tasksLoaded) {
      elements.taskTotal.textContent = `${elements.taskTotal.textContent.replace(" · 缓存", "")} · 缓存`;
    } else {
      elements.taskRows.replaceChildren(makeElement("p", "table-state error", "任务加载失败，请稍后重试"));
    }
  }
}

function formField(name) {
  return elements.configForm.elements.namedItem(name);
}

function setNodeBusy(busy) {
  state.nodeBusy = busy;
  elements.configForm.querySelectorAll("input, button").forEach((control) => { control.disabled = busy; });
  formField("id").disabled = busy || Boolean(state.editingNode);
  document.getElementById("new-node").disabled = busy;
  document.getElementById("close-node-config").disabled = busy;
}

function setNodeFormStatus(message, kind = "") {
  elements.formStatus.className = `form-status${kind ? ` ${kind}` : ""}`;
  elements.formStatus.textContent = message;
}

function confirmDiscard() {
  return !state.formDirty || window.confirm("当前节点配置尚未保存，确认放弃修改？");
}

function resetNodeForm() {
  elements.configForm.reset();
  formField("version").value = "";
  formField("id").disabled = false;
  elements.deleteNode.hidden = true;
  state.editingNode = null;
  state.formDirty = false;
  setNodeFormStatus("");
}

function fillNodeForm(node) {
  elements.configForm.reset();
  ["id", "base_url", "jobs_base_url", "public_base_url", "health_path", "submit_api_name", "check_api_name", "poll_interval", "request_timeout", "version"].forEach((name) => {
    formField(name).value = node[name] ?? "";
  });
  formField("enabled").checked = Boolean(node.enabled);
  formField("id").disabled = true;
  elements.deleteNode.hidden = false;
  state.editingNode = node;
  state.formDirty = false;
  setNodeFormStatus("");
}

function renderConfiguredNodes() {
  if (!state.configuredNodes.length) {
    elements.configNodeList.replaceChildren(makeElement("p", "inline-state", "暂无节点"));
    return;
  }
  const controls = state.configuredNodes.map((node) => {
    const selected = state.editingNode?.id === node.id;
    const button = makeElement("button", `config-node-button${selected ? " active" : ""}`);
    button.type = "button";
    button.append(
      makeElement("strong", "", node.id),
      makeElement("span", node.enabled ? "summary-healthy" : "muted", node.enabled ? "已启用" : "已停用")
    );
    button.addEventListener("click", () => {
      if (selected || !confirmDiscard()) return;
      fillNodeForm(node);
      renderConfiguredNodes();
    });
    return button;
  });
  elements.configNodeList.replaceChildren(...controls);
}

async function loadConfiguredNodes(selectedID = "") {
  elements.configNodeList.replaceChildren(makeElement("p", "inline-state", "正在加载节点..."));
  const response = await requestJSON("/manager/api/nodes");
  state.configuredNodes = Array.isArray(response.items) ? response.items : [];
  const selected = state.configuredNodes.find((node) => node.id === selectedID)
    || state.configuredNodes.find((node) => node.id === state.editingNode?.id)
    || state.configuredNodes[0];
  if (selected) fillNodeForm(selected); else resetNodeForm();
  renderConfiguredNodes();
}

function nodePayload(includeVersion) {
  const payload = {
    id: formField("id").value.trim(),
    base_url: formField("base_url").value.trim(),
    jobs_base_url: formField("jobs_base_url").value.trim(),
    public_base_url: formField("public_base_url").value.trim(),
    health_path: formField("health_path").value.trim(),
    submit_api_name: formField("submit_api_name").value.trim(),
    check_api_name: formField("check_api_name").value.trim(),
    poll_interval: formField("poll_interval").value.trim(),
    request_timeout: formField("request_timeout").value.trim(),
    enabled: formField("enabled").checked
  };
  if (includeVersion) payload.version = Number(formField("version").value);
  return payload;
}

function validateNodeForm() {
  if (elements.configForm.reportValidity()) return true;
  setNodeFormStatus("请完整填写节点配置", "error");
  return false;
}

function renderNodeProbe(checks) {
  const label = (name, check) => `${name}${check?.ok ? "通过" : `失败（${check?.error_code || "未知错误"}）`}`;
  return `${label("模型服务：", checks?.gradio)}；${label("任务服务：", checks?.jobs)}`;
}

async function reloadConflictedNode(id, message) {
  state.formDirty = false;
  try {
    await loadConfiguredNodes(id);
    setNodeFormStatus(`${message}，已加载最新配置`, "error");
  } catch (_) {
    setNodeFormStatus(`${message}，最新配置加载失败，请关闭后重试`, "error");
  }
}

async function testNodeConnection() {
  if (state.nodeBusy || !validateNodeForm()) return;
  setNodeBusy(true);
  setNodeFormStatus("正在测试连接...");
  try {
    const checks = await requestJSON("/manager/api/nodes/test", {
      method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(nodePayload(false))
    });
    setNodeFormStatus(renderNodeProbe(checks), "success");
  } catch (error) {
    if (error.message !== "unauthorized") setNodeFormStatus(error.payload?.checks ? renderNodeProbe(error.payload.checks) : error.message, "error");
  } finally {
    setNodeBusy(false);
  }
}

async function saveNode(event) {
  event.preventDefault();
  if (state.nodeBusy || !validateNodeForm()) return;
  const editing = state.editingNode;
  const payload = nodePayload(Boolean(editing));
  if (editing && editing.enabled && !payload.enabled && !window.confirm(`确认停用节点 ${editing.id}？`)) return;
  const url = editing ? `/manager/api/nodes/${encodeURIComponent(editing.id)}` : "/manager/api/nodes";
  if (editing) delete payload.id;
  setNodeBusy(true);
  setNodeFormStatus("正在保存...");
  try {
    const saved = await requestJSON(url, {
      method: editing ? "PUT" : "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload)
    });
    state.formDirty = false;
    await Promise.all([loadConfiguredNodes(saved.id), loadSnapshot()]);
    setNodeFormStatus("节点配置已保存", "success");
  } catch (error) {
    if (error.message !== "unauthorized" && error.status === 409 && editing) {
      await reloadConflictedNode(editing.id, error.message);
    } else if (error.message !== "unauthorized") {
      setNodeFormStatus(error.message, "error");
    }
  } finally {
    setNodeBusy(false);
  }
}

async function deleteConfiguredNode() {
  const node = state.editingNode;
  if (!node || state.nodeBusy || !window.confirm(`确认删除节点 ${node.id}？`)) return;
  setNodeBusy(true);
  setNodeFormStatus("正在删除...");
  try {
    await requestJSON(`/manager/api/nodes/${encodeURIComponent(node.id)}?version=${encodeURIComponent(node.version)}`, { method: "DELETE" });
    state.formDirty = false;
    await Promise.all([loadConfiguredNodes(), loadSnapshot(), loadTasks()]);
    setNodeFormStatus("节点已删除", "success");
  } catch (error) {
    if (error.message !== "unauthorized" && error.status === 409) {
      await reloadConflictedNode(node.id, error.message);
    } else if (error.message !== "unauthorized") {
      setNodeFormStatus(error.message, "error");
    }
  } finally {
    setNodeBusy(false);
  }
}

async function openNodeConfiguration() {
  if (!elements.configDialog.open) elements.configDialog.showModal();
  setNodeBusy(true);
  try {
    await loadConfiguredNodes();
  } catch (error) {
    if (error.message !== "unauthorized") setNodeFormStatus("节点配置加载失败", "error");
  } finally {
    setNodeBusy(false);
  }
}

function closeNodeConfiguration() {
  if (state.nodeBusy || !confirmDiscard()) return;
  elements.configDialog.close();
}

async function poll() {
  if (state.polling) return;
  state.polling = true;
  try {
    await Promise.all([loadSnapshot(), loadTasks()]);
  } finally {
    state.polling = false;
  }
}

function resetAndLoadTasks() {
  state.pageNum = 1;
  loadTasks();
}

elements.statusFilter.addEventListener("change", resetAndLoadTasks);
elements.upstreamFilter.addEventListener("change", resetAndLoadTasks);
elements.pageSize.addEventListener("change", resetAndLoadTasks);
elements.previousPage.addEventListener("click", () => { if (state.pageNum > 1) { state.pageNum -= 1; loadTasks(); } });
elements.nextPage.addEventListener("click", () => { if (state.pageNum < state.totalPages) { state.pageNum += 1; loadTasks(); } });
let searchTimer;
elements.searchFilter.addEventListener("input", () => {
  window.clearTimeout(searchTimer);
  searchTimer = window.setTimeout(resetAndLoadTasks, 350);
});
elements.configForm.addEventListener("input", () => { state.formDirty = true; });
elements.configForm.addEventListener("submit", saveNode);
elements.testNode.addEventListener("click", testNodeConnection);
elements.deleteNode.addEventListener("click", deleteConfiguredNode);
document.getElementById("open-node-config").addEventListener("click", openNodeConfiguration);
document.getElementById("close-node-config").addEventListener("click", closeNodeConfiguration);
document.getElementById("new-node").addEventListener("click", () => {
  if (!confirmDiscard()) return;
  resetNodeForm();
  renderConfiguredNodes();
  formField("id").focus();
});
elements.configDialog.addEventListener("cancel", (event) => {
  event.preventDefault();
  closeNodeConfiguration();
});
document.getElementById("logout").addEventListener("click", async () => {
  try { await fetch("/manager/api/session", { method: "DELETE" }); } finally { window.location.replace("/manager/login"); }
});

poll();
window.setInterval(poll, 5000);
