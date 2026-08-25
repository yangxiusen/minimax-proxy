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
  saveNode: document.getElementById("save-node"),
  profileDialog: document.getElementById("profile-config-dialog"),
  profileForm: document.getElementById("profile-form"),
  profileList: document.getElementById("profile-list"),
  profileStatus: document.getElementById("profile-form-status"),
  ratioRows: document.getElementById("ratio-rows"),
  loraRows: document.getElementById("lora-rows"),
  deleteProfile: document.getElementById("delete-profile"),
  cleanupDialog: document.getElementById("cleanup-dialog"),
  cleanupPreview: document.getElementById("cleanup-preview"),
  cleanupProgress: document.getElementById("cleanup-progress"),
  cleanupFormStatus: document.getElementById("cleanup-form-status"),
  apiKeyDialog: document.getElementById("api-key-dialog"),
  apiKeyList: document.getElementById("api-key-list"),
  apiKeyCount: document.getElementById("api-key-count"),
  apiKeyWarning: document.getElementById("api-key-warning"),
  apiKeyForm: document.getElementById("api-key-form"),
  apiKeyName: document.getElementById("api-key-name"),
  apiKeyStatus: document.getElementById("api-key-status"),
  apiKeySecretDialog: document.getElementById("api-key-secret-dialog"),
  apiKeySecretTitle: document.getElementById("api-key-secret-title"),
  apiKeySecretDescription: document.getElementById("api-key-secret-description"),
  apiKeySecret: document.getElementById("api-key-secret"),
  apiKeyCopyStatus: document.getElementById("api-key-copy-status"),
  videoPlayerDialog: document.getElementById("video-player-dialog"),
  videoPlayerTitle: document.getElementById("video-player-title"),
  videoPlayerStatus: document.getElementById("video-player-status"),
  videoPlayer: document.getElementById("video-player"),
  taskDetailDialog: document.getElementById("task-detail-dialog"),
  taskDetailTitle: document.getElementById("task-detail-title"),
  taskDetailStatus: document.getElementById("task-detail-status"),
  taskDetailBody: document.getElementById("task-detail-body"),
  officialNodeFields: document.getElementById("official-node-fields"),
  storageDialog: document.getElementById("object-storage-dialog"),
  storageForm: document.getElementById("object-storage-form"),
  storageStatus: document.getElementById("object-storage-status"),
  testObjectStorage: document.getElementById("test-object-storage"),
  saveObjectStorage: document.getElementById("save-object-storage")
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
  nodeBusy: false,
  profiles: [],
  profileDetail: null,
  profileBusy: false,
  profileFormDirty: false,
  profileTemplateID: "",
  cleanupPreview: null,
  cleanupID: "",
  cleanupTimer: null,
  apiKeys: [],
  apiKeyBusy: false,
  visibleAPIKey: null,
  storageConfig: null,
  storageBusy: false
};
let taskRequestGeneration = 0;
let apiKeyRequestGeneration = 0;
const apiKeysPath = "/manager/api/api-keys";

const statusLabels = {
  queued: "排队",
  running: "运行中",
  succeeded: "成功",
  failed: "失败",
  cancelled: "已取消"
};
const healthLabels = { healthy: "健康", unhealthy: "连接失败", unknown: "未知" };
const runtimeLabels = { running: "运行中", idle: "空闲", unknown: "状态未知" };
const phaseLabels = { dispatching: "提交中", recovering: "恢复中", retrying: "重试中", waiting: "等待实例", cancelling: "中止中" };

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
  if (node.protocol_version === "minimax-v2") {
    if (node.health === "healthy") return "连接正常";
    if (node.health === "unhealthy") return "连接异常";
    return "等待检查";
  }
  return `${healthLabels[node.health] || "未知"} · ${runtimeLabels[node.runtime] || "状态未知"}`;
}

