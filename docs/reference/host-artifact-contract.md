# 宿主产物契约

适用场景：改动产物打包方式或排查「插件装不上」时，核对产物是否满足宿主的硬性校验。

依据：宿主 `internal/pluginstore`（CLIProxyAPI 仓库）。本仓的 `release.go pack` 按此契约自检，任一条不满足时安装直接失败。

## 四条硬校验

| 项 | 要求 |
| :--- | :--- |
| 资产名 | `<id>_<version>_<goos>_<goarch>.zip` |
| 校验文件 | 必须叫 `checksums.txt` |
| 包内条目 | 只能有一个动态库，位于压缩包根级，名为 `<id><扩展名>` |
| 平台扩展名 | linux 用 `.so`，darwin 用 `.dylib`，windows 用 `.dll` |

## 相关错误

| 错误 | 原因 |
| :--- | :--- |
| `artifact checksum missing` | `install.artifacts[].sha256` 为空，哈希未回填 |
| `artifact checksum mismatch` | 哈希与下载产物不一致，通常是用了本地 `pack` 产物回填 |
