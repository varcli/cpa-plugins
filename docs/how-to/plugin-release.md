# 插件发布

适用场景：改动了 `plugins/<id>/` 下的代码，要把新版本发出去。

## 两条路径

| 场景 | 你要做的 |
| --- | --- |
| 改插件代码 | 写变更集 → 提交 → 提 PR（不碰版本号） |
| 发版 | 在 `main` 上跑一条命令 |

## 1. 写变更集

在 `plugins/<id>/changesets/` 下新建 json 文件，文件名自取，建议带日期与主题：

```json
{"bump": "patch", "note": "一句话描述改动"}
```

`bump` 取 `patch`、`minor`、`major` 之一，多个变更集共存时取最高档。变更集是唯一手写的东西：
`plugin.json` 的 `version`、产物地址与 Go 版本字面量都由脚本产出，不要手改。

## 2. 发版：一条命令

```bash
git pull --ff-only origin main
go run scripts/release.go publish --plugin <id>              # 发
go run scripts/release.go publish --plugin <id> --dry-run    # 先看会发生什么（不写文件、不推送）
```

脚本按固定顺序做完，任一步失败即停：

```
check-plugins 门禁 → version（消费变更集） → build-registry → git commit → git tag <id>/v<X.Y.Z> → git push main → git push tag
```

顺序不是可选项：先提交后打标签，CI 收到标签时提交才一定在 `main` 上；版本产出的同时就把新地址写进
`registry.json`，标签必须跟着推，否则 registry 指向不存在的资产。

不满足前置条件时脚本直接拒绝，且不写任何文件：

- 当前分支是 `main`
- 工作区没有未提交的已跟踪改动
- 不落后于 `origin/main`（本地领先允许：上次提交成功但推送失败时，重跑接着推）
- 有未消费的变更集；若已被消费，本次只补提交与标签的推送，可重复执行

## 3. 本地预检产物（可选）

```bash
go run scripts/release.go pack --plugin <id> --out dist
```

`pack` 只支持当前平台，产物按宿主契约命名并自检。此时 `sha256` 待回填属于预期警告，不要用 `--release-ready`。

## 4. 多插件发版

逐个跑 `publish`，等上一条流水线的 `record` 作业完成回填后再发下一个，避免并发回填互相覆盖。

## 5. CI 回填哈希

标签触发构建发布后，工作流的 `record` 作业自己下载产物、跑 `release.go record`，再把 `plugin.json` 与 `registry.json` 的改动提交回 `main`。哈希只能由真实上传的产物得出，本地 `pack` 的产物不能用来回填。

CI 绿后 `git pull --ff-only origin main`，确认 `registry.json` 中该插件版本与哈希已就位：

```bash
jq '.plugins[] | select(.id=="<id>") | .version, .install.artifacts[0].sha256' registry.json
```

若 `record` 作业失败，在同一提交上重打标签触发全新流水线，由 CI 的 `record` 作业重新回填，禁止本地代填：

```bash
git push origin :refs/tags/<id>/v<X.Y.Z>
git tag -d <id>/v<X.Y.Z> && git tag <id>/v<X.Y.Z>
git push origin <id>/v<X.Y.Z>
```

## 6. 线上验收

```bash
go run scripts/verify-registry-install.go <id>
```

通过后在宿主侧把插件更新到新版本，再用管理面确认：

```bash
go run scripts/management-api.go -base http://<host>:8317 -path /v0/management/plugins \
  | jq -c '.plugins[] | select(.id=="<id>") | {version: .metadata.version, config_fields}'
```

判据是 `version` 等于新标签的版本号，且声明过 `ConfigFields` 的插件 `config_fields` 非空。两者缺一，说明宿主仍跑旧产物。

若宿主由 GitOps 管理，先确认 Flux 跟踪的分支，再更新该分支的插件版本；有其他未提交修改时使用独立 worktree。不要仅改未被 Flux 跟踪的本地分支

## 7. 卡住了怎么办

| 现象 | 处理 |
| --- | --- |
| `check-plugins` 提示 `插件 <id> 没有对应标签` | 就是没发完：按提示跑 `publish` 补上提交与标签 |
| `publish` 提交成功但 push 失败 | 直接重跑 `publish`：它检测到本地领先，会补推提交与标签 |
| 标签推错、要重发同一版本 | 见第 5 节的删标签重推 |
| `record` 作业失败 | 在同一提交上重打标签触发全新流水线，禁止本地代填哈希 |
| `publish` 长时间无输出或超时 | 先查 `git status --short`、`git tag -l '<id>/v*'` 和最新提交，确认是否已消费变更集或创建标签；不要盲目重复。仍未写入版本时，分别运行 `go run scripts/check-plugins.go`、`git fetch origin main` 缩小阻塞位置，再运行可重复的 `publish` |
| 宿主仓库的提交钩子提示工具不存在 | 若该仓库使用 mise，在仓库内通过 `mise exec -- pre-commit run` 检查已暂存文件，并在同一 mise 环境提交；不要跳过钩子 |

## 底层子命令

`publish` 只是把下面这些串起来；排查时可以单独跑：

| 子命令 | 作用 |
| --- | --- |
| `version --plugin <id>` | 消费变更集，写 `plugin.json` 的版本与各平台产物地址、Go 版本字面量 |
| `pack --plugin <id> --out dist` | 本机打包预检（命名与结构） |
| `record --plugin <id>` | 用 dist 里的 zip 回填哈希并重建 registry（正式回填由 CI 做） |
| `publish --plugin <id>` | 一条命令做完上面全部，另加门禁、提交、打标签、推送 |

## 相关

- 契约背景见 [变更集驱动的发布流程说明](plugin-release.md)
- 产物命名的硬性要求见 [宿主产物契约](../reference/host-artifact-contract.md)
