# 插件装载与可视化配置

用户在管理面板查看已安装插件状态, 并修改插件声明的可视化配置字段

## Sub-features

- `plugin-load` 宿主按配置扫描并装载插件动态库
- `config-fields-render` 管理界面按插件上报的元数据渲染表单字段
- `config-update` 通过管理接口热更新插件运行时配置

## 断言子集

断言 id 由 `dev-sandbox.go` 的注册表 (`checkRegistry`) 声明, `--checks list` 打印权威清单, 不在此处复制。下表只补充脚本派不出来的语义: 每个断言对应哪些判据、何时该跑。

| 断言 id | 对应判据 | 何时该跑 |
|---|---|---|
| `load` | 宿主日志出现该插件 id 的 `plugin loaded plugin_id=<id>` 与 `plugin registered plugin_id=<id>`; 日志另有 `model registrar` + `context deadline exceeded` 时告警 | 改了 `plugin.json`、`main.go` 注册路径、SDK 版本 |
| `config` | 插件源码声明的配置字段 (注册元数据的 `ConfigFields`) 管理面全部回报; 未声明的插件跳过 | 改了 `ConfigFields` 声明 |
| `menus` | 管理面 `plugins` 列表暴露菜单 (面板侧边栏入口) | 改了菜单/入口声明 |
| `resource` | 管理面回报的每个菜单路径 (即插件声明的 `ResourceRoute.Path`) 都能返回 `200` 且内容含插件 id | 改了 resource 页面 |
| `models` | `/v1/models` 覆盖静态清单声明的全部模型 | 改了静态清单或模型注册 |
| `quota` | 插件声明的额度能力与宿主注册的额度提供方一致 (声明了必须在列, 没声明不得混入) | 改了 QuotaProvider |

`config` 与 `quota` 的判据都要两边对齐才成立: 插件没声明就跳过 (不算失败), 声明了却对不上才算失败。它们先读插件源码/宿主回报的声明, 再比对管理面结果, 因此不会因为响应为空而静默通过。

装载类改动至少点名 `load,models`; 只改配置字段声明时 `-checks load,config` 足够。

沙箱的 `-profile <值>` 会把该值写进插件的 `identity-profile` 与 `login-profile` 配置项, 用于验证「档位改变后上行请求随之改变」, 默认不写。

**加断言**: 写一个 `func(*sandbox, string) error`, 在 `checkRegistry` 注册一行; 每个断言对每个插件各跑一次, `--checks list`、解析与调度自动跟随。

## How to get to it (user POV)

- 浏览器打开 `http://<host>:8317/management.html` 进入插件管理页
- 管理面接口 `GET /v0/management/plugins` 与 `GET /v0/management/plugins/<id>/config`

## Driving it

Preconditions:

- 宿主服务正常运行, 管理面接口可达
- 生产实例经 `sec-run` 取密钥; 沙箱经 `-token-file` 取密钥

- **查询插件状态。** 确认装载与启用状态:
  ```bash
  go run scripts/management-api.go -base http://<host>:8317 -path /v0/management/plugins | jq '.plugins[] | select(.id=="<id>")'
  ```
  `registered` 为 `true` 且 `enabled` 为 `true`; 有配置字段时 `config_fields` 非空

- **查询当前配置。** 读取已生效的键值项:
  ```bash
  go run scripts/management-api.go -base http://<host>:8317 -path /v0/management/plugins/<id>/config
  ```

- **启用插件。** 未激活时提交写入, 响应 `HTTP 200`, 宿主热重载后装载:
  ```bash
  go run scripts/management-api.go -base http://<host>:8317 -path /v0/management/plugins/<id>/config -method PUT -body '{"enabled":true}'
  ```

- **复原。** 验证结束把改动过的配置 PUT 回原值

## Gotchas

- 页面提示「该插件没有声明可视化配置字段」 -> 先读 `metadata.version` 与 `config_fields`: `registered` 为 `false` 说明宿主没装载该二进制, `enabled` 为 `false` 说明配置里没启用; 两者都为 `true` 而 `config_fields` 仍为空, 说明宿主跑的产物版本早于声明 `ConfigFields` 的那次提交, 须升版本重新出包再更新宿主
- 动态库已在目录但 `registered` 恒为 `false` -> 宿主配置 `plugins.configs.<id>` 缺失或 `enabled` 为 `false`
- 配置文件里的明文密钥被替换成 bcrypt 哈希 -> 宿主预期防护行为, 真实明文经 `sec-run printenv CPA_TOKEN` 提取
