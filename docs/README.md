# 文档索引

面向两类读者：**使用插件**（装什么、怎么配）看插件 README 与宿主产物契约；
**维护本仓**（改代码、发版）看 how-to 与 reference。

## 使用

- 根 [README](../README.md)：插件清单、`store-sources` 订阅、安装与使用总览
- 各插件 `plugins/<id>/README.md`：能力、配置字段、登录方式、管理面路由
- [host-artifact-contract.md](reference/host-artifact-contract.md)：产物命名与校验规则；
  插件装不上、核对包结构时读

## 维护

### how-to

- [plugin-release.md](how-to/plugin-release.md)：发布新版本（变更集 → publish → CI 回填哈希 → 线上验收）
- [local-verification.md](how-to/local-verification.md)：提交前本地验证；
  `go build` 对 cgo 顶层包为何不可信、tidy/gofmt 的坑、本机验不了什么

### reference

- [feature-check-list.md](reference/feature-check-list.md)：新增或审查 provider 类插件时逐条核对能力
- [host-artifact-contract.md](reference/host-artifact-contract.md)：宿主 `internal/pluginstore`
  对产物的硬性校验（资产名、包内条目、扩展名、checksums.txt）
