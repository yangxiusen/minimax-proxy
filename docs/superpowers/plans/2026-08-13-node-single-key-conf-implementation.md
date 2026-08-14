# MiniMax-H3 Node Single-Key Configuration Implementation Plan

> 本计划已由正式 `/s-change` 变更包 `specs/developing/version_0.0.1/changes/006-node-single-key-conf/task.md` 取代。后续开发以 006 变更包及其验收文档为唯一执行入口，本文件仅保留为设计过程记录。

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make MiniMax-H3 create and exclusively use a root `conf.yml` containing one 32-character API Key and generated WebUI credentials, while simplifying MiniMax-H3-Proxy node management to one API Key field.

**Architecture:** A focused Node credential loader owns strict YAML parsing, secure generation, and atomic first-write behavior. `start.py` passes an explicit credential object to the Node app so Pydantic Settings cannot reintroduce an environment credential source; Node auth compares one Bearer Key and WebUI auth compares the generated username/password. Proxy keeps encrypted Key storage and its current single-string Node client, removes Key ID from application semantics, and leaves the legacy SQLite column as a non-null compatibility placeholder.

**Tech Stack:** Python 3, PyYAML, Pydantic Settings, FastAPI/Gradio, pytest; Go, SQLite, net/http, embedded HTML/JavaScript, Go testing.

---

## Repository Boundaries

This feature spans two independent Git repositories:

- Node: `E:/MiniMax-WorkFlow/Minimax-H3`
- Proxy and plan owner: `E:/MiniMax-WorkFlow/MiniMax-H3-Proxy`

Both worktrees already contain user changes and untracked feature files that the current implementation depends on, so execution must remain in the current worktrees. Do not create a clean worktree from HEAD, revert unrelated changes, or stage files outside the current task. Commit Node and Proxy changes separately with explicit path lists.

## File Map

### MiniMax-H3

- Create `h3_service/credential_config.py`: strict `conf.yml` schema, safe loader, random generation, atomic publish.
- Create `tests/h3_service/test_credential_config.py`: creation, stability, concurrency, strict parsing and redaction tests.
- Modify `h3_service/settings.py`: remove all credential fields so it owns only non-secret runtime settings.
- Modify `h3_service/auth.py`: single Bearer Key comparison, shared rate limit, plaintext-in-memory WebUI comparison.
- Modify `h3_service/app.py`: construct the single-key authenticator before and after Gradio mount.
- Modify `start.py`: load/create root `conf.yml` before `ServiceSettings`; remove credential CLI overrides.
- Modify credential-using tests under `tests/h3_service/`: construct settings with single Key and password.
- Modify `.gitignore`, `.env.example`, `README.md`, `SECURITY.md`, `requirements.txt`; create `conf.example.yml`.

### MiniMax-H3-Proxy

- Modify `internal/domain/model_node.go`: remove `APIKeyID` from the application model.
- Modify `internal/config/model_node.go`: remove Key ID validation.
- Modify `internal/store/sqlite/model_node.go`: scan/write the old column as an ignored non-null compatibility value.
- Modify `internal/httpapi/manager/nodes.go`: remove request/response Key ID and enforce one 32-character alphanumeric Key.
- Modify `internal/httpapi/manager/web/manager.html` and `manager.js`: remove Key ID control and payload field.
- Modify Node, Registry, Artifact, Cleanup and Profile tests that still populate `APIKeyID`; keep external-customer `APIKeyID` fields unchanged.
- Modify `internal/upstream/nodeapi/client_test.go`: assert exact single-Key Bearer behavior.
- Modify `specs/developing/version_0.0.1/changes/004-manager-node-configuration/CHANGE_SPEC.md`, `PRD_DELTA.md`, `PROTOTYPE_DELTA.md`, `TECH_SOLUTION.md`, `API_DELTA.md`, `DATABASE_DELTA.md`, `NODE_API_CONTRACT_AUDIT.md`, `api-modules/manager-nodes.md`, `api-modules/h3-node-integration.md`, `task.md`, and `TEST_ACCEPTANCE.md` to replace the superseded credential contract.

## Task 1: Build Strict Node `conf.yml` Loader

**Files:**
- Create: `E:/MiniMax-WorkFlow/Minimax-H3/h3_service/credential_config.py`
- Create: `E:/MiniMax-WorkFlow/Minimax-H3/tests/h3_service/test_credential_config.py`

- [ ] **Step 1: Write failing creation and repeat-load tests**

Add tests using a temporary project root and the desired public API:

