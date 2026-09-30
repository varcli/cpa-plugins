# 对话链路

客户端把请求打到宿主, 流式回复逐帧返回, 非流式回复一次返回完整结果

## Sub-features

- `chat-stream` 流式请求按帧返回增量
- `chat-nonstream` 非流式请求由插件聚合上游流式后返回单条完整响应
- `chat-cot` 思维链以 `reasoning_content` 增量下发, 正文以 `content` 下发
- `chat-cache` 多轮与重复请求命中上游前缀缓存
- `chat-memory` 多轮里模型复现前文事实
- `chat-effort` 客户端 `reasoning_effort` 真实改变上游思考深度
- `chat-tools` 工具定义透传, 模型返回 `tool_calls` 并消费工具结果
- `chat-tool-choice` 客户端 `tool_choice` 与 `parallel_tool_calls` 原样上行
- `chat-model` 只接受宿主报送过的模型 id

## 场景与请求成本

场景 id 由脚本的注册表 (`scenarioRegistry`) 声明, `verify-chat.go -list` 打印权威清单与每项请求成本, 不在此处复制。下表只补充脚本派不出来的语义: 每个场景覆盖哪些判据、何时该跑。

| 场景 id | 覆盖的判据 | 何时该跑 |
|---|---|---|
| `session` | 流式帧合规、正文输出、上下文记忆、多轮缓存(轮次2/3)、用量上报 | 改了消息组装、会话头、请求体构造 |
| `probe` | 重复请求缓存(二次)、用量上报 | 改了缓存相关字段、请求幂等性 |
| `nonstream` | 非流式链路 | 改了流式聚合、`stream:false` 路径 |
| `tools` | 工具调用 流式/非流式、工具结果消费 | 改了工具字段透传、分片拼接 |
| `toolchoice` | 点名函数、`tool_choice=none`、`parallel_tool_calls=false` | 改了 `tool_choice` 或 `parallel_tool_calls` 的上行构造 |
| `effort` | 思维链输出、思考深度传递 | 改了推理档位映射、`reasoning_effort` 传递 |
| `guard` | 未知模型拒绝 | 改了模型注册、路由或清单装载 |

默认 (不传 `-scenarios`) 只跑 `session,nonstream`: 二者成本最低且覆盖请求构造主路径。场景按注册顺序执行, 与书写顺序无关。

**加场景**: 写一个 `func(s *session)` 跑请求并 `s.record(...)` 判据, 在 `scenarioRegistry` 注册一行。`-list`、成本合计、参数解析、调度自动跟随, 无需改 main, 也不要在此表外另抄一份清单。

## 判据

判据只在本场景被点名时执行 (见上一节的场景表); 已执行的判据里任一 `FAIL` 即对话链路不可用, 退出码 1。未点名的场景不执行也不报绿。

每个请求轮次都会先记一条健康判据 (`轮次1/2/3 流式`、`探针 首次/二次`、`低档/高档 <effort>`), 覆盖该次请求本身: 状态码、帧数、正文与推理字数; 请求失败或流式缺终止帧直接 `FAIL`。下面这组是链路级判据:

| 判据 | 通过条件 |
|---|---|
| 流式帧合规 | 每帧剥掉宿主补的 `data:` 后都是合法 JSON, 末帧为 `[DONE]` |
| 思维链输出 | 上限档推演轮的增量里 `reasoning_content` 非空; 上游自述 `reasoning_tokens>0` 而流里没有思考帧即 FAIL (插件丢帧) |
| 正文输出 | 复述轮 `content` 非空 |
| 上下文记忆 | 第二轮复现第一轮植入的暗号 |
| 用量上报 | 流式轮次的 `usage` 非空; 只在缺失时记 FAIL, 有则归入该轮健康判据的明细 (缓存判据的前置), 覆盖轮次2/3 与探针二次 |
| 多轮缓存(轮次2/轮次3) | 第二轮起缓存命中数大于 0 |
| 重复请求缓存(二次) | 同一请求连发两次, 第二次命中数大于 0 |
| 思考深度传递 | 最低档与最高档各采样 3 次比中位数: 高档中位大于低档为 PASS; 相等或反序记 WARN (上游未按档位加深, 透传契约由 plugins/<id> 单测钉住); 仅在两档所有采样都没有任何思考输出时 FAIL (思考链路失效)。全采样有 reasoning_tokens 时按 tokens 比, 否则退到推理正文字数 |
| 非流式链路 | 单条完整响应, `object` 为 `chat.completion`, 带 `finish_reason`; `usage` 缺失不判 FAIL |
| 工具调用 流式 | 带 `tools` 与 `tool_choice:auto` 的请求返回 `finish_reason:"tool_calls"`, 每个调用带非空 `id` 与函数名, `arguments` 是合法 JSON |
| 工具调用 非流式 | 同一请求走聚合路径后仍带 `tool_calls` 与合法 `arguments` |
| 工具结果消费 | 把首轮 `tool_calls` 与工具结果带回下一轮, 模型正文引用了工具返回的事实 |
| 点名函数 | `tool_choice` 用对象形态点名 `get_weather` 时, 返回的 `tool_calls` 恰为该函数且 `finish_reason` 为 `tool_calls`; 返回别的函数或直接答正文即 FAIL (客户端约束被丢弃) |
| `tool_choice=none` | 同请求带 `tool_choice:"none"` 时不得返回 `tool_calls`, 且要有正文或推理输出; 返回调用即 FAIL |
| `parallel_tool_calls=false` | 显式给出该字段的请求被上游接受 (HTTP 2xx), 且返回的调用数不超过 1 |
| 未知模型拒绝 | 未报送的模型 id 在路由阶段被拒 (400 `model_not_found`); 同沙箱内合法模型会走到凭据阶段 (无凭据时 503 `auth_not_found`), 说明二者处理阶段不同 |

缓存判据要有可命中的公共前缀才有意义, 脚本默认用重复段落撑出足够长的 system 消息, 段落数由 `-prefix` 控制

参数: `-base` 目标实例、`-model <plugin>/<model>`、`-manifest` 静态清单 (默认按模型 id 的插件段推导)、`-scenarios` 场景子集、`-prefix` 前缀段落数、`-timeout` 单请求超时 (默认 180s)、`-token-file` 沙箱密钥、`-list` 打印场景清单

## How to get to it (user POV)

- 任意 OpenAI 兼容客户端指向宿主, 选该渠道模型发消息
- 直接 `POST /v1/chat/completions`

## Driving it

Preconditions:

- 实例经 Doctor 确认可用, 插件 `registered` 且 `enabled`
- 真实对话需要可用凭据 (`auth-files` 中目标渠道 `status` 为 `active`); 
- 密钥: 生产经 `sec-run`, 沙箱经 `-token-file ~/.cache/cpa-plugins/sandbox/<id>/management-key`

- **按场景跑判据。** 脚本只执行点名的场景, 未点名的判据不执行也不报绿, 退出码 1 表示有失败项:
  ```bash
  # 改了请求构造: 只跑主路径两个场景
  go run scripts/verify-chat.go -base http://<host>:8317 -model <id>/<model> \
    -scenarios session,nonstream

  # 改了工具透传或推理档位: 追加对应场景
  go run scripts/verify-chat.go -base http://<host>:8317 -model <id>/<model> \
    -scenarios tools,effort

  # 发布门禁 / 协议层改动: 全量
  go run scripts/verify-chat.go -base http://<host>:8317 -model <id>/<model> \
    -scenarios all
  ```
  `-manifest` 可省略, 默认按 `-model` 的插件段推导为 `plugins/<plugin>/data/static-config.json`; 本仓三个 provider 插件都走动态发现, 无该清单, 元数据判据因此退化为「不比对」。要恢复, 用 `-manifest` 显式指定清单文件

- **工具调用是独立场景。** 无需手工发请求, 首轮带 `get_weather` 定义断言 `finish_reason:"tool_calls"` 与合法 `arguments`, 次轮带回调用与结果断言模型引用了结果; 只在点名 `-scenarios tools` 时执行

- **跑构造层单测。** 不依赖上游凭据, 改动请求构造后先跑 (这是零成本, 不需要选场景):
  ```bash
  cd plugins/<id> && go test ./...
  ```
  以 workbuddy 为例, `TestSSEPayloadStripsFramingPrefix` 钉住下发载荷只带一层帧头, `TestAggregateChatStreamRebuildsCompletion` 与 `TestAggregateChatStreamMergesToolCallFragments` 钉住非流式聚合

- **抓包差异审计。** 改过请求头或请求体后, 若对应上游有独立的抓包研究仓, 在该仓跑一次差异审计, 聊天接口不应有 `Missing` 项; 抓包版本须与本机客户端版本一致


