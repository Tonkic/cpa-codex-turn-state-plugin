# CPA Codex Turn State 插件 v0.3.0

CLIProxyAPI 的进程内 DLL 插件。保持现有 CPA 网关和业务代理，只让独立 state 探测使用代理池或链式代理。

## 工作方式

1. CPA 选定 Codex 账号及实际上游模型后，插件查找该“账号 + 模型”的 state。
2. state 缺失、过期或距过期不足 5 分钟时，发送独立的轻量 ping。第一次请求最多等待配置的探测总超时；其他并发请求不重复探测。
3. 探测使用当前账号的 OAuth 信息和相同模型，不发送用户的业务内容。只有 SSE 明确完成且 state 通过格式、时间及长度筛选后才缓存。
4. 业务请求注入该账号、该模型的有效 state，继续使用 CPA 原有代理。探测失败时复用仍有效的缓存；没有有效缓存时照常放行业务请求。
5. 业务响应也能提供新的候选 state，只有业务请求最终成功后才提升。

新账号/模型第一次实际被选中时按需获取。已有缓存则由后台主动维护：默认在签发后 55 分钟（到期前 5 分钟）开始获取新 state，无需业务流量触发。后台每 5 秒检查一次到期任务；重启或热更新会从 v2 缓存恢复计划。成功获得更新、合格的 state 后立即替换缓存，并按新签发时间重新计算下次刷新。

最终失败的 429、502/503/504、overload，以及 SSE 中的对应错误会排队提前刷新；内部重试事件也会为上次选中的账号/模型安排刷新，覆盖宿主没有逐次暴露 HTTP 错误的情况。错误回调不等待网络，不修改业务结果。单次刷新失败保留仍有效的旧 state；重复错误合并，每组默认最短间隔 60 秒。已明确额度耗尽的探测（usage_limit_reached / insufficient_quota）默认退避 900 秒，刷新 state 不能恢复账号额度。

后台只维护已知缓存/错误触发的账号模型，不会枚举未知模型。禁用或删除的账号不会继续发送上游探测。进程停止/热替换时会取消并等待后台任务退出。过期 state 默认不注入。background_refresh 和 refresh_on_errors 默认开启，设为 false 可关闭对应功能。

长度只是配置的筛选规则，不代表已经验证“高算力”，本插件不做智力测试、不解密 state，也不保证上游一定签发 state。

## 已验证的 HTTP proxy 配置

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    cpa-codex-turn-state:
      enabled: true
      priority: 100
      auto_update: true
      inject_expired: false
      state_file: "state/codex-turn-state-v2.json"
      defaults:
        accepted_blocks: [10, 12]
      probe:
        enabled: true
        background_refresh: true
        refresh_on_errors: true
        quota_backoff_seconds: 900
        timeout_seconds: 15
        retry_seconds: 60
        refresh_before_seconds: 300
        max_attempts: 1
        first_proxy:
          url: "socks5h://127.0.0.1:1080"
        proxy_pool:
          - url_file: "state/proxy-pool.url"
            connect_host: "proxy.example.invalid:8080"
```

在私有文件 `state/proxy-pool.url` 放入一行：

```text
http://<URL编码后的用户名>:<URL编码后的密码>@proxy.example.invalid:8080
```

该文件不提交 Git，限制为 CPA 服务账号可读。文件按每次拨号读取，更换地址/凭据不需要重启。也可使用 `url_env` 指定环境变量名称，或使用 `url` 直接配置 URL；三者必须且只能选一个。

链路直接在 DLL 内完成：

```text
插件探测 → 1080 SOCKS5 → HTTP proxy HTTP CONNECT → Codex HTTPS
业务请求 → CPA 原有代理 → Codex HTTPS
```

不需要 1081、Python sidecar 或更换网关。`connect_host` 等价于已验证 curl 的 `--proxy-header Host: ...`，修复第一跳 HTTP 嗅探覆盖 HTTP proxy 目标的问题；CONNECT 目标和网站 TLS/SNI 保持真实目标。只在已验证需要的 HTTP 代理上配置此字段。

代理池可增加多条 HTTP、HTTPS、SOCKS5 或 SOCKS5H URL；每轮轮询起点，失败/无可接受 state 时最多尝试 `max_attempts` 条，所有尝试共享总超时。不会绕过配置的代理直连。URL 中可放 `{session}`，每次拨号替换为 8 位随机十六进制字符，适用于 HTTP proxy 的 sid 格式。动态 IP 实际是否变化由代理商控制。

支持 IPv6 字面地址，例如 `socks5h://[::1]:1080`；SOCKS 目标域名交给代理解析。IPv6 公网出口取决于代理商，本次没有验证 IPv6 出口。仅代理探测 TCP 流量，不是整机 TUN/UDP 代理。