```python
from pathlib import Path
import re

from h3_service.credential_config import load_or_create_credentials


def test_missing_conf_is_generated_once(tmp_path: Path):
    first = load_or_create_credentials(tmp_path)
    config_path = tmp_path / "conf.yml"
    original = config_path.read_bytes()
    original_mtime = config_path.stat().st_mtime_ns

    assert re.fullmatch(r"[A-Za-z0-9]{32}", first.api_key)
    assert first.webui_username == "admin"
    assert re.fullmatch(r"[A-Za-z0-9]{8}", first.webui_password)
    assert re.search(r"[A-Za-z]", first.webui_password)
    assert re.search(r"[0-9]", first.webui_password)

    second = load_or_create_credentials(tmp_path)
    assert second == first
    assert config_path.read_bytes() == original
    assert config_path.stat().st_mtime_ns == original_mtime
```

- [ ] **Step 2: Run the test and verify RED**

Run from `E:/MiniMax-WorkFlow/Minimax-H3`:

```powershell
walkingwithai\python.exe -m pytest tests\h3_service\test_credential_config.py -q
```

Expected: collection fails because `h3_service.credential_config` does not exist.

- [ ] **Step 3: Implement the data model, secure generators and strict loader**

Create `credential_config.py` with these owned interfaces:

```python
from dataclasses import dataclass
from pathlib import Path
import os
import re
import secrets
import string
import tempfile

import yaml

ALPHANUMERIC = string.ascii_letters + string.digits
API_KEY_PATTERN = re.compile(r"^[A-Za-z0-9]{32}$")
PASSWORD_PATTERN = re.compile(r"^[A-Za-z0-9]{8}$")


@dataclass(frozen=True)
class CredentialConfig:
    api_key: str
    webui_username: str
    webui_password: str


class CredentialConfigError(ValueError):
    pass


def generate_api_key() -> str:
    return "".join(secrets.choice(ALPHANUMERIC) for _ in range(32))


def generate_webui_password() -> str:
    while True:
        value = "".join(secrets.choice(ALPHANUMERIC) for _ in range(8))
        if any(c.isalpha() for c in value) and any(c.isdigit() for c in value):
            return value
```

Use a custom `yaml.SafeLoader` mapping constructor that raises `CredentialConfigError` on duplicate keys. Validate exact key sets `{api, webui}`, `{key}`, and `{username, password}`; require `type(value) is str`; never include values in error text.

Implement `load_or_create_credentials(project_root: Path) -> CredentialConfig`. When absent, serialize with `yaml.safe_dump(..., allow_unicode=False, sort_keys=False)`, write UTF-8 through `tempfile.NamedTemporaryFile(delete=False, dir=project_root)`, flush and `os.fsync`, apply `os.chmod(temp, 0o600)`, then publish without overwriting. Use `os.link(temp, config_path)` followed by unlinking the temp file so concurrent creators cannot replace a winner; when `FileExistsError` occurs, discard the temp file and read the winner.

- [ ] **Step 4: Add failing strictness and concurrency tests**

Cover duplicate keys, unknown keys, missing fields, non-string YAML scalars, invalid Key/password formats, error redaction, and eight concurrent callers:

```python
def test_existing_invalid_conf_is_rejected_without_rewrite(tmp_path):
    path = tmp_path / "conf.yml"
    path.write_text("api:\n  key: 123\nwebui:\n  username: admin\n  password: A1b2C3d4\n", encoding="utf-8")
    original = path.read_bytes()

    with pytest.raises(CredentialConfigError, match=r"api\.key") as caught:
        load_or_create_credentials(tmp_path)

    assert "123" not in str(caught.value)
    assert path.read_bytes() == original
```

For concurrency, call the loader through `ThreadPoolExecutor(max_workers=8)` and assert every result equals the same parsed file.

- [ ] **Step 5: Run loader tests and verify GREEN**

```powershell
walkingwithai\python.exe -m pytest tests\h3_service\test_credential_config.py -q
```

Expected: all loader tests pass and no credential value is printed.

- [ ] **Step 6: Commit the Node loader**

```powershell
git add -- h3_service/credential_config.py tests/h3_service/test_credential_config.py
git commit -m "feat: generate node credentials from conf yaml"
```

## Task 2: Pass `conf.yml` Credentials Into Node Startup

**Files:**
- Modify: `E:/MiniMax-WorkFlow/Minimax-H3/h3_service/settings.py`
- Modify: `E:/MiniMax-WorkFlow/Minimax-H3/start.py`
- Modify: `E:/MiniMax-WorkFlow/Minimax-H3/tests/h3_service/test_settings.py`
- Modify: `E:/MiniMax-WorkFlow/Minimax-H3/h3_service/app.py`
- Modify: `E:/MiniMax-WorkFlow/Minimax-H3/tests/h3_service/test_app.py`

