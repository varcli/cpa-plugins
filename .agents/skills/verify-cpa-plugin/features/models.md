# 模型注册与可用性

插件装载后向宿主上报模型清单 (静态声明或动态发现), 并经标准 API 暴露给调用方

## Sub-features

- `models-serve` 宿主对外接口包含插件上报的模型
- `models-blacklist` 带名单过滤的插件 (如 workbuddy) 按自身规则剔除官方隐藏模型后再上报; 不做过滤的插件全量直报, 无此项
- `models-metadata` 上下文长度与输出上限与插件声明的一致 (动态发现的插件以官方返回值为准)

## 断言子集

`dev-sandbox.go --checks models` 覆盖 `models-serve` 的「声明的必须都在」方向 (改了静态清单或注册过滤后点名它, 常与 `load` 同跑)。本仓当前三个 provider 插件 (workbuddy/trae/qoder) 都走动态发现, 无 `data/static-config.json`, 该断言对它们退化为「不比对」:

```bash
go run scripts/dev-sandbox.go -plugin <id> -checks load,models
```

比对规则是「裸 id 或带插件前缀 id 命中其一即算在列」; 插件无静态清单时跳过比对 (不算失败)。反向方向不覆盖: 断言只判「声明的没少」, 不判「该剔除的没多」, 黑名单的负面判据见下方 Driving it 与插件单测。多处不一致时报错文案为 `静态清单声明了 N 个模型, 但 /v1/models 少了 M 个: <名单>`。断言按注册顺序在各插件间 fail-fast: 前一个插件报错即终止后续插件比对。

多插件共生时一个宿主只拉一次 `/v1/models`, 各插件分别比对; 断言名与描述以 `--checks list` 为准。

`models-metadata` 无脚本断言, 按下方 Driving it 的对照法人工核。

## How to get to it (user POV)

- 客户端配置宿主地址后获取 `/v1/models` 列表
- 客户端模型下拉框直接看到该渠道的模型名

## Driving it

Preconditions:

- 插件已启用 (`effective_enabled` 为 `true`)
- 本地沙箱无需凭据即可访问; 生产实例访问 `/v1/models` 须用 `-auth client`, 客户端密钥由脚本就地取得

- **核对在列模型。** 拉取完整模型集合, 输出须包含 `data/static-config.json` 声明的全部模型项 (插件启用模型前缀时为 `<id>/<model>` 形态):
  ```bash
  go run scripts/management-api.go -base http://<host>:8317 -auth client -path /v1/models | jq -r '.data[].id'
  ```

- **验证黑名单过滤。** 被剔除的模型不得对外暴露, 预期无输出。正则须兼容前缀形态, 行首行尾定界会漏判 `workbuddy/auto` 这类带前缀泄漏:
  ```bash
  go run scripts/management-api.go -base http://<host>:8317 -auth client -path /v1/models \
    | jq -r '.data[].id' | grep -E '(^|/)(auto|default|hunyuan-3b)$'
  ```
  workbuddy 的名单与前缀规则见 `plugins/workbuddy/models.go` 的 `isModelAllowed` (其余插件无此函数, 直报全量); 名单随官方客户端版本变化, 以代码为准
- **核对元数据。** `/v1/models` 只返回 id 与归属, 上下文长度等元数据以插件声明与单测为准: 有静态清单时对比 `data/static-config.json` 中该模型的声明; 动态发现的插件以官方接口返回值为准。无论哪种, 都跑 `cd plugins/<id> && go test ./...` 中钉住元数据映射的用例

## Gotchas

- 客户端模型列表看不到插件模型 -> 检查宿主配置 `plugins.configs.<id>.enabled` 是否为 `true`
- 静态配置已改但模型列表未更新 -> 静态清单经 Go embed 编译固化, 改完必须重新编译并替换动态库 (动态发现的插件无此问题, 但可能有缓存, 用 `?refresh=1` 或重启触发重新发现)
