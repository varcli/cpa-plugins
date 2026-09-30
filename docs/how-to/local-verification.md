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

Windows 侧没有 C 工具链，但 WSL 里可以跑通 CI 的构建步骤：

```bash
cd /mnt/d/Varc/code-repos/cpa-plugins/plugins/<id>
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -buildmode=c-shared -o /tmp/<id>.so .
```

得到真实的 ELF 动态库，顺带验证「按 `<id>.so` 打包后 zip 根级只有一个 `.so`」这条宿主契约。

### 环境准备（Ubuntu-24.04）

发行版自带 `gcc`/`make`/`zip`/`curl`，只缺 Go。**apt 的 Go 版本过旧**（不满足
`go 1.26.0`），用官方 tarball；无密码 sudo 时装到 `/usr/local`，否则装用户级 `~/.local`：

```bash
curl -fsSLO https://go.dev/dl/go1.27.1.linux-amd64.tar.gz
mkdir -p ~/.local && tar -C ~/.local -xzf go1.27.1.linux-amd64.tar.gz
echo 'export PATH=$PATH:$HOME/.local/go/bin' >> ~/.bashrc
rm go1.27.1.linux-amd64.tar.gz
go version   # go1.27.1 linux/amd64
```

首次构建要拉全部依赖，kiro/qoder/workbuddy 的依赖较大（含 `modernc.org/sqlite`），
耗时可观，建议后台跑。

### 从 Windows 侧驱动 WSL 的两个坑

- **Git Bash 会改写路径**：`wsl.exe -e /bin/bash` 里的 `/bin/bash` 会被 MSYS 转成
  `D:/.../usr/bin/bash` 导致 `execvpe ... failed`。加 `MSYS_NO_PATHCONV=1` 前缀即可。
- **`wsl.exe` 的 stdout 经管道会变成 UTF-16LE**：让脚本内部 `exec > /mnt/d/.../<file>`
  自己落盘，再从 Windows 读那个文件，比解析管道输出省事得多。

### 产物别落进仓库树

脚本与产物都写到 `/mnt/d/...` 下的临时目录或 `/tmp`，**不要落进仓库树**——
`.so`/`.h`/`.zip` 都不该被提交。跑完记得清掉，并确认 `git status` 干净。

### 其他平台

- `linux/arm64`：可用 `zig cc -target aarch64-linux-musl` 交叉编译（Alpine 无 aarch64
  交叉 gcc，zig 是可行替代）。
- `darwin/arm64`：本机仍验不了——zig 能产 Mach-O，但链接需要 macOS SDK
  （`unable to find dynamic system library 'resolv'`），只能交给 CI 的 macos-14。

## 本机验不了什么

按上一节配好 WSL 后，`linux/amd64` 与 `linux/arm64` 的 c-shared 产物可以本地验。下列仍
只能在 CI 侧完成，汇报时应标注「未本地执行」而非「已通过」：

- `darwin/arm64` 的 c-shared 产物（需 macOS SDK）
- `scripts/dev-sandbox.go` 的沙箱断言（需缓存宿主二进制）
- `release.go publish`（需 git remote）

## 相关

- 门禁与提交约定见 [插件发布](plugin-release.md)