- [ ] **Step 1: Write failing settings and app tests for explicit credential ownership**

Replace old multi-key validation expectations with a settings-source assertion:

```python
def test_service_settings_have_no_credential_fields(monkeypatch):
    monkeypatch.setenv("H3_API_KEY", "A" * 32)
    monkeypatch.setenv("H3_UI_USERNAME", "attacker")
    monkeypatch.setenv("H3_UI_PASSWORD", "Z9y8X7w6")

    settings = ServiceSettings(_env_file=None)

    assert "api_key" not in settings.model_fields
    assert "ui_username" not in settings.model_fields
    assert "ui_password" not in settings.model_fields
```

In `test_app.py`, construct `CredentialConfig("A" * 32, "admin", "A1b2C3d4")` and assert `create_app(settings, credentials=credentials, mount_ui=False)` installs an authenticator. Calling `create_app` without `credentials` must raise `ValueError("Node 凭据未配置")`.

- [ ] **Step 2: Run settings tests and verify RED**

```powershell
walkingwithai\python.exe -m pytest tests\h3_service\test_settings.py -q
```

Expected: failures because credential fields still exist and `create_app` has no explicit credentials parameter.

- [ ] **Step 3: Replace credential settings and wire startup**

Delete `APIKeyConfig`, `api_keys`, `ui_username`, and `ui_password_hash` from `ServiceSettings`, including validators that inspect credential presence. Keep non-credential settings in `BaseSettings`. Security no longer depends on binding mode because `start.py` always loads or creates `conf.yml` before opening listeners.

In `start.py`, remove `--ui-username` and `--ui-password-hash`. Resolve and load before constructing settings:

```python
from pathlib import Path

from h3_service.credential_config import load_or_create_credentials

PROJECT_ROOT = Path(__file__).resolve().parent
credentials = load_or_create_credentials(PROJECT_ROOT)
settings = ServiceSettings(**setting_overrides)
```

Pass `credentials=credentials` to `create_app`. Add a required keyword-only `credentials: CredentialConfig | None = None` argument to `create_app`; raise before app construction when it is absent. Do not add credential command-line overrides or environment fallbacks.

- [ ] **Step 4: Run settings and app construction tests**

```powershell
walkingwithai\python.exe -m pytest tests\h3_service\test_settings.py tests\h3_service\test_app.py -q
```

Expected: PASS after app fixtures supply explicit `CredentialConfig` objects.

- [ ] **Step 5: Commit Node startup integration**

```powershell
git add -- h3_service/settings.py h3_service/app.py start.py tests/h3_service/test_settings.py tests/h3_service/test_app.py
git commit -m "feat: load node credentials from conf yaml"
```

## Task 3: Replace Node Multi-Key/Scope Authentication

**Files:**
- Modify: `E:/MiniMax-WorkFlow/Minimax-H3/h3_service/auth.py`
- Modify: `E:/MiniMax-WorkFlow/Minimax-H3/h3_service/app.py`
- Modify: `E:/MiniMax-WorkFlow/Minimax-H3/tests/h3_service/test_auth.py`
- Modify: `E:/MiniMax-WorkFlow/Minimax-H3/tests/h3_service/test_ui_auth.py`
- Modify: `E:/MiniMax-WorkFlow/Minimax-H3/tests/h3_service/test_execution_api.py`
- Modify: `E:/MiniMax-WorkFlow/Minimax-H3/tests/h3_service/test_artifact_api.py`
- Modify: `E:/MiniMax-WorkFlow/Minimax-H3/tests/h3_service/test_cleanup_api.py`

- [ ] **Step 1: Rewrite auth tests to express single-Key behavior**

Use a valid 32-character test Key and assert all old composite tokens fail:

```python
API_KEY = "Abcdefghijklmnopqrstuvwx12345678"


def test_single_key_authenticates_every_internal_scope():
    auth = Authenticator(API_KEY, requests_per_minute=10)
    for scope in ("health", "execute", "artifact:read", "artifact:write", "artifact:delete", "maintenance"):
        auth.authenticate(f"Bearer {API_KEY}", {scope})


@pytest.mark.parametrize("header", [None, "", "Bearer wrong", f"Bearer proxy.{API_KEY}"])
def test_missing_wrong_and_legacy_tokens_are_rejected(header):
    with pytest.raises(ServiceError) as caught:
        Authenticator(API_KEY).authenticate(header, {"health"})
    assert caught.value.status_code == 401
```

