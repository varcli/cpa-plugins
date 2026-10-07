# Trae

CLIProxyAPI 的 Trae 统一 Provider 插件：**一个插件同时覆盖 Trae Code CN、Trae SOLO CN 与 Trae Intl 三个变体**，按账号路由、流式执行、每日签到、积分面板、token 自动保活。

每个账号按其 auth 文件里的 `variant` 字段路由；新增账号的登录变体由插件配置 `login_variant`（cn | solo | intl，默认 cn）决定。

## 功能

| 能力 | 说明 |
|---|---|
| **三变体合一** | Trae Code CN（`api.trae.cn` inline_chat）、Trae SOLO CN（`solo_work_lite`）、Trae Intl（`api.marscode.com` Web SOLO）共用一份代码，按账号变体路由 |
| **OAuth 登录** | 走官方客户端握手（GetLoginGuidance + AuthCode ExchangeToken），Cloud-IDE-JWT 鉴权，凭据由宿主托管 |
| **流式执行** | 真实 SSE 增量返回，含首包门与帧聚合，非流式请求聚合为单个 completion |
| **多账号池** | 多账号调度，按剩余积分选号；额度耗尽按冻结闸门处理，可手动解冻 |
| **每日签到** | 面板手动签到（单账号/批量）+ 09:00 定时自动签到，走 `checkin_credits` |
| **积分面板** | 账号卡片：昵称/积分/计划/签到状态/操作（签到/刷新/解冻），Intl 账号单独一栏 |
| **自动保活** | 每日 03:00 刷新 access token，避免会话过期 |

## 安装

### 从 Release（推荐）

在 CPA 的 `config.yaml` 中启用插件：

```yaml
plugins:
  enabled: true
  store-sources:
    - https://raw.githubusercontent.com/varcli/cpa-plugins/main/registry.json
  configs:
    trae:
      enabled: true
```

宿主会按 `registry.json` 拉取对应平台的产物并装载。

### 从源码

```bash
cd plugins/trae
CGO_ENABLED=1 go build -buildmode=c-shared -o trae.so .
```

### 配置字段

| 字段 | 类型 | 说明 |
|---|---|---|
| `checkin_auto` | bool | 每日 09:00 自动签到（默认 true） |
| `login_variant` | enum | **新登录**使用的变体：`cn`（Trae Code CN，默认）、`solo`（Trae SOLO CN）、`intl`（Trae Intl）。已有账号保持登录时记录的变体 |
| `app_version` | string | Intl 登录流程对外声明的客户端版本（默认 `3.5.66`）。www.trae.ai 授权页会拒绝过期的版本串——当登录被版本不匹配页弹回时，把它设成 intl 客户端当前上报的版本 |
| `callback_bind` | string | OAuth 回调监听地址（默认 `127.0.0.1`）。CPA 跑在 Docker 或远程主机时设为 `0.0.0.0` |
| `callback_port` | string | OAuth 回调固定端口（默认每次随机）。浏览器无法访问宿主 `127.0.0.1` 时，可把地址栏里失败的 URL 粘贴到面板 `<panel>/v0/resource/plugins/trae/panel` 的粘贴框 |
| `token_keepalive` | bool | 每日 03:00 刷新 access token（默认 true） |
| `models` | array | 可选模型列表，每项可含 id、name、alias、context、max_tokens、enabled |
| `model_prefix` | string | 注册到 CPA 的模型 ID 前缀（默认 `trae/`）。模型因此在 CPA 模型页归到 trae 分组，也不会与其他插件撞名。缺尾斜杠会自动补 |
| `enable_model_prefix` | bool | 是否给注册的模型 ID 加 `model_prefix`（默认 true）。关闭后回到裸 ID（与 0.12.x 行为一致） |

## 使用

### 登录

在管理面板的 Trae 页面点登录，浏览器完成授权后回调即落凭据。登录变体由 `login_variant` 决定；Docker 环境下建议同时设 `callback_bind` 与 `callback_port`。

`login_variant` 是**粘性**的：只有配置里显式出现该键才会改变它。宿主在 auth store 变动时可能用不带该键的配置重新下发 register/reconfigure，这种空配置不会把变体重置回 `cn`（Intl 登录不会中途被改道）。`app_version` 同理，只在新值非空时覆盖。

### CPA 自带的新增账号

CPA 自带的「新增账号」入口（v8：`/v8/management/oauth/auth-url?provider=trae`，v0：`GET /v0/management/trae-auth-url`）与插件面板走同一条登录流程，但回调能不能回到插件，取决于部署形态：

