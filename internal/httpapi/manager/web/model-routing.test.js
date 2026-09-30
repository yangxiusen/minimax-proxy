"use strict";

const { test } = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");

class Element {
  constructor(tag = "div") {
    this.tag = tag;
    this.value = "";
    this.children = [];
    this.dataset = {};
    this.listeners = {};
    this.checked = false;
    this.parentElement = { hidden: false };
  }
  append(...children) { this.children.push(...children); }
  prepend(...children) { this.children.unshift(...children); }
  replaceChildren(...children) { this.children = children; }
  addEventListener(type, handler) { this.listeners[type] = handler; }
  setAttribute(name, value) { this[name] = value; }
  reportValidity() { return true; }
  showModal() { this.open = true; }
  close() { this.open = false; }
  get options() { return this.children; }
  querySelectorAll(selector) {
    const matches = [];
    const visit = (node) => {
      for (const child of node.children || []) {
        if (selector.split(", ").includes(child.tag) || (selector === "button[data-no-candidates]" && child.tag === "button" && child.dataset.noCandidates)) matches.push(child);
        visit(child);
      }
    };
    visit(this);
    return matches;
  }
  fire(type) { return this.listeners[type]?.({ preventDefault() {} }); }
}

function harness(responder = async () => ({})) {
  const nodes = new Map();
  const get = (id) => {
    if (!nodes.has(id)) nodes.set(id, new Element());
    return nodes.get(id);
  };
  const fields = new Map();
  const field = (name) => {
    if (!fields.has(name)) fields.set(name, new Element(name === "protocol_version" ? "select" : "input"));
    return fields.get(name);
  };
  const state = { nodeBusy: false, formDirty: false };
  const requests = [];
  const ui = {
    state, formField: field,
    elements: { saveNode: new Element("button"), testNode: new Element("button") },
    requestJSON: async (url, options) => { requests.push({ url, options }); return responder(url, options); },
    makeElement: (tag, className, text) => { const node = new Element(tag); node.className = className; node.textContent = text; return node; },
    localTime: (value) => String(value ?? "--"),
    setNodeBusy: (value) => { state.nodeBusy = value; },
    setNodeFormStatus: (value) => { ui.error = value; },
    detailRow: (label, value) => Object.assign(new Element(), { textContent: `${label}:${value}` }),
    loadTasks() {}, renderTaskDetail() {}
  };
  const window = { managerUI: ui, confirm: () => true };
  vm.runInNewContext(fs.readFileSync(`${__dirname}/model-routing.js`, "utf8"), { window, document: { getElementById: get }, URLSearchParams, Date, Set, Map });
  field("protocol_version").value = "tk2sd-v1";
  field("service_url").value = "https://node.example";
  field("api_key").value = "token";
  return { api: window.modelRouting, get, field, ui, requests };
}

const tick = () => new Promise((resolve) => setImmediate(resolve));

test("first discovery selects returned models and empty catalogs disable enabling", async () => {
  const h=harness(async()=>({items:[{model_id:"new-model",enabled:true,present:true}]}));
  h.api.editNode(null);
  assert.equal(h.field("enabled").disabled,true);
  await h.get("discover-models").fire("click");
  assert.equal(h.get("node-model-items").children[0].children[0].checked,true);
  assert.equal(h.field("enabled").disabled,false);
});

test("old official DTO keeps editable override until new flags arrive", () => {
  const h = harness();
  h.field("protocol_version").value = "minimax-v2";
  h.api.editNode({ id: "official", protocol_version: "minimax-v2" });
  const payload = { upstream_model: "old-alias", enabled: true };
  assert.equal(h.api.prepareSave(payload), true);
  assert.equal(payload.upstream_model, "old-alias");
  assert.equal(h.field("upstream_model").readOnly, false);
  assert.equal(h.get("node-models").hidden, true);
});

test("draft discovery uses stored credentials and invalidates selection on connection edits", async () => {
  const h = harness(async (url) => url.includes("discover-models")
    ? { items: [{ model_id: "video-model", verified: true }] }
    : { catalog_revision: 7, total: 0, items: [] });
  h.api.editNode({ id: "node", version: 3, protocol_version: "tk2sd-v1", service_url: "https://old.example", model_catalog: { revision: 7 } });
  await tick();
  h.field("api_key").value = "";
  h.field("service_url").fire("input");
  await h.get("discover-models").fire("click");
  const request = h.requests.find((item) => item.url.endsWith("discover-models"));
  const body = JSON.parse(request.options.body);
  assert.equal(body.use_stored_api_key, true);
  assert.equal(body.id, "node");
  assert.equal("api_key" in body, false);
  const checkbox = h.get("node-model-items").children[0].children[0];
  checkbox.checked = true;
  checkbox.fire("change");
  const payload = { enabled: true, upstream_model: "wrong" };
  assert.equal(h.api.prepareSave(payload), true);
  assert.deepEqual(Array.from(payload.enabled_models), ["video-model"]);
  assert.equal(payload.catalog_revision, 7);
  assert.equal("upstream_model" in payload, false);
  h.field("api_key").fire("input");
  assert.equal(h.api.prepareSave({ enabled: true }), false);
});

test("saved refresh uses both revisions and retains the successful snapshot after failure", async () => {
  const h = harness(async (url) => {
    if (url.endsWith("/refresh")) throw new Error("refresh failed");
    return { catalog_revision: 6, total: 1, last_success_at: 123, items: [{ model_id: "one", enabled: true }] };
  });
  h.field("api_key").value = "";
  h.api.editNode({ id: "node", version: 4, protocol_version: "tk2sd-v1", service_url: "https://node.example", model_catalog: { revision: 6 } });
  await tick();
  await h.get("discover-models").fire("click");
  const refresh = h.requests.find((item) => item.url.endsWith("/refresh"));
  assert.deepEqual(JSON.parse(refresh.options.body), { version: 4, catalog_revision: 6 });
  assert.match(h.get("model-catalog-status").textContent, /123/);
  assert.equal(h.get("node-model-items").children[0].children[0].checked, true);
  assert.equal(h.api.prepareSave({ enabled: true }), true);
});