Update the rate-limit test so failed attempts share one fixed bucket instead of using Key ID.

- [ ] **Step 2: Run auth tests and verify RED**

```powershell
walkingwithai\python.exe -m pytest tests\h3_service\test_auth.py -q
```

Expected: constructor/signature and old composite-token assertions fail.

- [ ] **Step 3: Implement single-Key and WebUI comparisons**

Use `secrets.compare_digest` for both authentication paths. Keep route call sites stable by retaining `require_scopes(*_scopes)`, but ignore the scope values:

```python
@dataclass(frozen=True)
class Principal:
    authenticated: bool = True


class Authenticator:
    def __init__(self, api_key: str, *, requests_per_minute: int = 120) -> None:
        self._api_key = api_key
        self._requests_per_minute = requests_per_minute

    def authenticate(self, authorization: str | None, _required_scopes: set[str]) -> Principal:
        self._check_rate_limit()
        if not authorization or not authorization.startswith("Bearer "):
            raise ServiceError("missing_api_key", "缺少 Bearer API Key", status_code=401)
        supplied = authorization[7:]
        if not secrets.compare_digest(supplied, self._api_key):
            raise ServiceError("invalid_api_key", "API Key 无效", status_code=401)
        return Principal()
```

Change `build_ui_auth` to accept `CredentialConfig` and compare its username/password with `compare_digest`. Update both authenticator constructions in `app.py` to pass `credentials.api_key` and remove `allow_unauthenticated`.

- [ ] **Step 4: Update Node API fixtures and run the focused suite**

Replace `APIKeyConfig` and Argon2 setup in execution/artifact/UI tests with one shared `CredentialConfig(api_key=API_KEY, webui_username="admin", webui_password="A1b2C3d4")`. Pass it to `create_app` and `build_ui_auth`. Replace every `Bearer proxy.secret` with `Bearer {API_KEY}`.

```powershell
walkingwithai\python.exe -m pytest tests\h3_service\test_auth.py tests\h3_service\test_ui_auth.py tests\h3_service\test_execution_api.py tests\h3_service\test_artifact_api.py tests\h3_service\test_cleanup_api.py -q
```

Expected: PASS; scope-specific denial tests have been replaced with all-route single-Key acceptance tests.

- [ ] **Step 5: Commit Node authentication**

```powershell
git add -- h3_service/auth.py h3_service/app.py tests/h3_service/test_auth.py tests/h3_service/test_ui_auth.py tests/h3_service/test_execution_api.py tests/h3_service/test_artifact_api.py tests/h3_service/test_cleanup_api.py
git commit -m "refactor: use one node api key"
```

## Task 4: Remove Proxy Key ID From Domain and SQLite Semantics

**Files:**
- Modify: `E:/MiniMax-WorkFlow/MiniMax-H3-Proxy/internal/domain/model_node.go`
- Modify: `E:/MiniMax-WorkFlow/MiniMax-H3-Proxy/internal/config/model_node.go`
- Modify: `E:/MiniMax-WorkFlow/MiniMax-H3-Proxy/internal/store/sqlite/model_node.go`
- Modify: `E:/MiniMax-WorkFlow/MiniMax-H3-Proxy/internal/store/sqlite/model_node_test.go`
- Modify: `E:/MiniMax-WorkFlow/MiniMax-H3-Proxy/internal/store/sqlite/migration_v7_test.go`

- [ ] **Step 1: Write failing Store compatibility tests**

Add tests that create and update an H3 node without `APIKeyID`, then query the raw compatibility column:

```go
func TestH3NodeStoresEmptyCompatibilityKeyID(t *testing.T) {
    store := openTestStore(t)
    input := validH3NodeInput()
    input.APIKeyCiphertext = []byte("ciphertext")
    input.APIKeyNonce = []byte("nonce")
    input.APIKeyFingerprint = "sha256:test"

    node, err := store.CreateModelNode(context.Background(), input)
    if err != nil {
        t.Fatal(err)
    }
    var compatibility string
    if err := store.db.QueryRow(`SELECT api_key_id FROM model_service_nodes WHERE id=?`, node.ID).Scan(&compatibility); err != nil {
        t.Fatal(err)
    }
    if compatibility != "" {
        t.Fatalf("api_key_id=%q, want empty compatibility value", compatibility)
    }
}
```

Add an upgrade fixture containing a historical non-empty Key ID and assert Store reads the node without exposing that value in `domain.ModelNodeInput`.

- [ ] **Step 2: Run Store tests and verify RED**

```powershell
go test ./internal/store/sqlite -run 'ModelNode|SingleEndpoint' -count=1
```