function nodeCapacityText(node) {
  const active = Math.max(0, Math.trunc(Number(node.active_tasks) || 0));
  const maximum = Math.max(1, Math.trunc(Number(node.max_concurrency) || 1));
  return `${active} / ${maximum}`;
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
      makeElement("span", "node-meta", node.protocol_version === "minimax-v2" ? `官方节点 · ${nodeCapacityText(node)}` : node.private_queue == null ? "私有队列未知" : `私有队列 ${node.private_queue}`)
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
  if (node.protocol_version === "minimax-v2") {
    const capacity = makeElement("div", "official-capacity");
    capacity.append(makeElement("span", "box-label", "运行任务"), makeElement("strong", "official-capacity-value", nodeCapacityText(node)));
    elements.nodeDetail.replaceChildren(head, capacity);
    return;
  }
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
    let phaseLabel = phaseLabels[item.phase];
    if (item.result_delivery_status === "pending" || item.result_delivery_status === "uploading") phaseLabel = "上传结果";
    if (item.result_delivery_status === "failed") phaseLabel = "上传失败";
    const actions = makeElement("span", "task-actions");
    const view = makeElement("button", "task-action", "查看");
    view.type = "button";
    view.title = "查看用户提交内容";
    view.disabled = state.taskActions.has(item.id);
    view.addEventListener("click", () => openTaskDetail(item));
    actions.append(view);
    if (item.video_url) {
      const play = makeElement("button", "task-action play-action", "播放");
      play.type = "button";
      play.title = "播放生成视频";
      play.addEventListener("click", () => openVideoPlayer(item));
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
    if (item.can_retry_upload) {
      const retry = makeElement("button", "task-action retry-action", "重新上传");
      retry.type = "button";
      retry.title = item.result_upload_error?.summary || "重新上传结果";
      retry.disabled = state.taskActions.has(item.id);
      retry.addEventListener("click", () => retryResultUpload(item, retry));
      actions.append(retry);
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

async function retryResultUpload(item, button) {
  state.taskActions.add(item.id);
  button.disabled = true;
  button.textContent = "提交中";
  try {
    await requestJSON(`/manager/api/tasks/${encodeURIComponent(item.id)}/result-upload/retry`, { method: "POST" });
    await loadTasks();
  } catch (error) {
    if (error.message !== "unauthorized") window.alert(error.payload?.error?.message || "重新上传失败，请刷新后重试");
  } finally {
    state.taskActions.delete(item.id);
    button.disabled = false;
    button.textContent = "重新上传";
  }
}

async function runTaskAction(item, action, button) {
  const label = action === "cancel" ? "中止" : "删除";
  const message = action === "delete"
    ? `确认物理删除任务 ${item.id}？该操作会删除数据库记录和本地临时输入文件，不可恢复。`
    : `确认${label}任务 ${item.id}？`;
  if (!window.confirm(message)) return;
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
  elements.configForm.querySelectorAll("input, button, select").forEach((control) => { control.disabled = busy; });
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
  formField("api_key").required = true;
  elements.deleteNode.hidden = true;
  state.editingNode = null;
  state.formDirty = false;
  syncNodeProtocolFields();
  setNodeFormStatus("");
}

function fillNodeForm(node) {
  elements.configForm.reset();
  ["id", "service_url", "protocol_version", "poll_interval", "request_timeout", "version"].forEach((name) => {
    formField(name).value = node[name] ?? "";
  });
  formField("api_key").value = "";
  formField("api_key").required = false;
  formField("enabled").checked = Boolean(node.enabled);
  formField("upstream_model").value = node.upstream_model || "MiniMax-H3";
  formField("max_concurrency").value = Number(node.max_concurrency) || 1;
  formField("replace_result_url").checked = Boolean(node.replace_result_url);
  formField("id").disabled = true;
  elements.deleteNode.hidden = false;
  state.editingNode = node;
  state.formDirty = false;
  syncNodeProtocolFields();
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
      makeElement("span", node.enabled ? "summary-healthy" : "muted", node.enabled ? "已启用" : "已停用"),
      makeElement("span", "muted", node.protocol_version === "minimax-v2" ? `${node.upstream_model || "MiniMax-H3"} · ${node.active_tasks || 0} / ${node.max_concurrency || 1}` : "内部节点 · 0 / 1")
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
    service_url: formField("service_url").value.trim(),
    protocol_version: formField("protocol_version").value,
    poll_interval: formField("poll_interval").value.trim(),
    request_timeout: formField("request_timeout").value.trim(),
    enabled: formField("enabled").checked
  };
  if (payload.protocol_version === "minimax-v2") {
    payload.upstream_model = formField("upstream_model").value.trim();
    payload.max_concurrency = Number(formField("max_concurrency").value);
    payload.replace_result_url = formField("replace_result_url").checked;
  }
  const apiKey = formField("api_key").value;
  if (apiKey) payload.api_key = apiKey;
  if (includeVersion) payload.version = Number(formField("version").value);
  return payload;
}

function syncNodeProtocolFields() {
  const official = formField("protocol_version").value === "minimax-v2";
  elements.officialNodeFields.hidden = !official;
  elements.officialNodeFields.querySelectorAll("input").forEach((input) => { input.disabled = !official || state.nodeBusy; });
  formField("upstream_model").required = official;
  formField("max_concurrency").required = official;
  const key = formField("api_key");
  key.minLength = official ? 1 : 32;
  key.maxLength = official ? 512 : 32;
  if (official) key.removeAttribute("pattern"); else key.setAttribute("pattern", "[A-Za-z0-9]{32}");
}

function validateNodeForm() {
	const id = formField("id");
	id.value = id.value.trim();
	id.setCustomValidity("");
	if (id.value === "." || id.value === ".." || id.validity.patternMismatch) {
		id.setCustomValidity("节点 ID 仅支持 1 至 64 位字母、数字、点、下划线或短横线");
	}
  if (elements.configForm.reportValidity()) return true;
	setNodeFormStatus(id.validationMessage || "请完整填写节点配置", "error");
  return false;
}

function setVideoPlayerStatus(message, kind = "") {
  elements.videoPlayerStatus.className = `form-status${kind ? ` ${kind}` : ""}`;
  elements.videoPlayerStatus.textContent = message;
}

function resetVideoPlayer() {
  elements.videoPlayer.pause();
  elements.videoPlayer.removeAttribute("src");
  elements.videoPlayer.load();
  elements.videoPlayerTitle.textContent = "任务视频";
  setVideoPlayerStatus("");
}

function closeVideoPlayer() {
  resetVideoPlayer();
  if (elements.videoPlayerDialog.open) elements.videoPlayerDialog.close();
}

function openVideoPlayer(item) {
  if (!item?.video_url) return;
  resetVideoPlayer();
  elements.videoPlayerTitle.textContent = `任务 ${item.id || "--"}`;
  setVideoPlayerStatus("正在加载视频...");
  elements.videoPlayer.src = item.video_url;
  elements.videoPlayerDialog.showModal();
  elements.videoPlayer.load();
  elements.videoPlayer.play().catch(() => {});
}

function apiKeyErrorMessage(error) {
  const type = error.payload?.error?.type;
  if (type === "api_key_name_conflict") return "名称已存在";
  if (type === "api_key_version_conflict") return "配置已变化，已重新加载列表";
  if (type === "key_in_use") return "该密钥仍有关联任务或幂等记录，请停用后保留";
  if (type === "cache_refresh_failed") return "服务暂时无法刷新密钥配置，操作未完成";
  return error.message;
}

function setAPIKeyStatus(message, kind = "") {
  elements.apiKeyStatus.className = `form-status${kind ? ` ${kind}` : ""}`;
  elements.apiKeyStatus.textContent = message;
}

function setAPIKeyBusy(busy) {
  state.apiKeyBusy = busy;
  elements.apiKeyDialog.querySelectorAll("button, input").forEach((control) => { control.disabled = busy; });
}

function renderAPIKeys(enabledCount = state.apiKeys.filter((item) => item.enabled).length) {
  elements.apiKeyCount.textContent = `启用 ${enabledCount} 个`;
  elements.apiKeyWarning.hidden = enabledCount !== 0;
  if (!state.apiKeys.length) {
    elements.apiKeyList.replaceChildren(makeElement("p", "inline-state api-key-empty", "暂无对外 API Key"));
    return;
  }
  elements.apiKeyList.replaceChildren(...state.apiKeys.map((item) => {
    const row = makeElement("div", "api-key-row");
    const details = makeElement("div", "api-key-details");
    details.append(makeElement("strong", "api-key-name", item.name), makeElement("code", "api-key-mask", item.masked_key), statusPill(item.enabled ? "healthy" : "idle", item.enabled ? "已启用" : "已停用"));
    const actions = makeElement("div", "api-key-actions");
    const view = makeElement("button", "", "查看"); view.type = "button"; view.disabled = !item.key; view.title = item.key ? "查看完整密钥" : "该历史密钥没有可恢复的明文，请重新创建";
    const rename = makeElement("button", "", "重命名"); rename.type = "button";
    const toggle = makeElement("button", "", item.enabled ? "停用" : "启用"); toggle.type = "button";
    const remove = makeElement("button", "danger-button", "删除"); remove.type = "button";
    view.addEventListener("click", () => viewStoredAPIKey(item));
    rename.addEventListener("click", () => renameAPIKey(item));
    toggle.addEventListener("click", () => updateAPIKey(item, { name: item.name, enabled: !item.enabled }));
    remove.addEventListener("click", () => deleteAPIKey(item));
    actions.append(view, rename, toggle, remove); row.append(details, actions); return row;
  }));
}

async function copyText(value) {
  try {
    if (!navigator.clipboard?.writeText) throw new Error("clipboard unavailable");
    await navigator.clipboard.writeText(value);
    return true;
  } catch (_) {
    const field = document.createElement("textarea"); field.value = value; field.setAttribute("readonly", ""); field.className = "clipboard-fallback"; document.body.append(field); field.select();
    const copied = document.execCommand("copy"); field.value = ""; field.remove(); return copied;
  }
}

function showAPIKeySecret(key, title, description) {
  state.visibleAPIKey = key;
  elements.apiKeySecretTitle.textContent = title;
  elements.apiKeySecretDescription.textContent = description;
  elements.apiKeySecret.textContent = key;
  elements.apiKeyCopyStatus.textContent = "";
  elements.apiKeySecretDialog.showModal();
}

function closeTaskDetail() {
  if (elements.taskDetailDialog.open) elements.taskDetailDialog.close();
}

async function openTaskDetail(item) {
  if (!item?.id) return;
  elements.taskDetailTitle.textContent = `任务 ${item.id}`;
  elements.taskDetailStatus.className = "form-status";
  elements.taskDetailStatus.textContent = "正在加载请求内容...";
  elements.taskDetailBody.replaceChildren();
  elements.taskDetailDialog.showModal();
  try {
    const detail = await requestJSON(`/manager/api/tasks/${encodeURIComponent(item.id)}`);
    renderTaskDetail(detail);
  } catch (error) {
    if (error.message === "unauthorized") return;
    elements.taskDetailStatus.className = "form-status error";
    elements.taskDetailStatus.textContent = "任务详情加载失败，请稍后重试";
  }
}

function detailRow(label, value) {
  const row = makeElement("div", "task-detail-row");
  row.append(makeElement("span", "muted", label), makeElement("strong", "", value == null || value === "" ? "--" : String(value)));
  return row;
}

function mediaFileURL(taskID, inputID, download = false) {
  const url = `/manager/api/tasks/${encodeURIComponent(taskID)}/inputs/${encodeURIComponent(inputID)}/content`;
  return download ? `${url}?download=1` : url;
}

function mediaFileActions(detail, item) {
  if (!detail?.id || !item?.input_id) return null;
  const actions = makeElement("div", "task-detail-actions");
  const view = makeElement("a", "task-detail-action", "查看");
  view.href = mediaFileURL(detail.id, item.input_id);
  view.target = "_blank";
  view.rel = "noopener";
  const download = makeElement("a", "task-detail-action", "下载");
  download.href = mediaFileURL(detail.id, item.input_id, true);
  download.setAttribute("download", item.file_name || "");
  actions.append(view, download);
  return actions;
}

function renderTaskDetail(detail) {
  elements.taskDetailStatus.textContent = detail.legacy_base64_present ? "历史任务含 Base64，后台已隐藏正文。" : "";
  const summary = makeElement("section", "task-detail-grid");
  summary.append(
    detailRow("状态", statusLabels[detail.status] || detail.status),
    detailRow("方式", detail.scenario),
    detailRow("规格", detail.resolution),
    detailRow("比例", detail.ratio),
    detailRow("时长", `${detail.duration || 0}s`),
    detailRow("创建时间", localTime(detail.created_at))
  );
  const request = detail.request || {};
  const content = Array.isArray(request.content) ? request.content : [];
  const textItems = content.filter((item) => item.type === "text");
  const mediaItems = content.filter((item) => item.type === "image_url" || item.type === "audio_url" || item.type === "video_url");
  const textSection = makeElement("section", "task-detail-section");
  textSection.append(makeElement("h3", "", "文案"));
  if (!textItems.length) {
    textSection.append(makeElement("p", "muted", "无文案"));
  } else {
    textItems.forEach((item) => textSection.append(makeElement("pre", "task-detail-text", item.text || "")));
  }
  const mediaSection = makeElement("section", "task-detail-section");
  mediaSection.append(makeElement("h3", "", "媒体输入"));
  if (!mediaItems.length) {
    mediaSection.append(makeElement("p", "muted", "无媒体输入"));
  } else {
    mediaItems.forEach((item, index) => {
      const box = makeElement("div", "task-detail-media");
      box.append(
        detailRow("序号", index + 1),
        detailRow("类型", item.type),
        detailRow("角色", item.role),
        detailRow("来源", item.source_kind || "url"),
        detailRow("MIME", item.media_type),
        detailRow("文件", item.file_name || item.input_ref || "已隐藏"),
        detailRow("大小", item.size_bytes ? `${item.size_bytes} B` : "--"),
        detailRow("SHA256", item.sha256 ? `${String(item.sha256).slice(0, 16)}...` : "--")
      );
      const actions = mediaFileActions(detail, item);
      if (actions) box.append(actions);
      mediaSection.append(box);
    });
  }
  const configSection = makeElement("section", "task-detail-section");
  configSection.append(makeElement("h3", "", "设置"));
  configSection.append(makeElement("pre", "task-detail-json", JSON.stringify(detail.config || {
    model: detail.model, resolution: detail.resolution, ratio: detail.ratio, duration: detail.duration
  }, null, 2)));
  const deliverySection = makeElement("section", "task-detail-section");
  deliverySection.append(makeElement("h3", "", "结果交付"));
  deliverySection.append(
    detailRow("状态", detail.result_delivery_status || "not_required"),
    detailRow("上传轮次", detail.result_upload_round || 0),
    detailRow("尝试次数", detail.result_upload_attempts || 0)
  );
  if (detail.result_upload_error) deliverySection.append(detailRow("失败原因", `${detail.result_upload_error.code}：${detail.result_upload_error.summary || "上传失败"}`));
  elements.taskDetailBody.replaceChildren(summary, textSection, mediaSection, deliverySection, configSection);
}

function storageField(name) { return elements.storageForm.elements.namedItem(name); }

function setStorageStatus(message, kind = "") {
  elements.storageStatus.className = `form-status${kind ? ` ${kind}` : ""}`;
  elements.storageStatus.textContent = message;
}

function setStorageBusy(busy) {
  state.storageBusy = busy;
  elements.storageForm.querySelectorAll("input, button").forEach((control) => { control.disabled = busy; });
  document.getElementById("close-object-storage").disabled = busy;
}

function fillStorageForm(config) {
  elements.storageForm.reset();
  state.storageConfig = config?.configured ? config : null;
  storageField("version").value = config?.version || "";
  storageField("bucket_name").value = config?.bucket_name || "";
  storageField("file_host").value = config?.file_host || "";
  storageField("public_base_url").value = config?.public_base_url || "";
  storageField("request_timeout").value = config?.request_timeout || "30m";
  storageField("public_key").value = "";
  storageField("private_key").value = "";
}

async function openObjectStorage() {
  elements.storageDialog.showModal();
  setStorageBusy(true);
  setStorageStatus("正在加载配置...");
  try {
    const config = await requestJSON("/manager/api/object-storage");
    fillStorageForm(config);
    setStorageStatus(config.configured ? `已配置，最近测试：${config.last_test_status || "未测试"}` : "尚未配置对象存储");
  } catch (error) {
    if (error.message !== "unauthorized") setStorageStatus(error.message, "error");
  } finally { setStorageBusy(false); }
}

function storagePayload(forTest = false) {
  if (forTest && state.storageConfig && !storageField("public_key").value && !storageField("private_key").value) {
    return { use_stored_config: true, version: Number(storageField("version").value) };
  }
  const payload = {
    provider: "ucloud-us3",
    bucket_name: storageField("bucket_name").value.trim(),
    file_host: storageField("file_host").value.trim(),
    public_base_url: storageField("public_base_url").value.trim(),
    public_key: storageField("public_key").value,
    private_key: storageField("private_key").value,
    request_timeout: storageField("request_timeout").value.trim()
  };
  if (state.storageConfig) {
    payload.version = Number(storageField("version").value);
    if (!payload.private_key) {
      delete payload.private_key;
      payload.use_stored_private_key = true;
    }
    if (!payload.public_key) delete payload.public_key;
  }
  return payload;
}

async function testObjectStorage() {
  if (state.storageBusy || !elements.storageForm.reportValidity()) return;
  setStorageBusy(true); setStorageStatus("正在执行上传与公开读取测试...");
  try {
    await requestJSON("/manager/api/object-storage/test", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(storagePayload(true)) });
    setStorageStatus("连接测试通过", "success");
  } catch (error) {
    if (error.message !== "unauthorized") setStorageStatus(error.message, "error");
  } finally { setStorageBusy(false); }
}

async function saveObjectStorage(event) {
  event.preventDefault();
  if (state.storageBusy || !elements.storageForm.reportValidity()) return;
  setStorageBusy(true); setStorageStatus("正在保存...");
  try {
    const saved = await requestJSON("/manager/api/object-storage", { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(storagePayload(false)) });
    fillStorageForm(saved);
    try {
      await requestJSON("/manager/api/object-storage/test", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ use_stored_config: true, version: saved.version }) });
      state.storageConfig.last_test_status = "passed";
      setStorageStatus("对象存储配置已保存并测试通过", "success");
    } catch (probeError) {
      setStorageStatus(`配置已保存，但连接测试失败：${probeError.message}`, "error");
    }
  } catch (error) {
    if (error.message !== "unauthorized") setStorageStatus(error.message, "error");
  } finally { setStorageBusy(false); }
}

