# Notion2API

一个基于 Go 的 Notion AI OpenAI 兼容桥接服务，提供标准 API、WebUI 管理面、多账号池和本地 SQLite 持久化，方便本地部署、调试和统一接入。

## 功能概览

- OpenAI 兼容接口：`/v1/models`、`/v1/chat/completions`、`/v1/responses`
- 支持流式响应
- 支持多账号池、账号切换、登录态刷新
- 支持图片、PDF、CSV 等附件请求
- 自带 WebUI 管理面：`/admin`
- 使用 SQLite 持久化账号、会话和运行状态
- 纯 Go 自动注册机（无 python/playwright 依赖）：
  - tempmail 链：`mail.tm`（tokend 直连）/ AdGuard tempmail（预置邮箱 cookies 复用）
  - 完整协议链：signup → getLoginOptions → sendTemporaryPassword → 收码(数字码/魔链 token 均支持) → loginWithEmail → 空间获取(getSpacesInitial / loadUserContent / selfJoinSpaceByDomain / createspace) → getAvailableModels → probe 落盘
  - 邮件延迟/invalid_or_expired_password 自动换码重试（内层 3 轮）
- 工作空间配额恢复轮换（space_pool）：
  - 每账号预建 N 个空间（默认 3，节流 10min/次）
  - 推理遇额度归零 → 写 cooldown（默认 60min）→ 无当即复用池内空间重试
  - 定时巡检冷却到点自动恢复为 active（不再增删工作空间）

## 快速开始

### 本地运行

```bash
go run ./cmd/notion2api --config ./config.example.json
```

### 本地构建

```bash
go build ./cmd/notion2api
```

## Docker 部署

先按实际环境修改 `config.docker.json`，再启动：

```bash
docker compose up -d --build
```

如果使用偏生产配置：

```bash
docker compose -f docker-compose.prod.yml up -d --build
```

本地从源码开发需 Go `1.25.0+`（`go.mod` 已声明）。

## 默认入口

- API：`http://127.0.0.1:8787/v1/*`
- Health：`http://127.0.0.1:8787/healthz`
- WebUI：`http://127.0.0.1:8787/admin`

## 代理与 Resin 粘性代理

### 代理模式

`proxy_mode` 支持：

- `off`：关闭代理
- `env`：从环境变量读取（优先 `N2A_*`）
- `http`：固定 HTTP 代理
- `https`：按协议拆分 HTTP/HTTPS 代理
- `socks5`：SOCKS5/SOCKS5H 代理
- `resin_forward`：Resin 粘性代理转发

### 环境变量优先级（`proxy_mode=env`）

HTTPS 请求优先顺序：

1. `N2A_PROXY_HTTPS_URL`
2. `N2A_UPSTREAM_PROXY_HTTPS_URL`
3. `N2A_PROXY_URL`
4. `N2A_UPSTREAM_PROXY_URL`
5. `HTTPS_PROXY` / `https_proxy`
6. `ALL_PROXY` / `all_proxy`

HTTP 请求优先顺序：

1. `N2A_PROXY_HTTP_URL`
2. `N2A_UPSTREAM_PROXY_HTTP_URL`
3. `N2A_PROXY_URL`
4. `N2A_UPSTREAM_PROXY_URL`
5. `HTTP_PROXY` / `http_proxy`
6. `ALL_PROXY` / `all_proxy`

也可以直接用环境变量覆盖配置文件中的代理字段：

- `N2A_PROXY_MODE`
- `N2A_PROXY_URL`
- `N2A_PROXY_HTTP_URL`
- `N2A_PROXY_HTTPS_URL`
- `N2A_RESIN_ENABLED`
- `N2A_RESIN_URL`
- `N2A_RESIN_PLATFORM`
- `N2A_RESIN_MODE`

### Resin 粘性代理（按账号隔离）

每个账号都可以独立设置粘性身份：

- `accounts[].sticky_proxy_account`：显式设置粘性账号名（推荐）
- 未设置时会回退到邮箱派生值

当启用 `resin_forward` 时：

- 代理认证用户名格式：`<resin_platform>.<sticky_proxy_account>`
- 密码使用 `resin_url` 中 token
- 请求会附带 `X-Resin-Account` 头

## 配置说明

建议优先检查这些字段：

- `api_key`：OpenAI 兼容接口密钥
- `admin.password`：WebUI 登录密码
- `upstream_base_url` / `upstream_origin`
- `proxy_mode` / `proxy_url` / `proxy_http_url` / `proxy_https_url`
- `resin_enabled` / `resin_url` / `resin_platform` / `resin_mode`
- `accounts[*].sticky_proxy_account`
- `accounts` / `active_account`

### 自动注册（register）

- `register.enabled`：开启账号自动注册+水位补给（低于 `min_healthy_accounts` 时自动补号）
- `register.script_dir`：注册产物根目录（`accounts/detail` 出 probe.json、`mailboxes` 放 adguard cookies、`logs` 落 trace）
- `register.mail_provider`：`mailtm` 或 `adguard`
  - `mailtm`：自建随机邮箱（Notion 域名白名单限制，当前 emalupe.com 被拒时自动切回）
  - `adguard`：从 `script_dir/mailboxes/*.json`（capjs 浏览器版脚本产出）挑未消费的邮箱
- `register.space_mode`：`invite`（先 selfJoinSpaceByDomain 再自建 personal 兜底）或 `personal`（自建/复用 personal）
- `register.timeout_sec`：单次注册上限（建议 1200，其中收码等待常耗 10min+）