- Trae 授权页强制要求 `auth_callback_url` 形如 `http://127.0.0.1:<端口>/authorize`，插件因此总是自建回环监听并优先从该监听取码。浏览器与 CPA 同机时，自带入口可直接走完。
- 浏览器不在 CPA 宿主机上（远程 / Docker）时，回环回调到不了插件；自带界面**没有粘贴框**，请改用**插件面板**的粘贴框提交地址栏里失败的完整链接。
- 若宿主把回调重定向到了它自己的 `oauth-callback` 端点，插件会在轮询时读取 `<AuthDir>/.oauth-trae-<state>.oauth`。本轮修复：此前该回退被 `listener == nil` 门禁挡住，而实时登录一定会绑定监听，导致它永远不可达，自带流程会在整个 15 分钟 TTL 内一直 "pending" 直到过期。

### 管理面接口

插件在 `/v0/management/plugins/trae/` 下暴露以下路由：

| 路由 | 说明 |
|---|---|
| `GET /accounts` | 列出 Trae SOLO CN 账号（积分、计划、签到状态） |
| `POST /checkin` | 手动签到单个（`auth_index`）或全部账号 |
| `GET /credits` | 实时积分查询（单个或全部） |
| `POST /refresh` | 强制刷新所有账号 access token 并返回面板数据 |
| `GET /status` | 账号池状态：每个账号的冷却/禁用原因 |
| `POST /release` | 手动解除额度耗尽的冻结（1005/4008 / scan-zero） |
| `POST /import` | 导入 Trae 凭据 JSON（嵌套或扁平）到宿主 auth store |
| `POST /device/align` | 把某账号（`auth_index`）凭证的 `auth.deviceId` 对齐为服务端 `BoundDeviceID`；仅影响积分查询等 ug/pay 族请求画像，签到不受影响 |
| `GET /intl/accounts` | Trae Intl：列出账号（uid、昵称、token 过期时间） |
| `GET /intl/status` | Trae Intl：插件状态 |
| `POST /intl/import` | Trae Intl：导入凭据 JSON 到宿主 auth store |
| `GET /models/groups` | 按变体的模型目录快照（`?refresh=1` 重新发现） |

### 面板

浏览器访问 `/v0/resource/plugins/trae/panel`（宿主服务插件声明的 resource 页面）。

账号卡片上的「设备绑定」行来自 `CheckLogin` 探测：服务端绑定设备 `BoundDeviceID`、绑定状态（`BOUND` 等）以及与本凭证 `deviceId` 是否一致。**绑定值不一致不影响签到**——签到族请求（`checkin_credits/status`、`/claim`）每轮都携带新生成的随机 16 位 `x-device-id`，既不发送服务端绑定值也不发送本凭证 `deviceId`；不一致只影响积分查询等 ug/pay 族的请求画像。需要对齐时点卡片上的「对齐设备绑定」（仅在检测到不一致且服务端有绑定值时出现），插件会把凭证的 `auth.deviceId` 改写为服务端绑定值——绑定值一律由服务端实时取回，其余凭证字段（设备密钥对等）原样保留。若卡片显示「登录失效」，则需要退出重新登录。

## 模型

模型 id 统一带 `trae/` 前缀，并按变体加后缀命名空间，两层叠加：

| 变体 | 注册 ID 示例 |
|---|---|
| cn | `trae/glm-5.2` |
| solo | `trae/glm-5.2-solo` |
| intl | `trae/claude-sonnet-4-intl`、虚拟 `trae/auto`、`trae/work` |

前缀让模型在 CPA 模型页归到 trae 分组（此前动态发现的条目缺 `OwnedBy`，全部落进 `other`），后缀则保证同一上游模型名在 cn/solo/intl 三个凭据类之间不会互相路由。`cn` 与 `solo` 共享同一份 `solo_work_lite` 目录，Intl 使用自己的目录。

前缀由 `model_prefix` 控制（默认 `trae/`），`enable_model_prefix: false` 可关掉。宿主侧 `oauth-model-alias` / `oauth-excluded-models` 配置照常生效——排除项按**注册后的 ID**（含前缀与后缀）匹配。

执行器在转发上游前会剥掉自己的前缀（宿主只剥凭据的 `auth.Prefix`，不剥插件前缀）：CN/SOLO 走 `upstream.SanitizeModelName`，Intl 走 `intlupstream.resolveMode`。历史 `model_cache` 快照（裸 ID）与面板排除选择器在读取时会自动对齐到当前前缀，无需手工迁移。

## 开发

```bash
cd plugins/trae

# 编译动态库
CGO_ENABLED=1 go build -buildmode=c-shared -o trae.so .

# 单元测试
go test ./...
```

`-buildmode=c-shared` 是 CGO 构建，交叉编译需要目标平台的 C 工具链，因此发布产物由 CI 在各平台原生运行器上分别构建。

## 相关

- 产物命名与校验规则见 [宿主产物契约](../../docs/reference/host-artifact-contract.md)
- 发版流程见 [插件发布](../../docs/how-to/plugin-release.md)