function viewStoredAPIKey(item) {
  if (!item.key) { setAPIKeyStatus("该历史密钥没有可恢复的明文，请重新创建", "error"); return; }
  showAPIKeySecret(item.key, "查看密钥", `${item.name} 的完整密钥，选中文本后可手动复制。`);
}

async function loadAPIKeys() {
  const requestGeneration = ++apiKeyRequestGeneration;
  elements.apiKeyList.replaceChildren(makeElement("p", "inline-state", "正在加载密钥"));
  setAPIKeyStatus("");
  try {
    const response = await requestJSON(apiKeysPath);
    if (requestGeneration !== apiKeyRequestGeneration) return;
    state.apiKeys = response.items || [];
    renderAPIKeys(Number(response.enabled_count) || 0);
  } catch (error) {
    if (error.message !== "unauthorized" && requestGeneration === apiKeyRequestGeneration) {
      elements.apiKeyList.replaceChildren(makeElement("p", "inline-state table-state error", "密钥加载失败"));
      setAPIKeyStatus(apiKeyErrorMessage(error), "error");
    }
  }
}

async function openAPIKeys() {
  elements.apiKeyDialog.showModal(); setAPIKeyBusy(true);
  try { await loadAPIKeys(); } finally { setAPIKeyBusy(false); }
}