Expected: compile/assertion failure because `APIKeyID` is still part of the model and persisted.

- [ ] **Step 3: Implement the compatibility boundary**

Remove `APIKeyID` from `ModelNodeInput` and Node normalization. Keep `api_key_id` in SQL column lists, but:

- pass `""` for create/update H3 rows;
- scan it into a local ignored `string` variable;
- omit it from `sameModelNodeInput` and persistence structs;
- preserve NULL for Legacy inserts to satisfy the other side of the v7 CHECK.

Do not change external customer API Key identifiers in task, artifact ownership, callbacks, or public Bearer authentication.

- [ ] **Step 4: Run Store/config tests and verify GREEN**

```powershell
go test ./internal/config ./internal/store/sqlite -run 'ModelNode|SingleEndpoint|Migration' -count=1
```

Expected: PASS; v7 databases open without a new migration and H3 rows keep non-null compatibility values.

- [ ] **Step 5: Commit Proxy domain compatibility**

```powershell
git add -- internal/domain/model_node.go internal/config/model_node.go internal/store/sqlite/model_node.go internal/store/sqlite/model_node_test.go internal/store/sqlite/migration_v7_test.go
git commit -m "refactor: remove node api key id semantics"
```

## Task 5: Simplify Proxy Manager API and UI to One 32-Character Key

**Files:**
- Modify: `E:/MiniMax-WorkFlow/MiniMax-H3-Proxy/internal/httpapi/manager/nodes.go`
- Modify: `E:/MiniMax-WorkFlow/MiniMax-H3-Proxy/internal/httpapi/manager/nodes_test.go`
- Modify: `E:/MiniMax-WorkFlow/MiniMax-H3-Proxy/internal/httpapi/manager/web/manager.html`
- Modify: `E:/MiniMax-WorkFlow/MiniMax-H3-Proxy/internal/httpapi/manager/web/manager.js`
- Modify: `E:/MiniMax-WorkFlow/MiniMax-H3-Proxy/internal/httpapi/manager/handler_test.go`

- [ ] **Step 1: Write failing Manager contract tests**

Update `validNodeJSON` to use a strict 32-character Key and no Key ID. Add assertions:

```go
func TestNodeAPIKeyIsExactly32AlphanumericCharacters(t *testing.T) {
    h := testHandler(Dependencies{
        Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour},
        Nodes: &nodeStoreStub{}, NodeSecrets: testNodeSecrets{},
    })
    cookie := login(t, h, "admin", "secret", "192.0.2.4:1")
    for _, key := range []string{"short", strings.Repeat("A", 31), strings.Repeat("A", 33), strings.Repeat("A", 31) + "!"} {
        response := serve(h, http.MethodPost, "/manager/api/nodes", nodeJSONWithKey(key), "application/json", cookie, "192.0.2.4:1", false)
        assertManagerError(t, response, http.StatusBadRequest, "bad_request_error")
    }
}

func TestDeprecatedNodeAPIKeyIDIsRejected(t *testing.T) {
    h := testHandler(Dependencies{
        Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour},
        Nodes: &nodeStoreStub{}, NodeSecrets: testNodeSecrets{},
    })
    cookie := login(t, h, "admin", "secret", "192.0.2.4:1")
    response := serve(h, http.MethodPost, "/manager/api/nodes", `{"id":"node-1","api_key_id":"proxy"}`, "application/json", cookie, "192.0.2.4:1", false)
    assertManagerError(t, response, http.StatusBadRequest, "bad_request_error")
}
```

Use one existing-style helper instead of parallel test setup:

```go
func nodeJSONWithKey(key string) string {
    value := map[string]any{
        "id": "node-1", "service_url": "http://private.example:7860", "protocol_version": "h3-node-v1",
        "api_key": key, "poll_interval": "3s", "request_timeout": "30s", "enabled": true,
    }
    data, _ := json.Marshal(value)
    return string(data)
}
```

Add `strings` to the existing imports. Reuse the existing `testHandler`, `login`, `serve`, and `assertManagerError` helpers shown above; do not add another handler abstraction.

Add an embedded-page assertion that `api_key_id` and visible “Key ID” are absent, while the API Key input has `minlength=32`, `maxlength=32`, and an alphanumeric `pattern`.

- [ ] **Step 2: Run Manager tests and verify RED**

```powershell
go test ./internal/httpapi/manager -run 'Node|Web' -count=1
```

Expected: tests fail because Key ID remains in the DTO/page and Key validation accepts 32-512 arbitrary characters.

- [ ] **Step 3: Implement the API and UI contraction**

