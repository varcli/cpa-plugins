# Cline

CLIProxyAPI 的 Cline / ClinePass Provider 插件：通过 Cline Desktop 使用的 WorkOS 设备码流程登录，把 WorkOS 授权注册为 Cline token 对，实时拉取并跨账号合并模型目录，流式/非流式执行 chat completions，并提供账号与模型管理面板。

## 功能

| 能力 | 说明 |
|---|---|
| **WorkOS 设备码登录** | 复用 Cline Desktop 的 WorkOS 公共客户端（`client_01K3A541FN8TA3EPPHTD2325AR`）发起设备码授权，浏览器完成确认后换取 WorkOS token 对，再调用 Cline `POST /api/v1/auth/register` 注册为 Cline token |
| **单飞刷新 + 401 重试** | Cline access token 只有 1 小时有效期，且插件自有 provider 不会获得宿主的 refresh lead。插件在每次上游调用前按 10 分钟提前量刷新，上游返回 401 时再强制刷新一次重试；刷新走单飞（single-flight）保护——Cline 每次刷新都会轮换 refresh token，并发刷新会让后一个用过期的 token 兑换 |
| **宿主调度兜底** | 凭证属性写入 `refresh_interval_seconds`，让宿主也按同一节奏调度 `auth.refresh`；两套机制用同一个提前量，不会互相打架 |
| **模型发现** | `GET /api/v1/ai/cline/recommended-models` 实时目录，按 `recommended` / `free` / `clinePass` / `clineCloud` 分组；按账号缓存（TTL 5 分钟），多账号按 ID 去重合并；冷缓存按需刷新，刷新失败保留上一次的目录而不是清空 |
| **流式执行** | SSE 解析后按客户端需要决定是否补 `data: ` 分帧；宿主内联模式（无 `stream_id`）收集为 chunk 数组返回 |
| **错误语义** | 403 + `ENTITLEMENT_ERROR` 明确提示 ClinePass 未订阅；上游 500 `empty response content` 归因为 400（客户端 `max_tokens` 太小），避免一次坏请求把整个账号冷却 |
| **面板** | 账号卡片（昵称 / 邮箱 / 订阅状态 / 重命名 / 删除）与模型列表（隐藏 / 恢复 / 上下移 / 自定义添加） |

## 安装

### 从 Release（推荐）

在 CPA 的 `config.yaml` 中启用插件：

```yaml
plugins:
  enabled: true
  store-sources:
    - https://raw.githubusercontent.com/varcli/cpa-plugins/main/registry.json
  configs:
    cline:
      enabled: true
```

宿主会按 `registry.json` 拉取对应平台的产物并装载。

当前发布 linux/amd64、linux/arm64、darwin/arm64、windows/amd64 四个平台，扩展名分别为 `.so`/`.dylib`/`.dll`。

### 从源码

```bash
cd plugins/cline
CGO_ENABLED=1 go build -buildmode=c-shared -o cline.so .
```

### 配置字段

| 字段 | 类型 | 说明 |
|---|---|---|
| `model_prefix` | string | 注册模型 ID 的前缀，默认 `cline/`。用于与其他同 id 插件或原生 provider 共存 |
| `enable_model_prefix` | bool | 是否启用前缀，默认 `true`。关闭后本插件使用 Cline 原生模型 ID（如 `cline-pass/glm-5.3`），此时不要再与另一个 Cline provider 同时启用 |
| `hidden_models` | string | 隐藏的模型 ID，逗号分隔。条目以 `*` 结尾表示按前缀隐藏整个系列（如 `cline/cline-pass/*`），从而覆盖上游以后新增的模型；`*` 只允许出现在结尾 |
| `models` | string | 额外附加到模型列表的 **Cline 原生**模型 ID，逗号分隔。用于上游 feed 未列出但客户端可用的模型；同样会带上 `model_prefix` |

> 前缀开关是为了满足「同 id 插件混用」的场景：注册的模型 ID 带前缀（`cline/cline-pass/glm-5.3`），执行器转发上游前只剥离本插件自己的前缀，因此上游收到的一直是 Cline 原生 ID。

## 使用

### 登录

在管理面板的 Cline 页面点「OAuth 登录」：

1. 面板创建一次 WorkOS 设备码会话并自动打开登录页；
2. 浏览器完成授权后，面板轮询到成功即把凭证写入 CPA（`host.auth.save`），无需重启；
3. 每个账号写入独立的 `cline-<account>.json`，因此可以并存多个账号。

### 管理面接口

插件在 `/v0/management/plugins/cline/` 下暴露以下路由：

| 路由 | 说明 |
|---|---|
| `GET /models` | 返回生效的模型目录、分组、来源与 overlay；`?refresh=1` 强制按账号重新拉取上游 |
| `PUT /models` | 整体替换 overlay（`hide` / `order` / `add`） |
| `POST /models/action` | 应用单个模型操作：`hide` / `restore` / `move` / `add` |
| `GET /accounts` | 列出 Cline 账号与订阅状态 |
| `POST /oauth/start` | 开始一次 WorkOS 设备码登录 |
| `POST /oauth/poll` | 轮询登录（body `{state}` 或 query `state`） |
| `POST /accounts/rename` | 重命名账号（body `{id, name}`） |
| `POST /accounts/delete` | 删除账号（body `{id}`），返回宿主 `auth-files` 删除地址由面板调用 |

### 面板

浏览器访问 `/v0/resource/plugins/cline/panel`，或在 CPA 管理面板侧栏点「Cline」。

模型页的隐藏/排序/添加：

- **隐藏** 会同时写回插件配置的 `hidden_models`，因此 CPA 重新注册模型后依然生效；
- **排序** 与 **自定义添加** 只保存在插件内存中，CPA 重启后恢复。

## 模型

模型 ID 默认带 `cline/` 前缀（如 `cline/cline-pass/glm-5.3`）。目录优先来自 `recommended-models` 实时接口；当所有账号都拉取失败、或进程刚启动还没有任何账号目录时，回退到内置目录（见 `provider/models.go` 的 `fallbackModels`）。内置目录同时与一份「客户端兼容清单」求并集，避免上游 feed 临时缺项时把仍可用的模型摘掉。

## 开发

```bash
cd plugins/cline

# 编译动态库
CGO_ENABLED=1 go build -buildmode=c-shared -o cline.so .

# 静态检查
go vet ./...

# 单元测试 (不需要 C 工具链, 不触碰网络)
go test ./...
```

`-buildmode=c-shared` 是 CGO 构建，交叉编译需要目标平台的 C 工具链，因此发布产物由 CI 在各平台原生运行器上分别构建。

代码分层：

- `main.go` — C ABI 薄壳与方法分发；顶层持有版本字面量与 `ConfigFields` 声明（`scripts/release.go` 与 `scripts/dev-sandbox.go` 只扫描 `plugins/<id>/*.go` 顶层）；
- `provider/` — 协议实现：注册、认证、凭证生命周期、模型目录、执行器、管理面与面板；
- `clinerpc/` — 宿主 RPC 管线（host.http.do / do_stream / stream_read / stream_close、host.stream.emit / close、信封编解码）；
- `clinenx/` — 无依赖小工具（字符串归一、截断、JSON 防御性解码、列表操作）。

## 相关

- 产物命名与校验规则见 [宿主产物契约](../../docs/reference/host-artifact-contract.md)
- 发版流程见 [插件发布](../../docs/how-to/plugin-release.md)