function showCreateAPIKey() {
  elements.apiKeyForm.hidden = false; elements.apiKeyName.value = ""; setAPIKeyStatus(""); elements.apiKeyName.focus();
}

async function createAPIKey(event) {
  event.preventDefault(); if (state.apiKeyBusy || !elements.apiKeyForm.reportValidity()) return;
  const name = elements.apiKeyName.value.trim(); if (!name) return;
  setAPIKeyBusy(true); setAPIKeyStatus("正在创建...");
  try {
    let created = await requestJSON(apiKeysPath, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ name }) });
    showAPIKeySecret(created.key, "保存新密钥", "完整密钥已保存，请妥善保管。选中文本后可手动复制。");
    created = null;
    elements.apiKeyForm.hidden = true;
    loadAPIKeys().catch(() => {});
  } catch (error) { if (error.message !== "unauthorized") setAPIKeyStatus(apiKeyErrorMessage(error), "error"); }
  finally { setAPIKeyBusy(false); }
}

async function updateAPIKey(item, changes) {
  if (state.apiKeyBusy) return; setAPIKeyBusy(true);
  try {
    await requestJSON(`${apiKeysPath}/${encodeURIComponent(item.id)}`, { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ ...changes, version: item.version }) });
    await loadAPIKeys(); setAPIKeyStatus("密钥已更新", "success");
  } catch (error) { if (error.message !== "unauthorized") { setAPIKeyStatus(apiKeyErrorMessage(error), "error"); if (error.payload?.error?.type === "api_key_version_conflict") await loadAPIKeys(); } }
  finally { setAPIKeyBusy(false); }
}

