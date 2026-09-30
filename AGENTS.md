# CPA-PLUGINS

聚合与分发 CLIProxyAPI 插件的统一 Monorepo 仓库。

## 布局

- `plugins/`: 自研插件源码目录，每个插件拥有独立子目录、`plugin.json` 与 Go 模块
- `external/`: 外部可信插件的声明文件，通过 JSON 直接引用上游 Release 或自建构建配方
- `scripts/`: 核心维护与校验脚本
  - `build-registry.go`: 扫描子目录声明并合并生成根目录 `registry.json`，支持 `--check` 一致性检查
  - `verify-registry-install.go`: 端到端拉取清单、下载 Release 压缩包并校验 SHA256 与 ELF 动态库格式的验证工具，支持按插件 ID 参数化校验
- `registry.json`: CPA 宿主通过 `plugins.store-sources` 直接订阅的单一聚合清单文件
- `.github/workflows/`: 跨平台 CI 工作流，负责清单校验以及按标签自动发布 Release 产物

## 约束

- 因为有些插件不适合开源，工作流在必要时引入私有仓库进行构建
- 为每个插件编写独立的 `README.md`，编写前阅读相关 SKILL
- 每次提交前，检查对应插件 `README.md` 是否需要更新
- 严禁在任何 调试/测试/bash 中直接显式读取和使用secret/apikey; 只能隐式读取和使用(例如环境变量).
- 设计之前必须查看 `docs/reference/feature-check-list.md` 进行核对对应类型的插件的功能需求

## 注意

- 关于provider类的插件, 统一在注册时带上模型前缀, 且带上配置开关, 避免和其他同id混用
- 每次提交前, 检查插件, 如果插件是`provider`类型的插件, 检查模型 list 是否仍然与上游一致 (本仓插件走动态发现, 无静态清单可同步), 如有偏差向用户汇报

## 快速指路

- 插件发版/发布: 阅读 `docs/how-to/plugin-release.md`（变更集升版本、本地打包预检、推标签触发 CI 回填哈希的完整流程）
- 文档地图: `docs/README.md`（分类与阅读顺序）

## 宿主程序

- [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI): 权威主程序，一般源码会在本地仓库 `{kaiyuan-dir}/CLIProxyAPI` 中
