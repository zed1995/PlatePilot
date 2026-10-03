# web — AGENTS.md

通用约定在 [../AGENTS.md](../AGENTS.md)；本文件只写管理后台前端。

**只读运维控制台**：通过 `chat-service` 的 `/admin/v1/*` 查看已入库的数据，
不写库、不直接连数据库（后端 API 由该 Go 服务提供）。
这个目录归 Node 工具链，**不要在这里放 Go 代码**。

```bash
npm install
npm run dev      # Vite，代理 /admin 与 /v1 到后端
npm run build    # tsc && vite build → dist/
npm test         # vitest
```

## 运行前提

1. `chat-service` 必须带着 `ADMIN_ENABLED=true` 起；默认是关的（`make run-admin`）。
2. admin API **额外限制 loopback**，所以后端要绑本地地址（如 `HTTP_ADDR=127.0.0.1:8080`）。
3. 不走代理直连别的服务时用 `VITE_API_BASE`，该来源必须被后端的
   `HTTP_CORS_ALLOW_ORIGINS` 允许；改后端目标地址用 `VITE_BACKEND_TARGET`。

## 结构

| 路径 | 内容 |
|---|---|
| `src/api/` | `client.ts`（请求封装）、`types.ts`（后端响应类型） |
| `src/pages/` | 8 个页面：Dashboard、Ingestion(+Detail)、Restaurants(+Detail)、Documents(+Detail)、RetrievalDebug；每个页面同目录放 `.test.tsx` |
| `src/components/` | 业务组件 + `components/ui/`（手抄的 shadcn 风格原子件） |
| `src/lib/utils.ts` | `cn()` 类名合并工具 |
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