function renameAPIKey(item) {
  const name = window.prompt("输入新的密钥名称", item.name); if (name === null || !name.trim() || name.trim() === item.name) return;
  updateAPIKey(item, { name: name.trim(), enabled: item.enabled });
}

async function deleteAPIKey(item) {
  if (state.apiKeyBusy || !window.confirm(`确认删除密钥 ${item.name}（${item.masked_key}）？`)) return;
  setAPIKeyBusy(true);
  try { await requestJSON(`${apiKeysPath}/${encodeURIComponent(item.id)}?version=${encodeURIComponent(item.version)}`, { method: "DELETE" }); await loadAPIKeys(); setAPIKeyStatus("密钥已删除", "success"); }
  catch (error) { if (error.message !== "unauthorized") { setAPIKeyStatus(apiKeyErrorMessage(error), "error"); if (error.payload?.error?.type === "api_key_version_conflict") await loadAPIKeys(); } }
  finally { setAPIKeyBusy(false); }
}

function clearVisibleAPIKey() {
  const selection = window.getSelection(); if (selection) selection.removeAllRanges();
  elements.apiKeySecret.textContent = "";
  elements.apiKeyCopyStatus.textContent = "";
  state.visibleAPIKey = null;
}

function closeVisibleAPIKey() { clearVisibleAPIKey(); elements.apiKeySecretDialog.close(); }

async function copyVisibleAPIKey() {
  if (!state.visibleAPIKey) return;
  elements.apiKeyCopyStatus.textContent = await copyText(state.visibleAPIKey) ? "已复制" : "复制失败，请手动选择密钥";
}

