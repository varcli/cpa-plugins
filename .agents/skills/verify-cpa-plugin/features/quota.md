# 额度查询与版本门禁

管理面板提供各渠道实时配额查询, 该能力由宿主版本门禁控制

## Sub-features

- `quota-providers` 宿主识别具备额度查询能力的已注册插件
- `quota-fetch` 通过管理接口拉取实时余额并返回格式化比例

## 断言子集

`dev-sandbox.go --checks quota` 断言「插件声明的额度能力」与「宿主注册的额度提供方」一致: 插件声明了 `QuotaProvider` 却不在提供方列表即失败, 未声明却混入也失败, 未声明的插件跳过 (改了 QuotaProvider 注册后点名它):

```bash
go run scripts/dev-sandbox.go -plugin <id> -checks load,quota
```

真实余额数值必须走下方 Driving it 的 `quota/fetch`, 沙箱离线断言不覆盖。

**判据陷阱**: 判「额度有没有被消耗」必须读上游原始数值, 不能看管理面文案。文案是插件用取整格式拼的 (如 `余额 %.0f credits` 把 99.5675 显示成 100), 而钱包型渠道没有总量上限, `remainingFraction` 由插件写死为 `1.0`, 根本不反映消耗; 有总量的渠道 `remainingFraction` 才是 `(limit-used)/limit` 的四舍五入结果。判断消耗一律回到 `/user/wallets` 的 `balance` 等上游原始字段。

**接口契约**: `quota/fetch` 的 `auth_index` 必填 (缺了返回 `400 auth_index is required`); 凭据不存在返回 `404 auth not found`; 渠道未实现额度返回 `501`; 插件执行失败返回 `502`。插件自带额度页走的是插件私有端点 `POST /v0/management/plugins/<id>/quota` (同一套 `auth_index` 语义), 面板额度页数据源即此。

## How to get to it (user POV)

- 管理界面额度页查看各渠道余额与额度条
- 概览列表显示对应渠道的剩余比例
- 管理面接口 `GET /v0/management/quota/providers` 与 `POST /v0/management/quota/fetch`

## Driving it

Preconditions:

- 宿主版本 `>= v7.2.159`
- 插件已启用且 `supports_quota` 为 `true`

- **查询额度提供方。** 确认宿主识别了插件的额度扩展点, 预期 `HTTP 200` 且列表含目标插件条目:
  ```bash
  go run scripts/management-api.go -base http://<host>:8317 -path /v0/management/quota/providers
  ```

- **拉取实时额度。** 按凭据索引取实时余额, 输出按分组给出剩余比例, JSON 字段为 `remainingFraction`。`auth_index` 的值取 `auth-files` 条目的 `auth_index` 字段 (8 位哈希, 如 `27f747038b35e8c7`), 不是文件 id:
  ```bash
  AUTH_INDEX=$(go run scripts/management-api.go -base http://<host>:8317 -path /v0/management/auth-files \
    | jq -r '[.files[] | select(.provider=="<id>" and .status=="active")][0].auth_index')
  go run scripts/management-api.go -base http://<host>:8317 -path /v0/management/quota/fetch -method POST -body "{\"auth_index\":\"$AUTH_INDEX\"}"
  ```
  断言返回真实数值级余额 (与该渠道定价量级对照), 不是 0 或空; 记录该数值供后续验收对照

## Gotchas

- 额度页全空且接口 404 -> 宿主版本低于 `v7.2.159`, 额度路由尚未实现, 升级宿主镜像
- 额度查询返回 502 -> 上游部分精确度数值带字符串格式, 插件须用兼容反序列化类型处理
- 多账号场景按凭据逐条 fetch, 两条凭据各自返回余额且互不影响 (验收清单硬门禁)