### 空间配额池（space_pool）

- `space_pool.enabled`：开启预建+冷却轮换（）
- `space_pool.target_per_account`：每号预建至 N 个空间（默认 2）
- `space_pool.cooldown_minutes`：额度归零后冷却时长，到点自动恢复（建议 ≥60）
- `space_pool.check_interval_sec`：池巡检间隔（默认 120s）

行为说明：
- 推理撞额度错时，当前空间进 cooldown，轮换器优先**复用**池内 active 空间（不算新建，不吃建空间日额），池空才 fallback 新 create（原始节流 min interval 10min）
- `workspace_deletion` 旧式增删循环在 space_pool 开启时自然退化为无人可删
- `storage.sqlite_path`

可直接参考：

- `config.example.json`
- `config.docker.json`

## 使用建议

- 首次启动后先访问 `/admin`，确认账号、配置和连通性是否正常
- 修改管理台前端后需执行 `npm --prefix ./frontend run build:static`
- 调整会话延续与存储时，建议同步检查 `internal/app/sqlite_store.go` 的 schema 与迁移兼容性

## 架构

```
Client → API Gateway → Request Dispatcher → Agent Runtime → Provider → Notion Web
```

- **API Gateway**：路由、鉴权、`X-Request-Id`、body 限制（`internal/app/main.go`）
- **Request Dispatcher**：账号选择、健康探测缓存、预算、半开冷却、断连传播（`request_dispatch.go`）
- **Agent Runtime**：Session 缓存、流式处理、工具协议、Prompt Adapter、错误恢复、可观测性
- **Provider**：`NotionAIClient`（transport 缓存 + surf 回退；未来可替换/扩展）

详见 [`ARCHITECTURE.md`](ARCHITECTURE.md)。

## OpenAI SDK 接入示例

只改 `base_url` + `api_key`：

```python
from openai import OpenAI

client = OpenAI(base_url="http://127.0.0.1:8787/v1", api_key="change-me-openai-key")

# 非流式
resp = client.chat.completions.create(
    model="gpt-5.4",
    messages=[{"role": "user", "content": "Hello"}],
)
print(resp.choices[0].message.content)

# 流式
for chunk in client.chat.completions.create(
    model="gpt-5.4",
    messages=[{"role": "user", "content": "写一个快排"}],
    stream=True,
):
    delta = chunk.choices[0].delta.content if chunk.choices else None
    if delta:
        print(delta, end="", flush=True)
```

## Tool Calling 示例

传入标准 `tools` + `tool_choice`，网关返回标准 `tool_calls`；把 assistant 的 `tool_calls` 与 `tool` 结果原样回传即可继续：

```python
tools = [{
    "type": "function",
    "function": {
        "name": "read_file",
        "description": "read a file",
        "parameters": {"type": "object", "properties": {"path": {"type": "string"}}, "required": ["path"]},
    },
}]
resp = client.chat.completions.create(
    model="gpt-5.4",
    messages=[{"role": "user", "content": "读一下 note.txt"}],
    tools=tools,
    tool_choice="auto",
)
msg = resp.choices[0].message
# msg.tool_calls[0].function.name / .arguments
```

约束：`tool_choice="none"` 时绝不输出工具调用；`parallel_tool_calls=false` 时只返回首个调用。

## 生成参数支持

| 参数 | 行为 |
|---|---|
| `max_tokens` / `max_completion_tokens` | 本地强制截断，`finish_reason=length` |
| `n>1` | 400（只返回单个 choice） |
| `temperature` / `top_p` | 越界 400；合法值接受但上游无法生效，响应头 `X-Notion2API-Unsupported-Params` 显式上报 |
| `response_format`（`json_object` / `json_schema`） | 注入输出格式指令 |
| `parallel_tool_calls` | 支持（false 只保留首个调用） |

## 延迟开关

- `features.use_web_search`：是否联网检索（沿用配置值）。
- `streaming.initial_flush_delay_ms`：SSE 首字节主动 flush 延时；缺省 1500ms，设 `0` 为最低延迟。

## 错误与恢复

标准错误体 `{"error":{"message","type","param","code"}}`，分类：

- 额度/premium/402 → `429 insufficient_quota`
- 429 → `429 rate_limit_error`
- session/401/403 → `401 invalid_api_key`
- `temporarily-unavailable`/5xx → `503 upstream_unavailable`
- 超时 → `504 upstream_timeout`
- 其余 → `502 upstream_error`

流式中断：首字节前返回 HTTP 错误；首字节后发 `error` 事件（`code=upstream_aborted`）再 `[DONE]`。客户端断开会取消上游请求。

## 可观测性

- 每请求日志：`[req] id=... path=... status=... model=... account=... ttft_ms=... total_ms=... retries=... stream_error=...`
- `/metrics`（Prometheus）：`notion2api_request_duration_seconds`、`notion2api_ttft_seconds` 等。

## 测试

```bash
make test          # vet + 全量测试
# 或 Windows：
pwsh -File scripts/test.ps1
```

包含 OpenAI Python SDK 兼容冒烟（无 python/openai 时自动跳过）。详见 [`TEST_REPORT.md`](TEST_REPORT.md)。

## 开源协议

MIT License

## 致谢

本项目已在 [LINUX DO 社区](https://linux.do) 发布，感谢社区的支持与反馈。