function renderNodeProbe(checks) {
  const entries = Array.isArray(checks?.checks) ? checks.checks : [];
  return entries.map((item) => `${item.name}：${item.status === "passed" ? "通过" : `失败（${item.error_code || "未知错误"}）`}`).join("；") || "节点未返回检查结果";
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
    const payload = nodePayload(false);
    if (state.editingNode && !payload.api_key) payload.use_stored_api_key = true;
    const checks = await requestJSON("/manager/api/nodes/test", {
      method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload)
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

const ratios = ["adaptive", "21:9", "16:9", "4:3", "1:1", "3:4", "9:16"];
const ratioDefaults = {
  adaptive: [832, 480], "21:9": [1120, 480], "16:9": [832, 480], "4:3": [640, 480],
  "1:1": [480, 480], "3:4": [480, 640], "9:16": [480, 832]
};
function profileField(name) { return elements.profileForm.elements.namedItem(name); }
function setProfileStatus(message, kind = "") {
  elements.profileStatus.className = `form-status${kind ? ` ${kind}` : ""}`;
  elements.profileStatus.textContent = message;
}
function setProfileBusy(busy) {
  state.profileBusy = busy;
  elements.profileForm.querySelectorAll("input, select, button").forEach((control) => { control.disabled = busy || control.dataset.locked === "true"; });
  document.getElementById("save-profile").disabled = busy;
  elements.deleteProfile.disabled = busy;
  document.getElementById("new-profile").disabled = busy;
  document.getElementById("close-profile-config").disabled = busy;
}
function renderRatioRows(config = null) {
  document.getElementById("ratio-rule").textContent = "同一配置覆盖文生、图生和全能参考；请维护 adaptive 与六种固定比例。";
  const scale = profileField("restoration_enabled").checked ? Number(profileField("restoration_scale").value) : 1;
  const rows = ratios.map((ratio) => {
    const mapping = config?.ratios?.[ratio] || {};
    const defaults = ratioDefaults[ratio];
    const width = Number(mapping.base_width) || defaults[0];
    const height = Number(mapping.base_height) || defaults[1];
    const row = makeElement("div", "ratio-row");
    const label = makeElement("span", "", ratio);
    const widthInput = document.createElement("input"); widthInput.type = "number"; widthInput.min = "256"; widthInput.max = "4096"; widthInput.step = "32"; widthInput.value = String(width); widthInput.dataset.ratio = ratio; widthInput.dataset.dimension = "width";
    const heightInput = document.createElement("input"); heightInput.type = "number"; heightInput.min = "256"; heightInput.max = "4096"; heightInput.step = "32"; heightInput.value = String(height); heightInput.dataset.ratio = ratio; heightInput.dataset.dimension = "height";
    const target = makeElement("span", "ratio-target", `${width * scale} × ${height * scale}`);
    row.append(label, widthInput, heightInput, target);
    return row;
  });
  elements.ratioRows.replaceChildren(...rows);
}
function renderLoRAs(loras = []) {
  const rows = Array.from({ length: 4 }, (_, index) => {
    const row = makeElement("div", "lora-row");
    const name = document.createElement("input"); name.placeholder = `LoRA ${index + 1} 文件名（选填）`; name.value = loras[index]?.name || ""; name.dataset.loraName = String(index);
    const strength = document.createElement("input"); strength.type = "number"; strength.min = "-2"; strength.max = "2"; strength.step = "0.05"; strength.value = String(loras[index]?.strength ?? 1); strength.dataset.loraStrength = String(index);
    row.append(name, strength); return row;
  });
  elements.loraRows.replaceChildren(...rows);
}
function defaultProfileConfig() {
  return {
    resolution: "",
    generation: { model_mode: "high_quality", steps: 8, sage_attention: "auto", cache_mode: "easycache" },
    ratios: Object.fromEntries(ratios.map((ratio) => [ratio, { base_width: ratioDefaults[ratio][0], base_height: ratioDefaults[ratio][1], target_width: ratioDefaults[ratio][0] * 3, target_height: ratioDefaults[ratio][1] * 3 }])),
    loras: [], interpolation: { enabled: true, engine: "rife", scale: 2 }, restoration: { enabled: true, engine: "flashvsr", scale: 3 }
  };
}
function cloneProfileConfig(config) { return JSON.parse(JSON.stringify(config)); }
function profileNameKey(value) { return value.trim().replace(/[A-Z]/g, (letter) => letter.toLowerCase()); }
function validProfileName(value) {
  const name = value.trim();
  return [...name].length >= 1 && [...name].length <= 32 && [...name].every((character) => /[A-Za-z0-9 _-]/.test(character) || /\p{Script=Han}/u.test(character));
}
function renderProfileTemplates(selectedID = "") {
  const field = document.getElementById("profile-template-field");
  const select = profileField("profile_template");
  field.hidden = Boolean(state.profileDetail) || !state.profiles.length;
  select.replaceChildren(...state.profiles.map((profile) => {
    const option = document.createElement("option"); option.value = profile.id; option.textContent = profile.resolution; return option;
  }));
  if (state.profiles.length) select.value = selectedID || state.profileTemplateID || state.profileDetail?.id || state.profiles[0].id;
}
function fillProfile(detail = null, template = null) {
  const config = detail?.config || (template?.config ? cloneProfileConfig(template.config) : defaultProfileConfig());
  elements.profileForm.reset();
  profileField("profile_id").value = detail?.id || "";
  profileField("row_version").value = detail?.row_version || "";
  profileField("resolution").value = detail ? config.resolution : "";
  ["model_mode", "steps", "sage_attention", "cache_mode"].forEach((name) => { profileField(name).value = config.generation[name] ?? ""; });
  profileField("interpolation_enabled").checked = Boolean(config.interpolation.enabled);
  profileField("restoration_enabled").checked = Boolean(config.restoration.enabled);
  profileField("restoration_engine").value = config.restoration.engine || "flashvsr";
  profileField("restoration_scale").value = config.restoration.scale || 1;
  renderRatioRows(config); renderLoRAs(config.loras || []);
  state.profileDetail = detail;
  profileField("resolution").disabled = Boolean(detail);
  profileField("resolution").dataset.locked = detail ? "true" : "false";
  elements.deleteProfile.hidden = !detail;
  state.profileTemplateID = template?.id || "";
  renderProfileTemplates(template?.id || "");
  state.profileFormDirty = false;
  setProfileStatus("");
}
function confirmDiscardProfileChanges() {
  return !state.profileFormDirty || window.confirm("当前模型请求参数尚未保存，确认放弃修改？");
}
function profileConfigPayload() {
  const scale = profileField("restoration_enabled").checked ? Number(profileField("restoration_scale").value) : 1;
  const mappings = {};
  ratios.forEach((ratio) => {
    const width = Number(elements.ratioRows.querySelector(`[data-ratio="${ratio}"][data-dimension="width"]`).value);
    const height = Number(elements.ratioRows.querySelector(`[data-ratio="${ratio}"][data-dimension="height"]`).value);
    mappings[ratio] = { base_width: width, base_height: height, target_width: width * scale, target_height: height * scale };
  });
  const loras = [...elements.loraRows.querySelectorAll("[data-lora-name]")].map((input, index) => ({ name: input.value.trim(), strength: Number(elements.loraRows.querySelector(`[data-lora-strength="${index}"]`).value) })).filter((item) => item.name);
  return {
    resolution: profileField("resolution").value,
    generation: { model_mode: profileField("model_mode").value, steps: Number(profileField("steps").value), sage_attention: profileField("sage_attention").value, cache_mode: profileField("cache_mode").value },
    ratios: mappings, loras,
    interpolation: { enabled: profileField("interpolation_enabled").checked, engine: "rife", scale: 2 },
    restoration: { enabled: profileField("restoration_enabled").checked, engine: profileField("restoration_engine").value, scale: Number(profileField("restoration_scale").value) }
  };
}
function renderProfileList() {
  if (!state.profiles.length) { elements.profileList.replaceChildren(makeElement("p", "inline-state", "暂无配置")); return; }
  elements.profileList.replaceChildren(...state.profiles.map((profile) => {
    const button = makeElement("button", `config-node-button${state.profileDetail?.id === profile.id ? " active" : ""}`);
    button.type = "button"; button.append(makeElement("strong", "", profile.resolution), makeElement("span", "muted", "修改后立即生效"));
    button.addEventListener("click", () => { if (confirmDiscardProfileChanges()) loadProfileDetail(profile.id); }); return button;
  }));
}
async function loadProfiles(selectedID = "") {
  const response = await requestJSON("/manager/api/request-profiles");
  state.profiles = response.items || []; renderProfileList();
  const selected = selectedID || state.profiles[0]?.id;
  if (selected) await loadProfileDetail(selected); else fillProfile();
}
async function loadProfileDetail(id) { const detail = await requestJSON(`/manager/api/request-profiles/${encodeURIComponent(id)}`); fillProfile(detail); renderProfileList(); }
async function saveProfile(event) {
  event.preventDefault(); if (state.profileBusy || !elements.profileForm.reportValidity()) return;
  const name = profileField("resolution").value;
  if (!validProfileName(name)) { setProfileStatus("逻辑分辨率名称应为 1-32 个中文、英文字母、数字、空格、- 或 _", "error"); profileField("resolution").focus(); return; }
  if (!profileField("profile_id").value && state.profiles.some((item) => profileNameKey(item.resolution) === profileNameKey(name))) { setProfileStatus("逻辑分辨率名称已存在", "error"); return; }
  setProfileBusy(true); setProfileStatus("正在保存...");
  try { const id = profileField("profile_id").value; const payload = profileConfigPayload(); if (id) payload.row_version = Number(profileField("row_version").value); const saved = await requestJSON(id ? `/manager/api/request-profiles/${encodeURIComponent(id)}` : "/manager/api/request-profiles", { method: id ? "PUT" : "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload) }); state.profileFormDirty = false; await loadProfiles(saved.id); setProfileStatus("配置已保存并立即生效", "success"); } catch (error) { if (error.message !== "unauthorized") setProfileStatus(error.message, "error"); } finally { setProfileBusy(false); }
}
async function deleteProfile() { if (!state.profileDetail || !window.confirm(`确认删除 ${state.profileDetail.resolution} 配置？`)) return; setProfileBusy(true); try { await requestJSON(`/manager/api/request-profiles/${encodeURIComponent(state.profileDetail.id)}`, { method: "DELETE", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ row_version: state.profileDetail.row_version }) }); state.profileDetail = null; await loadProfiles(); setProfileStatus("配置已删除", "success"); } catch (error) { if (error.message !== "unauthorized") setProfileStatus(error.message, "error"); } finally { setProfileBusy(false); } }
async function openProfileConfiguration() { elements.profileDialog.showModal(); setProfileBusy(true); try { await loadProfiles(); } catch (error) { if (error.message !== "unauthorized") setProfileStatus("配置加载失败", "error"); } finally { setProfileBusy(false); } }

function bytesLabel(value) { const units = ["B", "KiB", "MiB", "GiB", "TiB"]; let size = Number(value) || 0; let index = 0; while (size >= 1024 && index < units.length - 1) { size /= 1024; index += 1; } return `${size.toFixed(index ? 1 : 0)} ${units[index]}`; }
function setCleanupStatus(message, kind = "") { elements.cleanupFormStatus.className = `form-status${kind ? ` ${kind}` : ""}`; elements.cleanupFormStatus.textContent = message; }
async function previewCleanup(event) { event.preventDefault(); setCleanupStatus("正在计算候选文件..."); try { const preview = await requestJSON("/manager/api/artifact-cleanups/preview", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ older_than_days: Number(document.getElementById("cleanup-days").value) }) }); state.cleanupPreview = preview; elements.cleanupPreview.hidden = false; elements.cleanupProgress.hidden = true; document.getElementById("cleanup-count").textContent = String(preview.candidate_count); document.getElementById("cleanup-bytes").textContent = bytesLabel(preview.candidate_bytes); document.getElementById("cleanup-cutoff").textContent = new Date(preview.cutoff_at).toLocaleString("zh-CN", { hour12: false }); document.getElementById("cleanup-node-summary").textContent = (preview.by_node || []).map((item) => `${item.node_id}: ${item.count} 个 / ${bytesLabel(item.bytes)}`).join("；") || "没有候选文件"; document.getElementById("cleanup-confirmation").placeholder = `DELETE ${preview.candidate_count} ARTIFACTS`; setCleanupStatus(preview.candidate_count ? "预览不会删除文件，请核对后确认" : "没有符合条件的视频", "success"); } catch (error) { if (error.message !== "unauthorized") setCleanupStatus(error.message, "error"); } }
async function confirmCleanup() { const preview = state.cleanupPreview; if (!preview) return; const confirmation = document.getElementById("cleanup-confirmation").value.trim(); if (confirmation !== `DELETE ${preview.candidate_count} ARTIFACTS`) { setCleanupStatus("确认文本不匹配", "error"); return; } if (!window.confirm(`确认删除 ${preview.candidate_count} 个物理文件？此操作不可恢复。`)) return; try { const job = await requestJSON("/manager/api/artifact-cleanups", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ preview_token: preview.preview_token, confirmation }) }); state.cleanupID = job.cleanup_id; elements.cleanupPreview.hidden = true; elements.cleanupProgress.hidden = false; setCleanupStatus("清理作业已创建", "success"); await loadCleanupProgress(); } catch (error) { if (error.message !== "unauthorized") setCleanupStatus(error.message, "error"); } }
async function loadCleanupProgress() { if (!state.cleanupID) return; try { const job = await requestJSON(`/manager/api/artifact-cleanups/${encodeURIComponent(state.cleanupID)}`); const completed = Number(job.succeeded_count) + Number(job.failed_count) + Number(job.skipped_count); const percent = job.total_count ? Math.min(100, completed / job.total_count * 100) : 100; document.getElementById("cleanup-status").textContent = job.status; document.getElementById("cleanup-status").className = `status-pill status-${job.status === "succeeded" ? "succeeded" : job.failed_count ? "failed" : "running"}`; document.getElementById("cleanup-progress-bar").style.width = `${percent}%`; document.getElementById("cleanup-progress-summary").textContent = `完成 ${job.succeeded_count}/${job.total_count} · 失败 ${job.failed_count} · 已释放 ${bytesLabel(job.deleted_bytes)}`; document.getElementById("retry-cleanup").hidden = !job.failed_count; const response = await requestJSON(`/manager/api/artifact-cleanups/${encodeURIComponent(state.cleanupID)}/items?limit=100`); document.getElementById("cleanup-items").replaceChildren(...(response.items || []).map((item) => makeElement("div", "cleanup-item", `${item.node_id} · ${item.status} · ${item.artifact_id}${item.last_error_code ? ` · ${item.last_error_code}` : ""}`))); if (!["succeeded", "failed", "partial_failed"].includes(job.status)) { window.clearTimeout(state.cleanupTimer); state.cleanupTimer = window.setTimeout(loadCleanupProgress, 2000); } } catch (error) { if (error.message !== "unauthorized") setCleanupStatus("清理进度加载失败", "error"); } }
async function retryCleanup() { try { await requestJSON(`/manager/api/artifact-cleanups/${encodeURIComponent(state.cleanupID)}/retry`, { method: "POST", headers: { "Content-Type": "application/json" }, body: "{}" }); await loadCleanupProgress(); } catch (error) { if (error.message !== "unauthorized") setCleanupStatus(error.message, "error"); } }