Delete `APIKeyID` from `nodeRequest` and `nodeDTO`. Replace validation with:

```go
var nodeAPIKeyPattern = regexp.MustCompile(`^[A-Za-z0-9]{32}$`)

func validNodeAPIKey(value string) bool {
    return nodeAPIKeyPattern.MatchString(value)
}
```

Use the stable message “api_key 必须是 32 位字母或数字”. Preserve edit semantics: absent Key reuses complete stored ciphertext; create/test/Legacy upgrade requires a Key. Before Store update, reject a Legacy-to-H3 transition without a new Key so SQLite constraints cannot produce 500.

In HTML, remove the Key ID label and change the password input to:

```html
<label><span>API Key</span><input name="api_key" type="password" minlength="32" maxlength="32" pattern="[A-Za-z0-9]{32}" autocomplete="new-password" placeholder="编辑时留空表示沿用已保存 Key"></label>
```

In JavaScript, remove `api_key_id` from `fillNodeForm` and `nodePayload`. Keep Secret blank on edit and `use_stored_api_key` only for connection tests.

- [ ] **Step 4: Run Manager tests and JavaScript syntax validation**

```powershell
go test ./internal/httpapi/manager -run 'Node|Web' -count=1
node --check internal/httpapi/manager/web/manager.js
```

Expected: PASS and no JavaScript syntax output.

- [ ] **Step 5: Commit Proxy Manager changes**

```powershell
git add -- internal/httpapi/manager/nodes.go internal/httpapi/manager/nodes_test.go internal/httpapi/manager/web/manager.html internal/httpapi/manager/web/manager.js internal/httpapi/manager/handler_test.go
git commit -m "feat: configure nodes with one api key"
```

## Task 6: Verify Every Proxy Node Client Sends the Single Key

**Files:**
- Modify: `E:/MiniMax-WorkFlow/MiniMax-H3-Proxy/internal/upstream/nodeapi/client_test.go`
- Modify: `E:/MiniMax-WorkFlow/MiniMax-H3-Proxy/internal/upstream/registry/prober_test.go`
- Modify: `E:/MiniMax-WorkFlow/MiniMax-H3-Proxy/internal/upstream/registry/runtime_test.go`
- Modify: `E:/MiniMax-WorkFlow/MiniMax-H3-Proxy/internal/artifact/service_test.go`
- Modify: `E:/MiniMax-WorkFlow/MiniMax-H3-Proxy/internal/cleanup/worker_test.go`
- Modify: `E:/MiniMax-WorkFlow/MiniMax-H3-Proxy/internal/profile/node_test_executor_test.go`

- [ ] **Step 1: Change client tests to a valid single Key and verify RED at integration boundaries**

Use one shared value such as `Abcdefghijklmnopqrstuvwx12345678`. Assert every local HTTP fake receives exactly:

```go
if got := r.Header.Get("Authorization"); got != "Bearer Abcdefghijklmnopqrstuvwx12345678" {
    t.Fatalf("Authorization=%q", got)
}
```

Remove `APIKeyID` from ModelNode fixtures. Keep factory signatures accepting one `apiKey string`; the production `nodeapi.NewClient` already represents the desired one-Key boundary.

- [ ] **Step 2: Run focused packages and verify failures identify stale fixtures**

```powershell
go test ./internal/upstream/nodeapi ./internal/upstream/registry ./internal/artifact ./internal/cleanup ./internal/profile -count=1
```

Expected before fixture cleanup: compilation failures at stale `APIKeyID` fields and assertion failures expecting composite Keys.

- [ ] **Step 3: Update all Node credential call-site fixtures**

Remove only model-node `APIKeyID` fields. Do not alter `domain.Task.APIKeyID`, artifact owner IDs, or external public API Key tests. Ensure each fake secret opener returns the exact 32-character Key.

- [ ] **Step 4: Run focused packages and verify GREEN**

```powershell
go test ./internal/upstream/nodeapi ./internal/upstream/registry ./internal/artifact ./internal/cleanup ./internal/profile -count=1
```

Expected: PASS; all consumed Node routes carry one unchanged Bearer Key.

- [ ] **Step 5: Commit Proxy client integration tests**

```powershell
git add -- internal/upstream/nodeapi/client_test.go internal/upstream/registry/prober_test.go internal/upstream/registry/runtime_test.go internal/artifact/service_test.go internal/cleanup/worker_test.go internal/profile/node_test_executor_test.go
git commit -m "test: cover single-key node clients"
```

## Task 7: Clean Credential Configuration and Synchronize Specifications

