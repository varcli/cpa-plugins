# CPA-PLUGINS

CLIProxyAPI (CPA) 插件聚合仓：统一维护、跨平台构建并分发插件。宿主通过订阅本仓的
`registry.json` 即可安装与更新，无需手工下载动态库。

## 插件清单

| 插件 | 说明 | 平台 |
| --- | --- | --- |
| **workbuddy** | 腾讯 CodeBuddy / WorkBuddy，CN + Global + Intl 三区合一 | linux amd64/arm64 · darwin arm64 · windows amd64 |
| **qoder** | Qoder，CN + Intl 双区合一，OAuth / PAT 双登录 | 同上 |
| **trae** | Trae，Code CN + SOLO CN + Intl 三变体合一 | 同上 |
| **kiro** | Kiro（AWS CodeWhisperer），凭证导入 + 多路登录 | 同上 |
| **cline** | Cline / ClinePass，WorkOS 设备码登录 | 同上 |
| **cpa-codex-autoban** | 外部插件：Codex 429 后自动禁用凭据 | 跟随上游 Release |

各插件的详细能力、配置字段与使用方式见 `plugins/<id>/README.md`。

## 安装

### 1. 订阅插件源

在 CPA 的 `config.yaml` 里把本仓加入 `plugins.store-sources`，然后按需启用插件：

```yaml
plugins:
  enabled: true
  dir: plugins                     # 动态库落盘目录（默认 plugins）
  store-sources:
    - https://raw.githubusercontent.com/varcli/cpa-plugins/main/registry.json
  configs:
    workbuddy:
      enabled: true                # 每个插件默认关闭，需显式启用
    qoder:
      enabled: true
    trae:
      enabled: true
    kiro:
      enabled: true
    cline:
      enabled: true
```

### 2. 让宿主安装产物

重启 CPA，宿主会按 `registry.json` 拉取当前平台对应的 zip、校验 SHA256 并装载。
也可以在管理面「插件」页点击安装/更新，无需重启。

### 3. 确认装载

管理面「插件」页能看到对应条目与版本；或调用管理面接口：

```bash
go run scripts/management-api.go -base http://<host>:8317 -path /v0/management/plugins \
  | jq -c '.plugins[] | select(.id=="workbuddy") | {version: .metadata.version, config_fields}'
```

判据：`version` 等于目标版本号，且声明过 `ConfigFields` 的插件 `config_fields` 非空。

### 4. 登录账号

在管理面侧栏点对应插件（如 WorkBuddy / Qoder / Trae）打开面板，按面板提示完成
OAuth 登录。凭据由宿主 auth store 托管，多账号可并存。

## 使用

- **模型调用**：注册的模型 ID 带插件前缀（如 `workbuddy/glm-5.2`、`trae/glm-5.2-solo`），
  在 CPA 模型页归到对应分组，也不会与其他插件撞名。可用 `model_prefix` /
  `enable_model_prefix` 调整或关闭。
- **别名与排除**：宿主侧的 `oauth-model-alias` / `oauth-excluded-models` 照常生效，
  按**注册后的 ID**（含前缀）匹配。
- **面板地址**：`/v0/resource/plugins/<id>/panel`。
- **手动安装**（不走 registry 时）：把对应平台的 `.so` / `.dylib` / `.dll` 放到
  `plugins/<goos>/<goarch>/` 或 `plugins/` 下即可，文件名需为 `<id><扩展名>`。

## 仓库结构

| 路径 | 说明 |
| --- | --- |
| `plugins/` | 自研插件源码，每个插件独立 Go 模块 + `plugin.json` |
| `external/` | 外部可信插件声明，直接引用上游 Release |
| `registry.json` | 聚合清单，宿主通过 `plugins.store-sources` 订阅 |
| `scripts/` | 构建、校验、发布、沙箱与端到端验证工具 |
| `.github/workflows/` | 清单校验与按标签自动构建发布 |

## 核心脚本

| 脚本 | 作用 |
| --- | --- |
| `build-registry.go` | 扫描 `plugins/` 与 `external/` 生成 `registry.json`；`--check` 校验一致性 |
| `check-plugins.go` | 仓库不变量检查与发布门禁 |
| `verify-registry-install.go` | 端到端校验：下载 Release 产物、核对 SHA256 与动态库格式（需传插件 id） |
| `dev-sandbox.go` | 编译插件、起宿主沙箱并断言装载与注册 |
| `release.go` | 消费变更集产出版本、按宿主契约打包、回填真实产物哈希 |
| `verify-chat.go` | 对真实上游发对话请求验证对话链路 |
| `management-api.go` | 调用宿主管理面接口 |

完整参数以脚本自身 usage 为准，例如 `go run scripts/release.go`。

## 贡献

- **新增插件**：在 `plugins/<id>/` 建独立 Go 模块，按
  [功能检查清单](docs/reference/feature-check-list.md) 核对能力，补齐 README 与
  `plugin.json`，跑 `go run scripts/build-registry.go` 更新清单。
- **引入外部插件**：在 `external/<id>.json` 声明上游 Release，不复制源码。
- **提交前**：跑 `go run scripts/check-plugins.go` 与
  `go run scripts/build-registry.go --check`；本地类型检查/单测用
  `bash scripts/typecheck.sh -t plugins/<id>`（`go build` 对 cgo 顶层包不可信，
  原因见[本地验证](docs/how-to/local-verification.md)）。
- **版本意图**：插件代码改动需带变更集，流程见[插件发布](docs/how-to/plugin-release.md)。
  提交信息用中文，遵循 conventional commits。

## 文档

- [docs/README.md](docs/README.md)：文档索引

## 许可证

MIT License