test("catalog paging preserves enabled selections outside the search results", async () => {
  const items = Array.from({ length: 101 }, (_, i) => ({ model_id: `model-${i}`, enabled: i === 100 }));
  const h = harness(async (url) => ({ catalog_revision: 2, total: 101, items: url.includes("page_num=2") ? items.slice(100) : items.slice(0, 100) }));
  h.api.editNode({ id: "node", version: 1, model_catalog: { revision: 2 } });
  await tick();
  h.get("model-search").value = "model-0";
  h.get("model-search").fire("input");
  const checkbox = h.get("node-model-items").children[0].children[0];
  checkbox.checked = true;
  checkbox.fire("change");
  const payload = { enabled: true };
  assert.equal(h.api.prepareSave(payload), true);
  assert.deepEqual(Array.from(payload.enabled_models).sort(), ["model-0", "model-100"]);
  assert.equal(payload.catalog_revision, 2);
});

test("late catalog response cannot replace a newly edited connection", async () => {
  let finish;
  const h = harness(() => new Promise((resolve) => { finish = resolve; }));
  h.api.editNode({ id: "node", version: 1, model_catalog: { revision: 2 } });
  h.field("service_url").fire("input");
  finish({ catalog_revision: 2, total: 1, items: [{ model_id: "old", enabled: true }] });
  await tick();
  assert.equal(h.api.prepareSave({ enabled: true }), false);
  assert.equal(h.get("node-model-items").querySelectorAll("input").length, 0);
});

test("legacy override is omitted until explicit controlled exit", async () => {
  const h = harness(async () => ({ catalog_revision: 9, total: 1, items: [{ model_id: "real-model", enabled: true }] }));
  h.field("protocol_version").value = "minimax-v2";
  h.api.editNode({ id: "official", version: 4, legacy_model_compat: true, model_catalog: { revision: 9 } });
  await tick();
  const preserved = { enabled: true, upstream_model: "old-alias" };
  assert.equal(h.api.prepareSave(preserved), true);
  assert.equal("upstream_model" in preserved, false);
  assert.equal("enabled_models" in preserved, false);
  assert.equal(h.field("upstream_model").readOnly, true);
  h.get("exit-legacy-model").checked = true;
  h.get("exit-legacy-model").fire("change");
  const exited = { enabled: true };
  assert.equal(h.api.prepareSave(exited), true);
  assert.equal(exited.legacy_model_compat, false);
  assert.equal(exited.catalog_revision, 9);
});

test("route binding sends row and global revisions with only configured candidates", async () => {
  const h = harness(async () => ({ routing_revision: 12, total: 1, items: [{ model: "video", version: 3, state: "ambiguous", candidates: [{ protocol_version: "tk2sd-v1", node_ids: ["node"], healthy_nodes: 1 }] }] }));
  h.get("route-search").value = "video";
  h.get("route-state").value = "ambiguous";
  h.get("open-model-routes").fire("click");
  await tick();
  assert.match(h.requests[0].url, /q=video&state=ambiguous/);
  const toolbar = h.get("model-route-items").children[0].children.at(-1);
  const [select, button] = toolbar.children;
  assert.equal(select.options.length, 2);
  select.value = "tk2sd-v1";
  await button.fire("click");
  const write = h.requests.find((item) => item.options?.method === "PUT");
  assert.deepEqual(JSON.parse(write.options.body), { model: "video", protocol_version: "tk2sd-v1", version: 3, routing_revision: 12 });
});

test("manual declarations survive a token repair", async () => {
  const h = harness(async () => ({ catalog_revision: 2, total: 1, items: [{ model_id: "official-real", enabled: true }] }));
  h.field("protocol_version").value = "minimax-v2";
  h.api.editNode({ id: "official", version: 1, legacy_model_compat: false, model_catalog: { revision: 2 } });
  await tick();
  h.field("api_key").fire("input");
  const payload = { enabled: true };
  assert.equal(h.api.prepareSave(payload), true);
  assert.deepEqual(Array.from(payload.enabled_models), ["official-real"]);
});

test("missing models can be deselected but cannot be re-enabled", async () => {
  const h = harness(async () => ({ catalog_revision: 2, total: 1, items: [{ model_id: "removed", enabled: true, present: false }] }));
  h.api.editNode({ id: "node", version: 1, model_catalog: { revision: 2 } });
  await tick();
  assert.equal(h.api.prepareSave({ enabled: true }), false);
  const checkbox = h.get("node-model-items").children[0].children[0];
  assert.equal(checkbox.disabled, false);
  checkbox.checked = false;
  checkbox.fire("change");
  assert.equal(checkbox.disabled, true);
});

test("legacy manual node can repair credentials without exiting compatibility", async () => {
  const h = harness(async () => ({ catalog_revision: 2, total: 0, items: [] }));
  h.field("protocol_version").value = "minimax-v2";
  h.api.editNode({ id: "official", version: 1, service_url: "https://node.example", protocol_version: "minimax-v2", legacy_model_compat: true });
  await tick();
  h.field("api_key").fire("input");
  const payload = { enabled: true };
  assert.equal(h.api.prepareSave(payload), true);
  assert.equal("legacy_model_compat" in payload, false);
  assert.equal("enabled_models" in payload, false);
});