document.getElementById("open-profile-config").addEventListener("click", openProfileConfiguration);
document.getElementById("close-profile-config").addEventListener("click", () => { if (!state.profileBusy && confirmDiscardProfileChanges()) elements.profileDialog.close(); });
elements.profileDialog.addEventListener("cancel", (event) => { event.preventDefault(); if (!state.profileBusy && confirmDiscardProfileChanges()) elements.profileDialog.close(); });
document.getElementById("new-profile").addEventListener("click", () => { if (confirmDiscardProfileChanges()) fillProfile(null, state.profileDetail || state.profiles[0] || null); });
profileField("profile_template").addEventListener("change", () => {
  if (!confirmDiscardProfileChanges()) { renderProfileTemplates(state.profileTemplateID); return; }
  const template = state.profiles.find((item) => item.id === profileField("profile_template").value);
  const currentName = profileField("resolution").value;
  if (template) { fillProfile(null, template); profileField("resolution").value = currentName; }
});
elements.profileForm.addEventListener("input", (event) => { if (event.target.name !== "profile_template") state.profileFormDirty = true; });
profileField("restoration_enabled").addEventListener("change", () => renderRatioRows(profileConfigPayload()));
profileField("restoration_scale").addEventListener("change", () => renderRatioRows(profileConfigPayload()));
elements.profileForm.addEventListener("submit", saveProfile); elements.deleteProfile.addEventListener("click", deleteProfile);
document.getElementById("open-cleanup").addEventListener("click", () => elements.cleanupDialog.showModal());
document.getElementById("close-cleanup").addEventListener("click", () => { window.clearTimeout(state.cleanupTimer); elements.cleanupDialog.close(); });
document.getElementById("cleanup-preview-form").addEventListener("submit", previewCleanup); document.getElementById("confirm-cleanup").addEventListener("click", confirmCleanup); document.getElementById("retry-cleanup").addEventListener("click", retryCleanup);
document.getElementById("open-api-keys").addEventListener("click", openAPIKeys);
document.getElementById("close-api-keys").addEventListener("click", () => { if (!state.apiKeyBusy) elements.apiKeyDialog.close(); });
document.getElementById("new-api-key").addEventListener("click", showCreateAPIKey);
document.getElementById("cancel-api-key").addEventListener("click", () => { elements.apiKeyForm.hidden = true; elements.apiKeyName.value = ""; setAPIKeyStatus(""); });
elements.apiKeyForm.addEventListener("submit", createAPIKey);
document.getElementById("copy-api-key").addEventListener("click", copyVisibleAPIKey);
document.getElementById("close-api-key-secret").addEventListener("click", closeVisibleAPIKey);
document.getElementById("confirm-api-key-saved").addEventListener("click", closeVisibleAPIKey);
elements.apiKeySecretDialog.addEventListener("cancel", (event) => { event.preventDefault(); closeVisibleAPIKey(); });
elements.apiKeySecretDialog.addEventListener("close", clearVisibleAPIKey);
document.getElementById("close-video-player").addEventListener("click", () => closeVideoPlayer());
elements.videoPlayerDialog.addEventListener("cancel", (event) => { event.preventDefault(); closeVideoPlayer(); });
elements.videoPlayerDialog.addEventListener("close", resetVideoPlayer);
elements.videoPlayer.addEventListener("loadstart", () => setVideoPlayerStatus("正在加载视频..."));
elements.videoPlayer.addEventListener("canplay", () => setVideoPlayerStatus(""));
elements.videoPlayer.addEventListener("error", () => setVideoPlayerStatus("视频加载失败，请关闭后重试", "error"));
document.getElementById("close-task-detail").addEventListener("click", () => closeTaskDetail());
elements.taskDetailDialog.addEventListener("cancel", (event) => { event.preventDefault(); closeTaskDetail(); });

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
formField("protocol_version").addEventListener("change", () => { syncNodeProtocolFields(); state.formDirty = true; });
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
document.getElementById("open-object-storage").addEventListener("click", openObjectStorage);
document.getElementById("close-object-storage").addEventListener("click", () => { if (!state.storageBusy) elements.storageDialog.close(); });
elements.storageDialog.addEventListener("cancel", (event) => { if (state.storageBusy) event.preventDefault(); });
elements.storageForm.addEventListener("submit", saveObjectStorage);
elements.testObjectStorage.addEventListener("click", testObjectStorage);
document.getElementById("logout").addEventListener("click", async () => {
  try { await fetch("/manager/api/session", { method: "DELETE" }); } finally { window.location.replace("/manager/login"); }
});

poll();
window.setInterval(poll, 5000);
