# Kiro

CLIProxyAPI 的 Kiro（AWS CodeWhisperer）Provider 插件：导入 Kiro IDE / kiro-cli / Amazon Q / AWS SSO 凭证，支持 Kiro 桌面浏览器 OAuth 与 AWS SSO 设备码 / 组织登录，实时拉取模型目录，流式执行 chat completions，查询配额并提供管理面板。

## 功能

| 能力 | 说明 |
|---|---|
| **凭证导入** | 扫描目录导入 Kiro IDE / kiro-cli / Amazon Q / AWS SSO 凭证：JSON、SQLite（`auth_kv`）、同目录 `<clientIdHash>.json` device-registration。`reference` 模式跟随原始文件，`copy` 模式保存独立快照 |
| **多路登录** | Kiro 桌面浏览器 OAuth（PKCE）、AWS SSO OIDC 设备码（Builder ID 与组织通用）、IAM Identity Center 组织浏览器登录 |
| **模型发现** | `ListAvailableModels` 实时目录，按账号的 `profileArn` 拉取；失败时回退到内置静态目录 + 配置的 `static_models` |
| **流式执行** | AWS Event Stream 二进制分帧解析（CRC32 校验、工具调用增量拼装、usage 透出），非流式请求聚合为单个 completion |
| **配额查询** | `GetUsageLimits` 用量与剩余额度，按账号卡片展示 |
| **面板** | 账号卡片：计划 / 订阅 / 用量进度 / 重置时间 / 重新登录；与 trae、qoder、workbuddy 面板同一套视觉与交互 |

## 安装

### 从 Release（推荐）

在 CPA 的 `config.yaml` 中启用插件：

```yaml
plugins:
  enabled: true
  store-sources:
    - https://raw.githubusercontent.com/varcli/cpa-plugins/main/registry.json
  configs:
    kiro:
      enabled: true
```

宿主会按 `registry.json` 拉取对应平台的产物并装载。

### 从源码

```bash
cd plugins/kiro
CGO_ENABLED=1 go build -buildmode=c-shared -o kiro.so .
```

### 配置字段

| 字段 | 类型 | 说明 |
|---|---|---|
| `import_mode` | enum | 凭据导入归属模式：`reference`（默认，跟随原始文件）或 `copy`（独立快照） |
| `login_mode` | enum | **新登录**使用的流程：`kiro-browser`（默认，Kiro 桌面浏览器 OAuth）或 `aws-device`（AWS SSO 设备码，支持 Builder ID 与组织，推荐用于远程 CPA 服务器）。该键为「粘性」：仅当配置显式出现 `login_mode:` 行时才改变，避免宿主中途重发空配置把流程打回默认 |
| `api_region` | string | Kiro runtime 区域，通常 `us-east-1`，与 AWS SSO 区域无关 |
| `sso_region` | string | AWS SSO OIDC 回退区域 |
| `sso_start_url` | string | 决定设备码登录的账号类型：`https://view.awsapps.com/start` 为 Builder ID；组织账号填 IAM Identity Center 的 AWS access portal URL |
| `browser_redirect_uri` | string | 仅浏览器登录使用。生产 Kiro 要求 localhost（默认 `http://localhost:3128`）或 `app.kiro.dev` 子域 |
| `runtime_base_url` | string | 可选 Kiro runtime base URL 覆盖（私有网关 / 测试） |
| `model_discovery_url` | string | 可选 `ListAvailableModels` 服务端点覆盖，默认 `https://q.{region}.amazonaws.com/` |
| `usage_url` | string | 可选 `GetUsageLimits` 服务端点覆盖，默认 `https://q.{region}.amazonaws.com/` |
| `static_models` | array | 实时发现不可用时额外广告的 Kiro runtime 模型 ID |

## 使用

### 登录

在管理面板的 Kiro 页面点「新增 Kiro 账号」：