**Files:**
- Modify: `E:/MiniMax-WorkFlow/Minimax-H3/.gitignore`
- Modify: `E:/MiniMax-WorkFlow/Minimax-H3/.env.example`
- Create: `E:/MiniMax-WorkFlow/Minimax-H3/conf.example.yml`
- Modify: `E:/MiniMax-WorkFlow/Minimax-H3/README.md`
- Modify: `E:/MiniMax-WorkFlow/Minimax-H3/SECURITY.md`
- Modify: `E:/MiniMax-WorkFlow/Minimax-H3/requirements.txt`
- Modify: `E:/MiniMax-WorkFlow/MiniMax-H3-Proxy/README.md`
- Modify: `E:/MiniMax-WorkFlow/MiniMax-H3-Proxy/config.example.yaml`
- Modify: `E:/MiniMax-WorkFlow/MiniMax-H3-Proxy/specs/developing/version_0.0.1/changes/004-manager-node-configuration/CHANGE_SPEC.md`
- Modify: `E:/MiniMax-WorkFlow/MiniMax-H3-Proxy/specs/developing/version_0.0.1/changes/004-manager-node-configuration/PRD_DELTA.md`
- Modify: `E:/MiniMax-WorkFlow/MiniMax-H3-Proxy/specs/developing/version_0.0.1/changes/004-manager-node-configuration/PROTOTYPE_DELTA.md`
- Modify: `E:/MiniMax-WorkFlow/MiniMax-H3-Proxy/specs/developing/version_0.0.1/changes/004-manager-node-configuration/TECH_SOLUTION.md`
- Modify: `E:/MiniMax-WorkFlow/MiniMax-H3-Proxy/specs/developing/version_0.0.1/changes/004-manager-node-configuration/API_DELTA.md`
- Modify: `E:/MiniMax-WorkFlow/MiniMax-H3-Proxy/specs/developing/version_0.0.1/changes/004-manager-node-configuration/DATABASE_DELTA.md`
- Modify: `E:/MiniMax-WorkFlow/MiniMax-H3-Proxy/specs/developing/version_0.0.1/changes/004-manager-node-configuration/NODE_API_CONTRACT_AUDIT.md`
- Modify: `E:/MiniMax-WorkFlow/MiniMax-H3-Proxy/specs/developing/version_0.0.1/changes/004-manager-node-configuration/api-modules/manager-nodes.md`
- Modify: `E:/MiniMax-WorkFlow/MiniMax-H3-Proxy/specs/developing/version_0.0.1/changes/004-manager-node-configuration/api-modules/h3-node-integration.md`
- Modify: `E:/MiniMax-WorkFlow/MiniMax-H3-Proxy/specs/developing/version_0.0.1/changes/004-manager-node-configuration/task.md`
- Modify: `E:/MiniMax-WorkFlow/MiniMax-H3-Proxy/specs/developing/version_0.0.1/changes/004-manager-node-configuration/TEST_ACCEPTANCE.md`

- [ ] **Step 1: Add static configuration regression checks**

Use repository searches as executable acceptance checks:

```powershell
rg -n "H3_API_KEYS|H3_UI_PASSWORD_HASH|key_id\.secret|api_key_id|Key ID|scope" README.md SECURITY.md .env.example conf.example.yml h3_service start.py
```

Expected before cleanup: matches in active Node configuration/auth documentation.

```powershell
rg -n "api_key_id|Key ID|key_id\.secret|scope" README.md config.example.yaml specs/developing/version_0.0.1/changes/004-manager-node-configuration
```

Expected before cleanup: matches in Proxy Node-credential documents.

- [ ] **Step 2: Remove obsolete sources and add safe examples**

Add `conf.yml` to Node `.gitignore`. Create:

```yaml
api:
  key: "ReplaceWith32LettersAndNumbers12"
webui:
  username: "admin"
  password: "A1b2C3d4"
```

Ensure the example Key is exactly 32 alphanumeric characters before committing. Remove credential entries from `.env.example`, leaving non-credential settings. Remove `argon2-cffi` only after `rg -n "argon2" h3_service tests requirements.txt` confirms no runtime use remains.

Update Node docs with first-start generation, exact file path, how to read credentials, strict no-overwrite behavior and file security. Apply this exact replacement matrix to the Proxy 004 package:

| Old active rule | New active rule |
| --- | --- |
| Key ID + Secret | One 32-character alphanumeric API Key |
| `Bearer key_id.secret` | `Bearer key` |
| Required scopes | One Key authorizes all Node routes |
| Key ID validation | Removed |
| v7 `api_key_id` business field | Ignored non-null compatibility column |
| Legacy upgrade needs Key ID + Secret | Legacy upgrade needs one API Key |

