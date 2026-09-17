# CPA Codex Turn State 插件

这是一个 CLIProxyAPI（CPA）进程内动态插件，按 CPA 实际选中的 Codex 凭据注入并安全续期 `X-Codex-Turn-State`。

插件不会解密或生成 state。它只读取 Fernet 外壳中公开的签发时间与密文块数，也不会把完整 state 写入日志。

## 核心行为

- 在认证选择完成后读取 `Metadata["selected_auth_id"]`，每个凭据独立维护 state。
- 只对 Codex 请求注入，可配置模型范围。
- 观测普通 HTTP 和 SSE 流式响应头。
- 新 state 先成为候选，只有对应请求最终成功后才会提升。
- 拒绝格式错误、时间倒退、未来签发、过期或块数异常的候选。
- 可使用原子替换的私有 JSON 文件持久化自动续期结果。

v0.1 暂不自动续期 WebSocket state；HTTP 与 SSE Responses 已覆盖。

## 配置

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
      state_file: "state/codex-turn-state.json"
      credentials:
        "<selected_auth_id>":
          plan: team
          state: "<已知正常的 Fernet token>"
          models:
            - "gpt-5.6-*"
            - "gpt-6-astra"
```

默认正常基线：

| 套餐 | 正常密文块数 | 常见带 padding 长度 |
| --- | ---: | ---: |
| Pro / Plus | 10 | 292 |
| Team | 12 | 332 |

其他套餐需要显式填写 `normal_blocks`。未知套餐不会自动提升观测到的新 state。

完整构建、安全和安装说明见 [README.md](README.md)。
