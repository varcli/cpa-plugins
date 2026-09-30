# 本地验证

适用场景：在本机改完 `plugins/<id>/`，提交前想知道「到底验了什么、还有什么没验」。

## 一条命令

```bash
bash scripts/typecheck.sh -t plugins/<id>   # 类型检查 + 单测
```

## 为什么不能只跑 go build

插件的 C ABI 外壳是 cgo（`main.go`；trae 另有 `intl_main.go`）。本机 `CGO_ENABLED=0`
且无 C 工具链时，`go build ./...` / `go vet ./...` 对顶层包**不是失败，而是根本不编译**，
按顶层文件的构成有两种表现：

| 顶层文件构成 | 表现 | 危险度 |
| --- | --- | --- |
| 全是 cgo 文件（cline、kiro） | 顶层包被静默跳过，退出码 0 | **假绿**：main.go 里写错也看不出来 |
| 混有非 cgo 文件（qoder、trae、workbuddy） | 引用 cgo 文件里的符号时报 `undefined: storedAuth` 等 | 假红：像是坏了，其实只是没编 |

`scripts/typecheck.sh` 把插件复制到临时目录，剥掉 cgo 前导块与 `C.` 限定符，补一个纯 Go
shim，让整个顶层包真正参与 `go build`/`go vet`（`-t` 再加单测）。它抓到过真实编译错误
（cline 的 `ConfigFieldTypeBool` 应为 `ConfigFieldTypeBoolean`；kiro 的 `kironx.Error`
应为 `kirorpc.Error` 且返回值个数不同）——这些在普通 `go build` 下完全不可见。

## gofmt 与行尾

本仓 `plugins/**/*.go` 是 LF，但这台机器的编辑器/工具会把改动过的文件写回 CRLF，于是
`gofmt -l` 会把整个文件列出来（gofmt 把 CRLF 当格式差异），`git diff` 也显示成整文件重写。

判断某文件是否真被 gofmt 抱怨：先把 `\r\n` 换成 `\n` 再跑 `gofmt -l`，剩下的才是真格式
问题。（`scripts/*.go` 在 HEAD 就是 CRLF，`gofmt -l` 一直会列它们，属既有噪声。）

## go.sum 与 go mod tidy

`go mod tidy` 不受 `CGO_ENABLED` 影响，且会考虑全部平台（`//go:build linux` 的文件在
Windows 上也计入）。所以本机跑 tidy 是安全的。

tidy 会**清掉失效条目**：cline 的 `go.sum` 从 51 行降到 2 行，删掉的是 `modernc.org/sqlite`
及其传递依赖——那是 kiro 的依赖，复制进 cline 时留下的残留，cline 从不 import。收缩是
清理，不是丢依赖。确认办法：

```bash
cd plugins/<id>
CGO_ENABLED=1 go list -deps -mod=readonly ./...   # 三个 CI 目标都过才算齐
CGO_ENABLED=1 go mod verify
```

## 想真正跑一次 c-shared 链接：走 WSL

Windows 侧没有 C 工具链，但如果有可用的 WSL 发行版，可以在里面补 Go + gcc 跑通 CI 的
构建步骤（`CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -buildmode=c-shared -o <id>.so .`），
得到真实的 ELF 动态库，并顺带验证「按 `<id>.so` 打包后 zip 根级只有一个 `.so`」这条宿主契约。

要点：

- 用 `wsl.exe -d <发行版> -e /bin/sh <脚本>` 执行；脚本写到 `/mnt/d/...` 下，**产物写 `/tmp`**，
  不要落进仓库树（`.so`/`.h`/`.zip` 都不该被提交）。
- `wsl.exe` 的 stdout 经管道会变成 UTF-16LE；让脚本内部 `exec > <文件>` 落盘再读更省事。
- `linux/arm64` 可用 `zig cc -target aarch64-linux-musl` 交叉编译。
- `darwin/arm64` 本机仍验不了：zig 能产 Mach-O，但链接需要 macOS SDK，只能交给 CI 的 macos-14。

## 本机验不了什么

无缓存宿主、无 git remote（Windows 侧也无 C 工具链，除非按上一节走 WSL），因此下列只能在
CI 侧完成，汇报时应标注「未本地执行」而非「已通过」：`darwin/arm64` 的 c-shared 产物、
`scripts/dev-sandbox.go` 的沙箱断言、`release.go publish`（需要 remote）。

## 相关

- 门禁与提交约定见 [插件发布](plugin-release.md)