Keep historical defect evidence only in `NODE_API_CONTRACT_AUDIT.md`, explicitly label it “superseded by single-Key design”, and remove it from current API examples and acceptance expectations. Mark `docs/superpowers/specs/2026-08-13-node-single-key-conf-design.md` as the decision source.

- [ ] **Step 3: Re-run static checks**

Node expected matches: zero active configuration or authentication references to the removed variables, Key ID, scopes, or composite Token. Proxy whitelist: `api_key_id` may remain only in `migrations/007_single_endpoint_nodes.sql`, SQL statements/local ignored scan variables in `internal/store/sqlite/model_node.go`, migration/store compatibility tests, and documentation sentences that explicitly call it ignored. `Key ID`, `key_id.secret`, and Node scope rules must have no active Manager/API examples.

- [ ] **Step 4: Commit docs/config cleanup in each repository**

Node:

```powershell
git add -- .gitignore .env.example conf.example.yml README.md SECURITY.md requirements.txt
git commit -m "docs: document generated node credentials"
```

Proxy:

```powershell
git add -- README.md config.example.yaml specs/developing/version_0.0.1/changes/004-manager-node-configuration
git commit -m "docs: align node management with single key"
```

## Task 8: End-to-End Verification and Local Restart

**Files:**
- Generated, ignored: `E:/MiniMax-WorkFlow/Minimax-H3/conf.yml`
- Runtime logs only; no source edits unless a failing regression requires a new test-first fix.

- [ ] **Step 1: Run the complete Node suite**

```powershell
walkingwithai\python.exe -m pytest tests\h3_service -q
```

Expected: all tests pass, including strict config, single-Key auth, WebUI auth, execution, artifact and cleanup suites.

- [ ] **Step 2: Run the complete Proxy quality gate**

```powershell
gofmt -w internal/domain/model_node.go internal/config/model_node.go internal/store/sqlite/model_node.go internal/store/sqlite/model_node_test.go internal/store/sqlite/migration_v7_test.go internal/httpapi/manager/nodes.go internal/httpapi/manager/nodes_test.go internal/httpapi/manager/handler_test.go internal/upstream/nodeapi/client_test.go internal/upstream/registry/prober_test.go internal/upstream/registry/runtime_test.go internal/artifact/service_test.go internal/cleanup/worker_test.go internal/profile/node_test_executor_test.go
go test ./... -count=1
go vet ./...
go build ./cmd/server ./cmd/healthcheck
node --check internal/httpapi/manager/web/manager.js
```

Expected: all commands succeed. Do not format unrelated user files.

- [ ] **Step 3: Verify no secret leakage or active legacy credential rules**

```powershell
rg -n "Authorization.*%|api_key.*slog|ui_password.*log|key_id\.secret|H3_API_KEYS|H3_UI_PASSWORD_HASH" h3_service start.py tests\h3_service
rg -n "api_key_id|Key ID|key_id\.secret" internal README.md config.example.yaml specs\developing\version_0.0.1\changes\004-manager-node-configuration
```

Expected: no credential logging; Proxy code matches only the SQLite compatibility column and external-customer API Key identifiers, not model-node Key ID semantics.

- [ ] **Step 4: Restart Node and confirm one-time config creation**

Stop only the currently running MiniMax-H3 Node process after resolving its PID and executable path. If `conf.yml` is absent, start through the repository's normal launcher and wait for the Node health port. Confirm `conf.yml` exists, has the three exact fields, Key length 32, password length 8, and contains no extra settings. Do not print the Key or password in terminal output.

Restart Node a second time and compare a SHA-256 digest and mtime of `conf.yml`; both must be unchanged.

- [ ] **Step 5: Update Proxy node and run live connection test**

Use the Manager page to update the Node with the Key read from local `conf.yml`, without displaying it in logs or screenshots. Test connection and confirm health/capabilities pass. Submit and cancel one lightweight execution only when the local model environment is ready; otherwise record this GPU-dependent check as manual pending.

- [ ] **Step 6: Restart Proxy and verify the Manager workflow**

Restart the Proxy on its existing configured port. Verify `/manager/login`, open node management, confirm only one API Key field exists, edit with Key blank to reuse it, test the stored Key, and confirm the previous 500 does not recur.

- [ ] **Step 7: Record final repository states without sweeping user changes into commits**

```powershell
git status --short
git log -5 --oneline
```

Run separately in each repository. Report remaining unrelated changes, test evidence, running URLs, and any real-GPU checks still pending. Do not create a blanket final commit.
