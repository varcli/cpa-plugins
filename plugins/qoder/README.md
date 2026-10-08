# Qoder CPA 插件

[CLIProxyAPI (CPA)](https://github.com/router-for-me/CLIProxyAPI) 的 Qoder 统一 Provider 插件：**一个插件同时覆盖 CN（qoder.com.cn）与 Intl（qoder.com）双区**，多账号 OAuth/PAT 双登录、动态模型、COSY 签名推理、每日签到、积分面板、token 自动保活。

每个账号按 auth 文件内的 `region` 字段路由（`qoder-cn-<uid>.json` → CN，`qoder-intl-<uid>.json` → Intl）；新增账号的登录区域由插件配置 `login_region`（cn | intl，默认 cn）决定。`login_region` 是**粘性**的：只有配置里显式出现该键才会改变它，宿主在 auth store 变动时下发的空配置不会把它重置回 `cn`（Intl 登录不会中途被改道）。

## 功能

| 能力 | 说明 |
|---|---|
| **双区合一** | 一个插件覆盖 CN（qoder.com.cn）与 Intl（qoder.com），按账号 `region` 路由 |
| **双登录方式** | OAuth 设备授权（PKCE，dt- 30 天 + drt- 1 年自动旋转）；PAT 导入（pt- 长期兜底）。两家族可共存 |
| **COSY 推理** | RSA 包 AES 会话密钥 + MD5 请求签名，对接 CN / Intl 各自 gateway，SSE 流式 |
| **动态模型** | COSY 拉取模型目录，静态目录兜底；注册 ID 带 `qoder/` 前缀 |
| **每日签到** | 面板手动签到（单账号/批量）+ 定时自动签到，走上游 campaigns 领取系统 |
| **多行领取** | 支持 CLAIM_BENEFIT 与 VIEW_DETAILS 两类行；`claim_unverified` 可代领无法核实面值的行（默认关） |
| **积分面板** | 账号卡片：昵称 / 积分 / 计划 / 签到状态 / 操作 |
| **token 保活** | 定时刷新；按 token 前缀路由（drt- → deviceToken/refresh，jrt- → jobToken/refresh），PAT 永不劫持 OAuth 刷新 |
| **大上下文** | 客户端自带 system 时自动模板瘦身 + 消息逐字透传；上游判输入过大时给出明确指引，与积分问题严格区分 |
| **错误归类** | 网关 504 HTML 折叠为可操作提示；业务码 112 渲染为 `plan_gate` 行，冷却只作用于（凭据, 模型）对 |
| **auth 隔离** | 文件名前缀 `qoder-` 过滤（含收养的 `qoder-cn-`/`qoder-intl-` 旧文件） |

### 上游协议对齐说明

以下为与上游 CLI 客户端行为对齐的实现细节，排查问题时可参考：

- **桌面端协议**：CN/Intl 统一按官方桌面客户端 v0.4.3 协议登录（共享 `client_id`、
  CN 授权域 `qoder.cn`、Intl `qoder-app://`），修复 cn/init 签到与首登奖励不触发的问题。
- **真实机器身份**：campaigns/claim 按官方 runtime-info 形状携带真实机器身份；模拟
  `Cosy-Machine*` 头会被服务端风控行过滤（表现为「今日无可签领权益」），因此
  claim/reward/limited-number 一律不发该族头（风险层 503 `RISK_DEPENDENCY_UNAVAILABLE` 根因）。
- **billing 会话**：billing 走 web-session cookie + CSRF 握手，auth-reject 会打断陈旧快照；
  billing 端点绕过 host bridge（HTTP/2 EOF 根因）并禁用 HTTP/2，遵循 `config.yaml` 的 `proxy-url`。
- **流式首包门**：可选 `stream_head_timeout: <秒>`（默认关）。开启后异步流式在移交前先等首包判决，
  上游把账号级错误发成 HTTP 200 + 帧内错误时可按普通失败返回真实状态，宿主得以换号/冷却，
  而不是收到「成功的空回答」。
- **轮次持久化**：campaigns 轮次状态落盘（`qoder_campaign_rounds.json`），重启后不重复探测隐藏轮。

## 安装

### 订阅插件源（推荐）

在 CPA 的 `config.yaml` 中加入本仓并启用插件，宿主会自动拉取对应平台产物：

```yaml
plugins:
  enabled: true
  store-sources:
    - https://raw.githubusercontent.com/varcli/cpa-plugins/main/registry.json
  configs:
    qoder:
      enabled: true
```

重启宿主（或在管理面「插件」页安装）即完成装载。当前发布 `linux/amd64`、
`linux/arm64`、`darwin/arm64`、`windows/amd64` 四个平台。

### 从源码

```bash
cd plugins/qoder
CGO_ENABLED=1 go build -buildmode=c-shared -o qoder.so .   # windows 下 -o qoder.dll
```

### 配置字段

| 字段 | 类型 | 说明 |
|---|---|---|
| `checkin_auto` | bool | 每日 09:00 / 21:00 自动签到 CN 账号（默认 true） |
| `login_region` | enum | **新登录**区域：`cn`（默认）或 `intl`。已有账号保持自身 region；旧 `qoder-cn-`/`qoder-intl-` 文件自动收养。该键为粘性：仅显式出现时才改变 |
| `lifecycle_auto` | bool | CN 账号积分耗尽自动禁用，签到回血后自动恢复（默认 true） |
| `token_keepalive` | bool | 每日 22:00 刷新 access token，避免 Keycloak 离线会话过期（默认 true） |
| `scheduler_mode` | enum | 多账号选择：`off`（默认，交给 CPA 内置调度）或 `credits`（选剩余积分最高） |
| `models` | array | 可选模型列表，每项可含 id / name / alias / context / max_tokens / enabled / reasoning |
| `model_prefix` | string | 注册模型 ID 前缀（默认 `qoder/`），缺尾斜杠自动补 |
| `enable_model_prefix` | bool | 是否加前缀（默认 true）。关闭后使用裸上游 ID |
| `claim_unverified` | bool | 是否代领「面值不可核实」的签到行（默认 false） |
| `stream_head_timeout` | int | 流式首包门超时秒数，0 = 关闭（默认） |
| `usage_report_url` | string | CPAMP usage 上报地址覆盖（也可用 env `USAGE_REPORT_URL`） |
| `usage_report_key` | string | CPAMP admin key 覆盖（优先 env / secret 文件自动探测） |

模型前缀示例（注册 ID 形如 `qoder/<上游 id>`）：

```yaml
plugins:
  configs:
    qoder:
      enabled: true
      model_prefix: "qoder/"        # 默认值，可省略
      enable_model_prefix: true     # 默认值，可省略

# 别名按注册后的 ID（含前缀）匹配
oauth-model-alias:
  qoder:
    - name: qoder/qmodel_preview
      alias: qoder/qwen3.8-max
```

## 使用

### 方式一：OAuth 登录（推荐）

1. CPA 管理面板 → Auth 文件 → QoderWork OAuth 登录卡片
2. 浏览器打开授权链接 → 登录 qoder.com.cn（阿里云 SSO）→ 点 Continue 授权
3. 插件自动轮询拿 token 落盘（dt-/drt-），并自动领取 Pro 升级包（若 eligible）

### 方式二：PAT 导入

1. qoder.com.cn → 设置 → Personal Access Token → 创建（pt- 开头）
2. 插件面板（`/v0/resource/plugins/qoder/panel`）→ 右上角「导入 Qoder 凭证」→ 粘贴 PAT → 导入
3. 插件自动换 jobToken（jt-/jrt-）落盘

### 面板

`/v0/resource/plugins/qoder/panel`（需 management key）

- 账号卡片：积分余额 / 计划 / 签到按钮（签到后返回最新积分）
- 「全部签到」批量签到；自动签到开关（09:00/21:00）

## 凭证家族与刷新

```
auth 文件字段（可共存）：
  accessToken:   dt- (OAuth, ~30d) 或 jt- (PAT 交换, 24h)
  refreshToken:  drt- (~1y, 旋转)   或 jrt- (48h)
  personalToken: pt- (长期兜底，导入即永久)
```

- 刷新路由按 `refreshToken` 前缀：`drt-` → `deviceToken/refresh`；`jrt-` → `jobToken/refresh` → 失败 fallback PAT re-exchange
- `personalToken` 永不主动覆盖活跃 token，只做最终兜底
- host 15 分钟 auto-refresh + 插件 22:00 keepalive 均按此规则

## 模型

11 个静态模型（`qmodel_preview`、`qfmodel` 等）+ COSY 动态拉取。注册到 CPA 的
ID 统一带 `qoder/` 前缀，例如 `qoder/qmodel_preview`、`qoder/qfmodel`——
这让模型在 CPA 模型页归到 qoder 分组，也不会与其他插件/原生 provider 的同名
ID 冲突，与 kiro 的 `kiro/xxx` 形式一致。前缀由 `model_prefix`（默认
`qoder/`）控制，`enable_model_prefix: false` 可关闭并回到裸 ID。

执行器转发上游前会剥掉自己的前缀（宿主只剥凭据的 `auth.Prefix`，不剥插件
前缀）。CPA 侧别名按注册后的 ID 匹配，示例：`qoder/qwen3.8-max` →
`qoder/qmodel_preview`。落盘快照、发现缓存与限流登账仍以裸 ID 为键，无需
手工迁移。

思考模式：2026-09-19 起对齐上游——所有请求统一 `is_reasoning: true` + `source: "system"`（不支持思考的模型上游自动忽略）；客户端可传 OpenAI 风格 `reasoning_effort`（`low`/`medium`/`xhigh`），非法值/缺省走上游默认（medium）。`Qwen3.8-Flash`（`qfmodel`）为官方限时免费模型（2026-10 前），动态发现与静态目录均已收录。

## 开发

```bash
cd plugins/qoder

# 编译动态库
CGO_ENABLED=1 go build -buildmode=c-shared -o qoder.so .   # windows 下 -o qoder.dll

# 静态检查与单测
go vet ./...
go test ./...
```

`-buildmode=c-shared` 是 CGO 构建，交叉编译需要目标平台的 C 工具链，因此发布产物
由 CI 在各平台原生运行器上分别构建。

代码分层：

- `main.go` — C ABI 薄壳与方法分发；顶层持有版本字面量与 `ConfigFields` 声明
  （`scripts/release.go` 与 `scripts/dev-sandbox.go` 只扫描 `plugins/<id>/*.go` 顶层）；
- `campaign.go` / `checkin.go` — 签到与权益领取（campaigns 协议、多行领取、轮次持久化）；
- `machine_identity.go` — 真实机器身份（runtime-info 桥接）；
- `sign.go` / `encoding.go` — COSY 签名与编码；
- `host_bridge.go` / `proxy_policy.go` — 宿主 RPC 与直连代理策略；
- `model_prefix.go` — 模型前缀（本仓统一约定）。

## 相关

- 产物命名与校验规则见 [宿主产物契约](../../docs/reference/host-artifact-contract.md)
- 发版流程见 [插件发布](../../docs/how-to/plugin-release.md)
