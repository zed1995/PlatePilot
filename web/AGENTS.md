# web — AGENTS.md

通用约定在 [../AGENTS.md](../AGENTS.md)；本文件只写管理后台前端。

**运维控制台是只读的**：通过 `chat-service` 的 `/admin/v1/*` 查看已入库的数据，
不写库、不直接连数据库（后端 API 由该 Go 服务提供）。

**例外：`/agent`（Agent 验证台）会写**。它走 `/v1` 对话面，以 `X-User-ID` 的身份
发言并触发工具调用，与只读运维台是两种定位，因此在侧边栏里单独分组、
类型与客户端也各自独立（`api/chat.ts` vs `api/client.ts`）。改动这两块时不要把
身份头塞进 `adminApi`，也不要让 `/agent` 的写能力蔓延到运维页面。

这个目录归 Node 工具链，**不要在这里放 Go 代码**。

```bash
npm install
npm run dev      # Vite，代理 /admin 与 /v1 到后端
npm run build    # tsc && vite build → dist/
npm test         # vitest
```

## 运行前提

1. 运维台（`/admin/v1`）需要 `chat-service` 带 `ADMIN_ENABLED=true` 起；默认是关的
   （`make run-admin`）。**Agent 验证台不需要它**，只依赖 `/v1`。
2. admin API **额外限制 loopback**，所以后端要绑本地地址（如 `HTTP_ADDR=127.0.0.1:8080`）。
3. 不走代理直连别的服务时用 `VITE_API_BASE`，该来源必须被后端的
   `HTTP_CORS_ALLOW_ORIGINS` 允许；改后端目标地址用 `VITE_BACKEND_TARGET`。
4. 验证台发消息前库必须迁到最新（`make migrate`）；库停在旧版本时第一条消息会 502。

## 结构

| 路径 | 内容 |
|---|---|
| `src/api/` | `client.ts`（只读运维面的请求封装）、`chat.ts`（对话面，含 `X-User-ID` 与 SSE）、`types.ts`（后端响应类型） |
| `src/lib/` | `utils.ts`（`cn()`）、`sse.ts`（POST SSE 解析）、`answer.ts`（`[^n]` 与五段式） |
| `src/hooks/` | `useThreads.ts`（线程/消息/run）、`useMemories.ts`（记忆）、`useAgentTurn.ts`（一轮对话的状态归约） |
| `src/pages/` | 9 个页面：Dashboard、Ingestion(+Detail)、Restaurants(+Detail)、Documents(+Detail)、RetrievalDebug，以及 **AgentConsole**；每个页面同目录放 `.test.tsx` |
| `src/components/` | 业务组件 + `components/agent/`（验证台专用）+ `components/ui/`（手抄的 shadcn 风格原子件） |
| `src/format.ts` | 展示层格式化 |

技术栈：Vite + React 18 + TS + Tailwind + Radix primitives + TanStack Query +
React Router + Lucide + Motion。设计语言是**苹果风灰度单色**。

## 规则

- 后端字段改名时，先改 `src/api/types.ts`，再改页面——类型就是这个项目和 Go
  之间的契约边界（对应后端 `/v1` 与 `/admin/v1` 的响应体）。
- 每个页面配一个同名 `.test.tsx`，改页面就改测试（`vitest` + React Testing Library）。
- 错误处理走统一的 `error-state` / `empty-state` / `status-tag` 组件，不要各页面自造。
- 数据展示优先复用 `key-set-table`（keyset 游标分页）、`json-block`（原始 JSON 折叠）、
  `trace-panel`（检索/证据 trace 可视化）——这些是为了把后端的 `trace` 忠实显示出来，
  **不要把它美化成看不出通道贡献的样子**。
- `trace` 与 `reasons` 是产品核心信息（引用为什么成立），UI 上不能弱化或省略。
- 新增依赖前先确认 `components/ui/` 里已有的原子件能不能满足；样式 token 改
  `tailwind.config.ts`，不要在组件里写死颜色。
- **验证台的三条硬规则**（对话面特有）：
  1. 后端事件集是封闭的今天、会变大的明天——未知 SSE 事件名必须忽略而不是抛错；
  2. `tool.start` / `tool.finish` 按 `call_id` 配对，**不许用数组下标**；
  3. `awaiting_*` 不是失败，它之后仍会有 `message.end`，状态要停在等待态；
     `message.end.warnings` 必须显示（向量通道降级只在这里出现一次）。

