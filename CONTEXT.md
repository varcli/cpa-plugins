# CPA Plugin Distribution Hub

A central repository for aggregating, building, and publishing CLIProxyAPI plugins to a unified registry.

## Language

**In-Tree Plugin**:
A plugin whose source code is authored, versioned, and built directly within this repository under `plugins/`.
_Avoid_: Builtin plugin, internal module, native plugin

**External Plugin**:
A trusted third-party plugin whose source code is hosted in an external repository, declared in `external/` either by direct release reference or by self-hosted build recipe.
_Avoid_: Remote plugin, third-party module

**Plugin Registry**:
The aggregated `registry.json` catalog consumed by CLIProxyAPI through `plugins.store-sources`.
_Avoid_: Store index, plugin list, repository catalog

**Plugin Artifact**:
A platform-specific zip archive containing the compiled native dynamic library attached to a GitHub Release.
_Avoid_: Binary package, dynamic link library, release bundle

**Native Provider Plugin**:
A plugin that registers as an upstream provider with CLIProxyAPI, supplying authentication, model discovery, execution, and quota capabilities directly.
_Avoid_: Backend adapter, upstream middleware, proxy plugin

**Traffic Field Diff**:
A structured comparison between request headers and payload keys emitted by a plugin and those observed in real client traffic captures.
_Avoid_: Packet diff, wire check, traffic dump

**Static Manifest**:
A pre-compiled, embedded JSON document containing verified endpoints, headers, model specifications, and token budgets for zero-latency runtime consumption.
_Avoid_: Dynamic config, runtime schema, remote dictionary

**Protocol Baseline**:
The specific official client whose observed network traffic defines a plugin's request headers, payload shape, and auth handshake.
_Avoid_: Reference client, target version, compat target

**Identity Profile**:
The client persona a plugin impersonates when writing request headers, selected independently of how the credential was obtained.
_Avoid_: Header set, user agent mode, spoof level

**Login Profile**:
The client persona a plugin impersonates when performing the OAuth handshake, selected independently of the request header identity.
_Avoid_: Auth mode, login type, credential source

**Manifest Provenance**:
溯源字段，标记静态清单由哪次真实客户端抓包生成、何时生成的，供校验清单未被手改。
_Avoid_: manifest metadata, capture stamp

**Cosy Signature**:
Aliyun's proprietary protocol encryption and signing scheme for QwenWork, combining custom Base64 encoding, EndSwap permutation, raw RSA-1024 encryption, AES-128-CBC encryption, and 5-segment MD5 authorization headers.
_Avoid_: Aliyun sign, Qwen auth hash, custom TLS

**Device Flow**:
An authorization flow where the user authorizes the application in a standard web browser using PKCE and a device code/nonce, while the plugin polls for token issuance.
_Avoid_: Web OAuth, QR login, password grant

**Wallet-Primary Quota**:
A quota resolution strategy prioritizing active wallet balances over package usage when the upstream usage endpoint returns null or unallocated packages.
_Avoid_: Mock balance, fallback credits, virtual quota