## 隔离、筛选与迁移

| 套餐 | 正常密文块数 | 常见带 padding 长度 | 拒绝的异常长度 |
| --- | ---: | ---: | ---: |
| Pro / Plus | 10 | 292 | 312 |
| Team | 12 | 332 | 356 |

`defaults.accepted_blocks: [10,12]` 可自动覆盖未知账号；明确账号套餐时，在 `credentials.<selected_auth_id>.plan` 设置 plus/pro/team 以收紧规则。其他套餐可指定 `normal_blocks` 或 `accepted_blocks`。不使用 160 的示例阈值。

v0.1.x 缓存只记录账号，没有模型信息，v0.2.0 不会复用它。建议备份旧文件并使用新的 v2 文件路径，重新按账号/模型获取。手动 seed 现在必须同时提供精确的 `state_model`；`models` 只是适用范围，不能赋予 seed 跨模型复用能力。

默认不注入过期 state。保留 `inject_expired` 兼容选项，但建议始终为 false。未知来源的客户端 state 在已纳入管理的账号/模型上会被清除或替换。

## 查看状态

带 CPA 管理密钥访问：

```text
GET /v0/management/codex-turn-state/status
```

返回版本、后台启用状态、账号匿名摘要、模型、state 长度、签发/到期时间、next_refresh_at、refresh_pending、last_probe_reason 和最近探测结果；不返回 state、账号 token 或代理密码。例如 `upstream_http_429_usage_limit_reached` 表示已连接 Codex，但账号额度限制导致探测失败。next_refresh_at 是考虑冷却/额度退避后的下一次可尝试时间；排队可能带来少量延迟。

当前支持 Codex OAuth、标准 ChatGPT Codex endpoint、HTTP/SSE；不对 API-key/自定义 base_url 账号发独立探测，不主动刷新 OAuth token，token 刷新继续由 CPA 负责。WebSocket state 捕获仍未实现。

## 构建和验证

需要 Go 1.26+ 与 Windows amd64 GCC（cgo）。

```powershell
go test -race ./...
go vet ./...
./scripts/build-windows.ps1
```

DLL 输出到 `dist/windows-amd64/cpa-codex-turn-state.dll`。安装路径及标准 ABI 说明见 [README.md](README.md)。

从 v0.2.0 升级可直接沿用 v2 缓存。运行中升级使用版本化文件名 `cpa-codex-turn-state-v0.3.0.dll`：插件 ID 仍为 `cpa-codex-turn-state`，新的路径让宿主执行真正的 DLL 热替换；仅覆盖同路径文件并重新加载配置可能仍使用旧 DLL。保留旧 DLL 作为回退。

实测 Go 插件链式传输到 HTTPS IP 查询成功。真实 Codex 探测中，一些账号返回 429，已确认一次错误为 usage_limit_reached；另一个账号完整完成并返回 356 字符、13 块的 state，按当前策略拒绝。网络/捕获成功不等于取得可接受的 state。单元测试验证隔离、池切换、失败复用、SSE 成功判定、取消和 IPv6 编码；原生 DLL ABI 和 CPA 7.3.6.1 宿主加载也已验证。