- **浏览器流程**（`login_mode: kiro-browser`）：在新标签页完成授权。若浏览器停在「无法连接 127.0.0.1:&lt;端口&gt;」页面，把地址栏完整链接粘贴到面板的粘贴框提交即可（无需改写前缀）。粘贴的链接会走与回调页完全相同的校验与落盘路径，提交后立即换取 token 并保存凭据。
- **设备码流程**（`login_mode: aws-device`）：访问面板给出的验证地址并输入用户码。该流程同时支持 Builder ID 与 IAM Identity Center 组织账号。组织账号走两步：粘贴回调后插件返回验证地址与用户码，按提示在浏览器打开验证页即可。

粘贴框同时服务于「新增账号」与卡片上的「重新登录」：插件会按当前登录类型把回调提交到对应端点（`/oauth/login/status` 或 `/oauth/relogin/status`）。

### 导入已有凭证

```bash
cpa --kiro-import /path/to/credentials --kiro-import-mode reference
```

目录会被递归扫描，命中 `.json` / `.sqlite` / `.sqlite3` / `.db`，并自动收集同目录的 `<clientIdHash>.json` device-registration。

### 管理面接口

插件在 `/v0/management/plugins/kiro/` 下暴露以下路由：

| 路由 | 说明 |
|---|---|
| `GET /quota` | 列出 Kiro 账号的用量与剩余额度 |
| `POST /quotaRequest` | 重新向上游查询配额（不发送模型请求） |
| `GET /credentials` | 列出 CPA 凭据记录与插件内存中的请求统计 |
| `POST /oauth/login/start` | 从面板开始一次 Kiro OAuth 登录 |
| `GET /oauth/login/status` | 轮询面板发起的登录 |
| `POST /oauth/login/status` | 提交浏览器回调 URL（`callback_url`）完成登录 |
| `POST /oauth/relogin/start` | 对已有 Kiro 凭据重新登录（`auth_index`） |
| `GET /oauth/relogin/status` | 轮询凭据替换登录 |
| `POST /oauth/relogin/status` | 提交浏览器回调 URL（`callback_url`）完成重新登录 |
| `POST /oauth/callback` | 接收 Kiro 浏览器回调，组织账号继续走 AWS SSO OIDC |

### 面板

浏览器访问 `/v0/resource/plugins/kiro/panel`（宿主服务插件声明的 resource 页面），或在 CPA 管理面板侧栏点「Kiro」。

侧栏菜单项与插件列表的图标取自注册元数据里的 `logo`（Kiro 官方图标）。宿主对插件资源路由只做**精确路径匹配**，不会转发 `/v0/resource/plugins/<id>/<file>` 这类子路径，因此图标不能自托管在插件资源里，只能是一个浏览器可直接抓取的绝对 URL。

## 模型

模型 id 统一带 `kiro/` 前缀（如 `kiro/CodeBuddy-sonnet-4.5`），与宿主 `oauth-model-alias` / `oauth-excluded-models` 配置照常协同。目录优先来自账号的 `ListAvailableModels`，含上游给出的 token 上限与输入模态；发现失败时回退到内置静态目录。

## 开发

```bash
cd plugins/kiro

# 编译动态库
CGO_ENABLED=1 go build -buildmode=c-shared -o kiro.so .

# 静态检查
go vet ./...
```

`-buildmode=c-shared` 是 CGO 构建，交叉编译需要目标平台的 C 工具链，因此发布产物由 CI 在各平台原生运行器上分别构建。

代码分层：

- `main.go` — C ABI 薄壳与方法分发；顶层持有版本字面量与配置字段声明（`scripts/release.go` 与 `scripts/dev-sandbox.go` 只扫描 `plugins/<id>/*.go` 顶层），并在 `cliproxy_plugin_init` 时注入 `provider`；
- `provider/` — 协议实现：注册、认证、凭证导入与生命周期、模型目录、执行器、配额、管理面与面板；
- `kirorpc/` — 宿主 RPC 管线（host.http.do / do_stream / stream_read、信封编解码）；
- `kirostream/` — AWS Event Stream 解码；
- `kirochat/` — 请求体构造与改写；
- `kironx/` — 无依赖小工具。

## 相关

- 产物命名与校验规则见 [宿主产物契约](../../docs/reference/host-artifact-contract.md)
- 发版流程见 [插件发布](../../docs/how-to/plugin-release.md)
