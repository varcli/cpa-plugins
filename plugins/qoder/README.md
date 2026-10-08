# Qoder CPA 插件

[CLIProxyAPI (CPA)](https://github.com/router-for-me/CLIProxyAPI) 的 Qoder 统一 Provider 插件：**一个插件同时覆盖 CN（qoder.com.cn）与 Intl（qoder.com）双区**，多账号 OAuth/PAT 双登录、动态模型、COSY 签名推理、每日签到、积分面板、token 自动保活。

每个账号按 auth 文件内的 `region` 字段路由（`qoder-cn-<uid>.json` → CN，`qoder-intl-<uid>.json` → Intl）；新增账号的登录区域由插件配置 `login_region`（cn | intl，默认 cn）决定。`login_region` 是**粘性**的：只有配置里显式出现该键才会改变它，宿主在 auth store 变动时下发的空配置不会把它重置回 `cn`（Intl 登录不会中途被改道）。

## 功能

| 能力 | 说明 |
|---|---|
| **双登录方式** | ① OAuth 设备授权（PKCE，浏览器授权，dt- 30 天 + drt- 1 年自动旋转）② PAT 导入（pt-，长期有效兜底）——两家族可共存于同一 auth 文件 |
| **登录自动领包** | OAuth 登录成功后自动判断并领取一次性 Pro 升级包（eligibility → claim） |
| **COSY 推理** | RSA 包 AES 会话密钥 + MD5 请求签名，按账号区域对接 gateway.qoder.com.cn（CN）/ api3.qoder.sh（Intl）SSE 流式 |
| **动态模型** | COSY 拉取 `/algo/api/v2/model/list`（chat scene），10 静态模型兜底 |
| **大上下文** | 客户端自带 system 时自动模板瘦身（省 ~10K token/请求）+ 消息逐字透传（tool_calls/多模态 content 保真）+ 客户端 tools 直通；上游判输入过大（413/过长文案）时给出明确指引，与账号积分问题严格区分 |
| **每日签到** | 面板手动签到（单账号/批量）+ 09:00/21:00 定时自动签到，签到后返回最新积分快照。v0.8.21：CN/Intl 统一走 campaigns 领取系统——上游已全局禁用 legacy CN daily-check-in（claim 恒 409 且不发积分，2026-09-21 实测）；legacy status 仅作 CN 只读统计补充。v0.8.22：billing 面全量携带桌面端 Cosy 身份头（User-Agent: Qoder / Cosy-ClientType: 10 / Cosy-Version: 0.3.4）——上游按该头门控 campaigns 响应，裸请求可能返回 showCampaign:false 导致当日权益静默漏签（bfSan 实测 2026-09-21）|
| **流式首包门** | 可选（v0.8.23，默认关）：`stream_head_timeout: <秒>` 开启后，异步流式在移交前先等首包判决——上游把账号级错误发成 HTTP 200 + 帧内 `statusCodeValue>=400`/`body.error` 时，移交前失败直接按普通失败返回并带上帧里的真实状态（缺失/越界落 502，不编造 401/403 之类会误导冷却的码），宿主得以换号/冷却而不是收到"成功的空回答"；真正开始吐内容后保持原样（含 in-band 错误），静默/超时等价于关闭态。0 = 逐字节沿用旧行为 |
| **网关故障页折叠** | 上游 ALB 在 ~60s 等不到首 token 时返回 504 HTML 页（大上下文请求易触发）——不再把 HTML 原样倒给客户端，而是折叠成一条可操作的重试/压缩上下文提示；其余 5xx HTML 页给通用网关故障文案。与账号积分无关，不会被误判成额度问题 |
| **plan-gate 分类** | 网关业务码 112（套餐额度/模型范围限制，message 里带 pricingUrl）渲染成一条可 grep 的 `plan_gate` 行；冷却只作用于（凭据, 模型）对——同账号其他模型照常可用，账号本身不判死 |
| **积分面板** | 账号卡片：昵称/积分/计划/签到状态/操作（签到/刷新/选用） |
| **token 保活** | 22:00 定时刷新；按 token 前缀路由（drt- → deviceToken/refresh，jrt- → jobToken/refresh），PAT 永不劫持 OAuth 刷新 |
| **auth 隔离** | 文件名前缀 `qoder-` 过滤（含收养的 `qoder-cn-`/`qoder-intl-` 旧文件），与 workbuddy 等其他插件互不干扰 |

## 安装

### 从 Release（推荐）

```bash
# 按你的平台下载（示例 linux/arm64）
unzip qoder_0.1.0_linux_arm64.zip
cp qoder.so /path/to/cliproxyapi/plugins/qoder.so
```

当前发布 `linux/amd64`、`linux/arm64`、`darwin/arm64`、`windows/amd64` 四个平台，扩展名分别为 `.so`/`.dylib`/`.dll`。

### 从源码

```bash
cd plugins/qoder
CGO_ENABLED=1 go build -buildmode=c-shared -o qoder.so .   # windows 下 -o qoder.dll
```

### config.yaml

```yaml
plugins:
  enabled: true
  configs:
    qoder:
      enabled: true

      # 注册到 CPA 的模型 ID 前缀（默认 "qoder/"，开关默认开）。
      # 注册 ID 形如 qoder/<上游 id>，例如 qoder/qmodel_preview，
      # 模型因此在 CPA 模型页归到 qoder 分组，也不会与其他插件/原生
      # provider 的同名 ID 冲突。enable_model_prefix: false 可关闭。
      model_prefix: "qoder/"
      enable_model_prefix: true

# 模型别名（可选）——按注册后的 ID 匹配，即带 qoder/ 前缀
oauth-model-alias:
  qoder:
    - name: qoder/qmodel_preview
      alias: qoder/qwen3.8-max
    - name: qoder/qmodel_latest
      alias: qoder/qwen3.7-max
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
