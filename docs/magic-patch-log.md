# Sub2API 魔改记录

## 目的
这个文件用于记录 **相对官方 upstream 的本地差异**，帮助后续：
- 理解为什么要改
- 找到改过哪些文件
- 在 rebase 时快速定位高风险冲突区
- 避免重复踩坑

## 记录规则
每次真实魔改后，至少补充以下信息：
- 日期
- 背景 / 目标
- 影响文件
- 改动摘要
- 为什么不能直接接受官方默认行为
- rebase 时需要特别注意什么
- 验证结果

## 当前基线状态

### 2026-04-23：魔改底座初始化
**类型**：基础设施初始化（非业务源码魔改）

**背景**：
- 需要保留旧生产 `sub2api` 不动
- 同时新起一套可持续 rebase 的魔改环境
- 新环境要适配当前 RackNerd + Cloudflare + NPM + Docker 内网架构

**当前结果**：
- 新源码仓：`/root/sub2api-modded`
- 官方远程：`upstream`
- 本地维护分支：`magic/main`
- 新依赖环境：`/root/sub2api-modded-deploy`
- 新数据目录：`/root/sub2api-modded-data`
- 新服务：`sub2api-modded.service`
- 新监听：`172.19.0.1:18081`
- 新依赖内网：
  - PostgreSQL：`172.23.0.10`
  - Redis：`172.23.0.11`

**说明**：
- 这一阶段主要是运行底座改造
- 源码仓尚未引入明确的业务逻辑差异补丁时，可视为“代码层接近上游，基础设施层已分叉”

**后续提醒**：
- 一旦开始改网关逻辑、上游路由、鉴权、头处理、调度、计费、前端页面等，请在这里逐条补记

---

### 2026-04-24：同步 upstream 并清理本地产物噪音
**类型**：运维适配 / 仓库卫生

**背景**：
- `magic/main` 在同步前落后 `upstream/main` 53 个提交
- 工作区同时存在本地临时脚本与构建产物，影响同步前后的状态判断
- 需要在不动旧生产目录的前提下，安全跟进官方更新并保持魔改仓可维护

**影响文件**：
- `.gitignore`
- `docs/magic-patch-log.md`

**改动摘要**：
- 先暂存本地脏工作区，再将 `magic/main` 快进同步到最新 `upstream/main`
- 在 `.gitignore` 中补充忽略 `bin/` 与 `backend/.tmp_admin_hash.go`
- 保留本地已有的 `.narrafork/`、`.worktrees/` 忽略规则

**与官方差异原因**：
- `bin/` 是本地构建输出目录，不应长期污染工作区
- `backend/.tmp_admin_hash.go` 是一次性管理员密码哈希辅助脚本，不属于正式源码
- 若不忽略，这类本地噪音会干扰后续 rebase、状态检查与补丁识别

**rebase 风险点**：
- 若 upstream 后续调整 `.gitignore` 末尾规则，需要留意这些本地忽略项是否仍应保留
- 临时脚本若后续转正为正式工具，应迁移到明确目录并重新评估忽略策略

**验证结果**：
- `magic/main` 已同步到 `upstream/main` 最新提交
- 已按文档执行 build / restart / health check / 日志检查 / 依赖检查
- 同步后工作区状态应不再因 `bin/` 与 `backend/.tmp_admin_hash.go` 持续报脏

---

### 2026-04-25：免费层级 OAuth 生图恢复 web2api 路由
**类型**：修复 / 上游路由容错

**背景**：
- image2 平台调用 Sub2API `/v1/images/generations` 时，free 账号被送到新版 Codex Responses `image_generation` 工具链路，上游返回 400：`Tool choice 'image_generation' not found in 'tools' parameter.`
- 旧版本文生图支持过 ChatGPT 网页 `backend-api/f/conversation`（历史提交中称 web2api）；free 层级账号应走该链路，而不是付费/团队账号使用的 Responses 图片工具。

**影响文件**：
- `backend/internal/service/openai_images_responses.go`
- `backend/internal/service/openai_images_web2api.go`
- `backend/internal/service/openai_images_test.go`
- `docs/magic-patch-log.md`

**改动摘要**：
- 恢复 ChatGPT web2api 文生图链路：`chat-requirements`、`conversation/init`、`conversation/prepare`、`f/conversation`、conversation 轮询、file-service/sediment 下载并转成 OpenAI Images 兼容 `b64_json` 响应。
- OAuth free 账号（`credentials.plan_type=free`）的非流式 `/v1/images/generations` 自动走 web2api；team/plus/pro/API Key 继续走现有 Responses / OpenAI Images 链路。
- 增加 `extra.openai_images_transport` / `credentials.openai_images_transport` 覆盖：`web2api` 强制走 web2api，`responses` 强制走 Responses。
- 保留专用 failover：当 Responses 链路遇到 `image_generation` tool 不可用时，仍可换同组其他账号。
- 补充单元测试覆盖 free 路由、手动覆盖、stream guard、web2api conversation body。

**与官方差异原因**：
- 当前环境明确需要 free 层级 OpenAI OAuth 账号也能做文生图；新版 Codex Responses `image_generation` 工具对这类账号不可用。
- web2api 依赖 ChatGPT 网页接口，官方默认不保证稳定，但它是 free 账号可用性的必要兼容层。

**rebase 风险点**：
- 如果 upstream 后续调整 OpenAI Images OAuth 转发、ChatGPT backend-api 参数、sentinel/PoW 或 file-service 下载逻辑，需要重点保留并复核 `openai_images_web2api.go`。
- web2api 目前只自动接管 free 账号的非流式文生图；流式/编辑图仍不自动切到该链路，除非后续确认网页接口稳定支持。

**验证结果**：
- `/usr/local/go1.26.2/bin/go test ./internal/service -run 'TestShouldUseOpenAIImagesWeb2API|TestBuildOpenAIImageConversationRequestUsesWeb2APIPictureHint|TestOpenAIGatewayServiceForwardImages_OAuth'` 通过。
- `/usr/local/go1.26.2/bin/go test ./...` 通过。
- 已使用 `-tags embed` 构建 `/root/sub2api-modded/bin/sub2api` 并重启 `sub2api-modded.service`。
- 宿主机与 `npm-app` 容器访问 `/health` 均返回 `{"status":"ok"}`。

---

### 2026-04-27：跟进 upstream v0.1.119 并保留本地补丁
**类型**：上游同步 / rebase / 冲突处理

**背景**：
- 官方 `upstream/main` 从 `9d1751ec` 更新到 `c92b88e3`，包含 `v0.1.119`、OpenAI Images 显式 session、Claude/Anthropic 兼容修复、EasyPay/Zpay 退款处理、邀请返利等改动。
- 本地分支包含 OpenAI free OAuth 生图 web2api、轻量探测、EasyPay ezfpy 兼容、CI 部署适配等补丁，需要 rebase 到最新官方主线。
- 当前仓库实际维护分支为 `main`；未发现本地 `magic/main` 分支。

**影响文件**：
- `backend/internal/payment/provider/easypay.go`
- `backend/internal/payment/provider/easypay_sign_test.go`
- `backend/internal/payment/provider/easypay_refund_test.go`
- `docs/magic-patch-log.md`
- OpenAI Images 相关补丁文件（rebase 后复核，无冲突）：
  - `backend/internal/service/openai_images_responses.go`
  - `backend/internal/service/openai_images_web2api.go`
  - `backend/internal/service/openai_images_test.go`

**改动摘要**：
- 已 fetch 官方更新并将本地 `main` rebase 到 `upstream/main=c92b88e3`。
- 解决 `fix(payment): support ezfpy easypay compatibility` 与官方 `Fix Zpay refund endpoint handling` 在 `easypay.go` 的冲突。
- EasyPay 合并策略：保留官方退款 URL 归一化、退款响应错误处理、`out_trade_no`/`trade_no` 重试；同时保留本地 ezfpy 兼容的成功码 `200`、旧成功码 `1`、`qrcode`/`code_url` 兼容、`findorder` 查询模式和 fallback。
- OpenAI free OAuth 生图 web2api 补丁顺利重放，继续保留 `credentials.plan_type=free` 或 `openai_images_transport=web2api` 时走 ChatGPT web2api 的本地行为。

**与官方差异原因**：
- 官方更新增强了 OpenAI Images 显式 session 与 EasyPay/Zpay 退款处理，但本地仍需要支持 free OAuth 生图 web2api 以及 ezfpy EasyPay 返回格式。
- ezfpy 的创建支付与查询返回可能使用 `code=200`、`code_url`、`/api/findorder`，不能只保留官方默认的 Zpay 行为。

**rebase 风险点**：
- 后续 upstream 若继续调整 OpenAI Images OAuth session、Responses failover 或 scheduler cache，需要复核 free 账号是否仍默认走 web2api。
- 后续 upstream 若继续调整 EasyPay/Zpay，需同时保留官方退款健壮性和本地 ezfpy 兼容矩阵。
- `origin/main` 仍是 rebase 前历史，当前本地 `main` 相对 `origin/main` 会显示大幅 ahead/behind；推送时需要按用户确认后的策略处理。

**验证结果**：
- `pnpm install --frozen-lockfile && pnpm run build` 通过，前端已产出到 `backend/internal/web/dist`。
- `/usr/local/go1.26.2/bin/go test ./internal/payment/provider` 通过。
- `/usr/local/go1.26.2/bin/go test ./internal/service -run 'TestShouldUseOpenAIImagesWeb2API|TestBuildOpenAIImageConversationRequestUsesWeb2APIPictureHint|TestOpenAIGatewayServiceForwardImages_OAuth'` 通过。
- `/usr/local/go1.26.2/bin/go test ./...` 通过。
- `/usr/local/go1.26.2/bin/go build -tags embed -o /root/sub2api-modded/bin/sub2api ./cmd/server` 通过。
- `systemctl restart sub2api-modded.service` 后服务为 `active`，日志显示 `Server started on 172.19.0.1:18081`。
- 宿主机与 `npm-app` 容器访问 `/health` 均返回 HTTP 200 / `{"status":"ok"}`。

---

### 2026-04-30：跟进 upstream v0.1.120+3 并保留本地补丁
**类型**：上游同步 / rebase / 冲突处理

**背景**：
- 官方 `upstream/main` 从 `c92b88e3` 更新到 `094e1171`；官方 tag `v0.1.120` 位于 `8bf2a7b8`，当前 upstream head 还包含 tag 后 3 个提交。
- 本次官方更新包含 OpenAI Fast/Flex Policy、Codex/Responses 兼容修复、OpenAI Images API Key versioned base URL、scheduler snapshot/sticky session 修复、Vertex Service Account、请求体压缩解码与流错误处理增强等改动。
- 本地分支包含 OpenAI free OAuth 生图 web2api、轻量探测、EasyPay ezfpy 兼容、CI 部署适配、图片能力 scope、GPT-5.5 fast 计费和 TTFT trace 等补丁，需要 rebase 到最新官方主线。
- 当前仓库实际维护分支仍为 `main`；rebase 后 `HEAD=3e2ed41b`，共同基点为 `upstream/main=094e1171`。

**影响文件**：
- `backend/internal/service/openai_images_test.go`
- `backend/internal/service/openai_images_web2api.go`
- `backend/internal/service/openai_images_responses.go`
- `backend/internal/repository/scheduler_cache.go`
- `backend/internal/repository/scheduler_cache_unit_test.go`
- `.github/workflows/build.yml`
- `backend/internal/payment/provider/easypay.go`
- `docs/magic-patch-log.md`
- TTFT trace 相关文件（rebase 后复核）：
  - `backend/internal/config/config.go`
  - `backend/internal/repository/http_upstream.go`
  - `backend/internal/handler/openai_gateway_handler.go`
  - `backend/internal/handler/openai_chat_completions.go`
  - `backend/internal/service/openai_first_token_trace.go`
  - `backend/internal/service/openai_gateway_service.go`
  - `backend/internal/service/openai_gateway_chat_completions.go`

**改动摘要**：
- 已 fetch 官方更新并将本地 `main` rebase 到 `upstream/main=094e1171`。
- 解决 `openai_images_test.go` 多轮冲突：同时保留官方 API Key Images versioned base URL 测试，以及本地 free OAuth web2api、capability、advanced 参数拒绝、failover 等测试。
- 解决 `openai_images_web2api.go` add/add 冲突：保留本地 `validateOpenAIImagesWeb2APIRequest` 防护、`openAIImagesDefaultModel` 默认模型和 free 账号 basic-only 路由；避免旧补丁回退为任意非流式 generation 都走 web2api。
- 解决 `.github/workflows/build.yml` add/add 冲突：保留本地部署 SSH 默认值和清晰错误输出；后续本地 CI 补丁继续跳过易碎 frontend test job，只构建 embed 前端。
- 解决 `easypay.go` 冲突：保留官方退款 URL 归一化、退款重试与非 JSON/HTML 错误处理；同时保留本地 ezfpy 兼容的成功码、`code_url`、`findorder` 查询与 fallback。
- duplicate/已上游化补丁由 rebase 自动丢弃或等价重放，例如 compact probe 暴露和 CI SSH 默认值相关提交。

**与官方差异原因**：
- 官方新增/修复了 OpenAI Images、Fast/Flex Policy、scheduler、Vertex 等能力，但本地仍需要 free OAuth 生图 web2api 兼容、TTFT trace 排障、GPT-5.5 fast 计费与 image2/CI/运维适配。
- CI 部署仍绑定当前 `sub2api-modded.service`、`172.19.0.1:18081` 与 NPM 回源验证流程，不能直接采用纯官方工作流。
- ezfpy EasyPay 返回格式与查询接口仍与官方默认 Zpay/EasyPay 行为存在差异，需要继续保留本地兼容矩阵。

**rebase 风险点**：
- upstream 的 OpenAI Fast/Flex Policy 会进入 `/responses`、chat completions 转 responses、WS/passthrough 等路径；后续需关注是否影响本地 GPT-5.5 fast 计费和 TTFT trace stage 覆盖。
- upstream 的 scheduler cache/sticky session 变更较大；后续若再改调度快照，必须确认 `plan_type`、`openai_images_transport`、账号组等本地调度元数据仍被保留。
- upstream 的 OpenAI Images base URL 与 OAuth session 逻辑继续演进；后续仍需确认 free OAuth basic 文生图默认 web2api，team/plus/pro/API Key/stream/advanced 请求仍走 Responses 或官方 API Key 路径。
- `origin/main` 仍是 rebase 前历史；如需推送，需要使用安全的 force-with-lease 策略，避免覆盖远端未知提交。

**验证结果**：
- OpenAI Images / Fast Policy / scheduler / EasyPay 等专项测试通过：
  - `/usr/local/go1.26.2/bin/go test ./internal/service -run 'TestBuildOpenAIImagesURL|TestShouldUseOpenAIImagesWeb2API|TestBuildOpenAIImageConversationRequestUsesWeb2APIPictureHint|TestOpenAIGatewayServiceForwardImages_OAuth|TestOpenAIGatewayServiceForwardImages_APIKey|TestOpenAIFastPolicy|TestNormalizeOpenAIServiceTier|TestExtractOpenAIServiceTier|TestOpenAIGatewayServiceRecordUsage_GPT55Priority'`
  - `/usr/local/go1.26.2/bin/go test ./internal/repository -run 'TestBuildSchedulerMetadataAccount|TestScheduler'`
  - `/usr/local/go1.26.2/bin/go test ./internal/payment/provider`
  - `/usr/local/go1.26.2/bin/go test ./internal/pkg/httputil ./internal/handler/admin`
- `/usr/local/go1.26.2/bin/go test ./...` 通过。
- `pnpm --dir frontend install --frozen-lockfile && pnpm --dir frontend run build` 通过；`backend/internal/web/dist/index.html` 已生成。
- `/usr/local/go1.26.2/bin/go build -tags embed -o /root/sub2api-modded/bin/sub2api ./cmd/server` 通过；二进制约 113M。
- `systemctl restart sub2api-modded.service` 后服务为 `active`，日志显示 `Server started on 172.19.0.1:18081`。
- 宿主机与 `npm-app` 容器访问 `/health` 均返回 HTTP 200 / `{"status":"ok"}`。
- 依赖容器 `sub2api-modded-postgres`、`sub2api-modded-redis` 均为 `healthy`。

---

### 2026-04-30：跟进 upstream v0.1.121 并切换为 GitHub Actions 验证
**类型**：上游同步 / rebase / 运维约束修正

**背景**：
- 官方 `upstream/main` 从 `094e1171` 更新到 `48912014`，tag `v0.1.121` 位于 `9d801595`。
- 本次官方更新包含 Anthropic 缓存 TTL 注入开关、管理员设置契约字段、表格分页大小 localStorage 持久化、OpenAI item references previous response 推断修复等改动。
- 本地 `main` 已 rebase 到 `upstream/main=48912014`，无源码冲突。
- 运维约束修正：后续不在服务器本机执行前端/后端构建、依赖安装或全量测试，默认通过 GitHub Actions 完成构建、测试、部署与健康检查。

**影响文件**：
- `backend/internal/config/config.go`
- `backend/internal/handler/dto/settings.go`
- `backend/internal/handler/admin/setting_handler.go`
- `backend/internal/service/settings_view.go`
- `frontend/src/views/admin/SettingsView.vue`
- `frontend/src/components/common/Pagination.vue`
- `frontend/src/composables/usePersistedPageSize.ts`
- `backend/internal/handler/openai_gateway_handler.go`
- `.claude/skills/sub2api-modded-rebase-playbook/SKILL.md`
- `.claude/skills/sub2api-modded-runtime-ops/SKILL.md`
- `docs/magic-patch-log.md`

**改动摘要**：
- 已 fetch upstream 并将本地 `main` rebase 到 `upstream/main=48912014`。
- rebase 过程没有出现需要手工解决的源码冲突；本地 OpenAI free OAuth web2api、图片能力 scope、GPT-5.5 fast 计费、TTFT trace、CI 部署适配等补丁自动重放。
- 更新 rebase/runtime SOP：禁止默认在服务器本机执行 `pnpm install`、`pnpm run build`、`go test ./...`、`go build`、手动 `systemctl restart sub2api-modded.service`；改由 GitHub Actions runner 构建测试，deploy workflow 负责受控重启、健康检查与失败回滚。

**与官方差异原因**：
- 官方继续演进设置项、OpenAI 转发和前端表格行为；本地仍需保留 free OAuth 生图 web2api、TTFT trace、计费与部署适配等魔改。
- 当前服务器承载运行环境，不应承担构建/测试负载；GitHub Actions 已具备前端 embed 构建、`go test ./...`、Linux 二进制构建、artifact 上传与远程部署回滚能力。

**rebase 风险点**：
- Anthropic 缓存 TTL 注入新增设置项，后续若继续调整设置契约，需关注前后端字段同步和默认值。
- OpenAI item references previous response 推断修复与本地 TTFT trace/OpenAI gateway 补丁同属转发路径，后续继续关注冲突。
- 本地 rebase 后 `origin/main` 仍在旧历史；如需上线，应推送到 GitHub 触发 Actions，而不是本机构建。

**验证结果**：
- 已完成源码级 rebase，无手工冲突。
- 本机曾误触发部分 Go 测试命令；因服务器重启/中断，结果不作为本次验证依据。后续不再在服务器本机执行构建或测试。
- 待 GitHub Actions 验证：前端 embed 构建、`go test ./...`、后端 Linux 二进制构建。
- 待 GitHub Actions deploy：上传二进制、备份旧二进制、重启 `sub2api-modded.service`、宿主机 `/health` 与 `npm-app` 回源健康检查；失败由 workflow 自动回滚。

### 2026-05-05：跟进 upstream v0.1.122/v0.1.123 并保留本地补丁
**类型**：上游同步 / rebase / 冲突处理

**背景**：
- 官方 `upstream/main` 从 `48912014` 更新到 `a1106e81`，包含 tag `v0.1.122`（`c129825f`）与 `v0.1.123`（`df722c9`）之后的合并提交。
- 本次官方更新包含 OpenAI Compact 批量编辑与专属模型映射、APIKey 上游不支持 Responses 时 raw Chat Completions 直转、OpenAI unknown model fallback 收紧、图片生成分组控制/并发/计费、OpenAI Messages/Claude Code 兼容增强、usage billing drain/zero usage 修复、邀请返利后台记录、ops cleanup 设置修复、Select/GroupSelector 搜索和 axios 更新等。
- 本地分支包含 free OAuth 生图 web2api、图片能力 scope、GPT-5.5 fast 计费、TTFT trace、EasyPay ezfpy、GitHub Actions 部署适配、低额充值 preset、Image2 UI 边界等补丁，需要 rebase 到最新官方主线。
- 当前仓库实际维护分支仍为 `main`；rebase 后本地 `main` 基于 `upstream/main=a1106e81`。

**影响文件**：
- `backend/internal/service/openai_images_test.go`
- `frontend/vite.config.ts`
- `backend/internal/service/openai_images.go`
- `backend/internal/handler/openai_chat_completions.go`
- `backend/internal/service/openai_gateway_chat_completions.go`
- `backend/internal/service/openai_gateway_service.go`
- `docs/magic-patch-log.md`
- rebase 后复核的本地补丁区域：
  - `backend/internal/service/openai_images_web2api.go`
  - `backend/internal/service/openai_images_responses.go`
  - `backend/internal/service/billing_service.go`
  - `backend/internal/service/openai_gateway_record_usage_test.go`
  - `.github/workflows/build.yml`
  - `frontend/src/views/user/PaymentView.vue`
  - `frontend/src/components/payment/AmountInput.vue`
  - `frontend/src/i18n/locales/en.ts`
  - `frontend/src/i18n/locales/zh.ts`

**改动摘要**：
- 已 fetch upstream 并将本地 `main` rebase 到 `upstream/main=a1106e81`。
- 解决 `openai_images_test.go` import 冲突：同时保留官方 streaming/client disconnect 测试需要的 `errors` 与本地 web2api conversation body 测试需要的 `encoding/json`。
- 解决 `frontend/vite.config.ts` 冲突：保留本地轻量构建策略，不重新开启 `checker.typescript`，同时保留 build 阶段禁用 checker 的说明与 `enableBuild: false`。
- 解决 `openai_images.go` 图片尺寸计费冲突：保留官方 `int64` 维度解析、`×` 归一和有效尺寸校验；同时恢复本地按官方尺寸白名单与 2K 像素阈值划分自定义有效尺寸的语义，避免 `1024x768` / `1280x768` 被误记为 1K、`2560x1600` / `3840x1024` 被误记为 2K。
- 解决 TTFT trace 与官方 OpenAI gateway 大改的冲突：保留官方 raw Chat Completions / stream drain / client disconnect 继续读取 usage / Messages bridge 等逻辑，同时保留本地 TTFT trace 的 account select、build upstream、http do、first SSE、first token、first chat chunk 等埋点。
- 复核 OpenAI OAuth legacy /responses 的 upstream context detach：该语义来自官方 `72d5ee4c`，本次 rebase 合并时曾误退回 stream-only detach，后续已恢复，避免客户端取消导致上游请求 `context canceled` 并丢失可计费结果。
- OpenAI free OAuth 生图 web2api 补丁保留：`shouldUseOpenAIImagesWeb2API(account, parsed)` 仍在 OAuth images 分支中生效，`plan_type=free` 与 `openai_images_transport` 覆盖项继续存在，Responses `image_generation` tool 不可用的专用 failover 判断仍保留。
- OpenAI usage billing 合并官方 unknown model fail-closed 与本地合法映射要求：`RecordUsage` 对无法定价的 token 模型返回错误，不再写 0 元伪成功记录；同时保留官方 `usageBillingModelCandidates` / compact alias / upstream fallback，避免误伤合法映射或上游模型计费。
- CI 部署 workflow 保留本地 GitHub Actions 构建、测试、artifact、远程部署、健康检查与失败回滚流程。
- Image2 边界保留：没有恢复已删除的独立 Image2 产品 UI；官方新增的图片生成分组/计费配置属于 Sub2API 管理能力，已随上游保留。

**与官方差异原因**：
- 官方增强了 OpenAI 兼容、图片生成控制、usage billing 与前端管理能力，但本地仍必须支持 free 层级 OpenAI OAuth 账号通过 ChatGPT web2api 做基础文生图。
- 本地需要继续保留 TTFT trace 以定位 OpenAI gateway 首包耗时；官方默认不包含这些细粒度埋点。
- 当前部署流程绑定 `sub2api-modded.service`、`172.19.0.1:18081`、`npm-app` 回源健康检查和 GitHub Actions 远程部署回滚，不能直接采用纯官方发布方式。
- 本地低额充值 preset 和 EasyPay ezfpy 兼容仍属于当前支付运营需求。
- Image2 是独立平台，不能把 Image2 产品工作台重新嵌回 Sub2API 前端。

**rebase 风险点**：
- 官方 OpenAI gateway/compat 改动很大，后续若继续调整 `Forward`、`ForwardAsChatCompletions`、Messages bridge、raw CC 或 streaming drain，需确认 TTFT trace 与 usage 计费仍同时生效。
- 官方 unknown model fail-closed 语义必须继续保留，避免未知 OpenAI 模型按默认模型误扣费；同时要保留 explicit mapping、channel mapping、`BillingModel`、`UpstreamModel`、compact alias 的合法计费候选。
- 官方图片生成控制与本地 free OAuth web2api 会共同影响 `/v1/images/generations`，后续需确认 free OAuth basic 路由、分组图片开关、图片 capability scope、size tier 与 image billing mode 不互相覆盖；size tier 冲突时保留本地语义：官方尺寸白名单精确分类，自定义/未知有效尺寸按是否超过 `2560*1440` 像素阈值划为 2K/4K，长边至少 3840 且短边不超过 1024 的宽屏/高屏尺寸仅用于 billing 分级时也可按阈值归为 4K，非法、双边超约束或无法解析尺寸回退 2K。
- OpenAI gateway rebase 时如出现 `detachStreamUpstreamContext(ctx, reqStream)` 与 `detachUpstreamContext(ctx)` 的冲突，应确认是否误丢官方 `72d5ee4c` 引入的 OAuth legacy /responses detach 语义；至少保持 `TestOpenAIGatewayService_OAuthLegacy_UpstreamRequestIgnoresClientCancel` 通过。
- `origin/main` 如落后于本地 rebase 后历史，推送前应先检查远端 SHA 并使用 `--force-with-lease`，由 GitHub Actions 验证/部署，避免本机构建或手动重启。

**验证结果**：
- 已完成源码级 rebase，并解决手工冲突：`openai_images_test.go`、`frontend/vite.config.ts`、`openai_images.go`、`openai_chat_completions.go`、`openai_gateway_chat_completions.go`、`openai_gateway_service.go`。
- 已完成轻量检查：`git diff --check` 通过；关键 grep 确认 free OAuth web2api、unknown model fail-closed/合法计费候选、GitHub Actions deploy 健康检查入口仍存在。
- 未在服务器本机执行 `pnpm install`、`pnpm run build`、`go test ./...`、`go build` 或 `systemctl restart`。
- 2026-05-05 首次推送 `79faa177` 后，GitHub Actions `Build sub2api modded` 在 `go test ./...` 阶段失败，deploy job 因无 artifact 被跳过；失败点集中在图片尺寸分级期望与 OAuth legacy passthrough client cancel detach。
- 后续修复 `fabce79c` 恢复图片尺寸分级主语义与 OpenAI OAuth legacy /responses detached context；其中 OAuth detach 属于官方 `72d5ee4c` 已有语义，本次失败是 rebase 合并时误退回 stream-only detach。
- 补充修复 `8543f268` 覆盖 oversized image billing tier 边界：`3840x3840` 等双边超约束尺寸回退 `2K`，`3840x1024` / `4096x1024` 等长边大且短边不超过 1024 的宽屏/高屏尺寸按像素阈值归为 `4K`。
- GitHub Actions 最终验证通过：`8543f268` 对应 `CI` success、`Build sub2api modded` success、`Security Scan` success；`Build sub2api modded` 已完成前端构建、嵌入式后端二进制构建、artifact 上传、远程部署、服务重启与健康检查。

### 2026-05-12：跟进 upstream v0.1.124/v0.1.125 并保留本地补丁
**类型**：上游同步 / rebase / 冲突处理

**背景**：
- 官方 `upstream/main` 从 `a1106e81` 更新到 `33db04fb`，包含 tag `v0.1.124`（`f3577bc6`）与 `v0.1.125`（`a466e80e`）及后续修复提交。
- 本次官方更新包含 Codex image bridge 控制、OAuth 账号导入优化、登录注册条款确认、GitHub/Google 邮箱快捷登录、内容审计风控中心、thinking beta injection 默认关闭、Anthropic passthrough timeout 稳定性修复、模型白名单更新、CI 安全与 lint 修复等。
- 本地分支包含 free OAuth 生图 web2api、图片尺寸计费分级、OAuth detach、TTFT trace、probe-models、available models、EasyPay ezfpy、GitHub Actions 部署适配等补丁，需要 rebase 到最新官方主线。
- 当前仓库实际维护分支仍为 `main`；rebase 后本地 `main` 基于 `upstream/main=33db04fb`，39 个本地 commit 全部 replay。

**影响文件（冲突解决）**：
- `frontend/src/i18n/locales/en.ts`
- `frontend/src/i18n/locales/zh.ts`
- `frontend/src/stores/app.ts`
- `frontend/src/views/admin/SettingsView.vue`
- `backend/internal/server/api_contract_test.go`

**改动摘要**：
- 已 fetch upstream 并将本地 `main` rebase 到 `upstream/main=33db04fb`。
- 解决 5 个文件的冲突，均为"两个新功能区块同位置插入"模式：官方新增 `availableModels` 功能区块与本地已有的 `riskControl` 区块在同一位置，两者都保留。
- 冲突策略：i18n 文案、store 默认值、SettingsView card、api_contract_test 期望 JSON 中同时保留 `risk_control_enabled` 和 `available_models_enabled`。
- 本地所有关键补丁（web2api 路由、size tier 计费、OAuth detach、probe-models、TTFT trace）经 rebase 后自动重放，无需手工修改。

**与官方差异原因**：
- 官方新增了 Codex image bridge、风控中心、邮箱快捷登录等功能，但本地仍需保留 free OAuth 生图 web2api、TTFT trace、图片尺寸计费分级、probe-models 等魔改。
- 本地 `risk_control_enabled` 设置项来自官方本次更新的风控中心功能，与本地 `available_models_enabled` 并存无冲突。

**rebase 风险点**：
- 官方新增 `codex_image_generation_bridge` 控制（Codex image bridge toggle），后续需确认该开关与本地 free OAuth web2api 路由不互相覆盖；当前两者作用于不同账号类型（bridge 针对 Codex 账号，web2api 针对 free OAuth 账号）。
- 官方 `stop default redact thinking beta injection` 修改了 gateway beta 参数注入逻辑，后续若继续调整 beta/thinking 参数，需确认不影响本地 TTFT trace 的 stage 覆盖。
- 官方 OAuth 账号导入优化与本地 probe-models 功能同属 admin/account 路径，后续若继续调整 account handler/wire，需关注冲突。

**验证结果**：
- 已完成源码级 rebase，解决 5 个文件手工冲突。
- 关键 grep 确认：web2api 路由、size tier 计费、OAuth detach、probe-models handler 均完好。
- 待 GitHub Actions 验证：前端 embed 构建、`go test ./...`、后端 Linux 二进制构建、远程部署与健康检查。

---

### 2026-07-22：Rebase 到 upstream v0.1.126（18790386）
**类型**：上游同步 / rebase

**背景**：
- 上游从 `33db04fb` 推进到 `18790386`（tag `v0.1.126`），约 20+ 提交
- 本地 67 个魔改提交需要 replay

**上游主要变化**：
- `cache_control` 改写默认关闭，新增 `rewriteMessageCacheControlIfEnabled` 配置开关
- OpenAI unpriced model 记录零成本（`isUsagePricingUnavailableError`）
- OpenAI 429 plan type 同步（`syncPlanTypeFromUsageLimitError`）
- OpenAI messages multi-tool continuation 保留多工具上下文
- Antigravity UA 可配置版本
- tool name 改写测试完善
- Airwallex 支付 + 多币种支持
- deploy docker-compose 移除宿主机端口映射
- 前端大量重构（删除 AvailableModelsView、AnnouncementsView 等）
- `collectCacheControlPaths` 新增 `toolPaths` 返回值，`enforceCacheControlLimit` 优先移除工具断点

**冲突文件与解决策略**：
1. `backend/internal/service/setting_service.go`
   - 冲突原因：上游在 `GetAvailableChannelsRuntime` 后加了 `GetAntigravityUserAgentVersion`，我们在同位置加了 `AvailableModelsRuntime`
   - 解决：保留两者，上游的 `GetAntigravityUserAgentVersion` 在前，我们的 `AvailableModelsRuntime` 在后
2. `backend/internal/service/gateway_messages_cache.go`
   - 冲突原因：上游加了 `"context"` import，我们加了 `"bytes"` import
   - 解决：两个 import 都保留

**本地补丁影响评估**：
- kiro_gateway.go：不受影响（纯本地文件，上游无此文件）
- openai_images_web2api.go：不受影响（纯本地文件）
- openai_images_capability.go：不受影响（纯本地文件）
- openai_first_token_trace.go：不受影响（纯本地文件）
- 非 mimicry 路径 prompt caching 补丁：不受影响（我们直接调用 `stripMessageCacheControl` + `addMessageCacheBreakpoints`，不经过上游新增的配置开关）
- OAuth context detach：确认保留（`detachUpstreamContext` 仍在 openai_gateway_service.go 中）
- web2api 路由分支：确认保留（`shouldUseOpenAIImagesWeb2API` / `shouldFailoverOpenAIImagesOAuthResponse` 仍在）

**验证结果**：
- 源码级 rebase 完成，67 个提交全部成功 replay，仅 2 个冲突已手工解决
- 关键 grep 确认：kiro stream fix、web2api 路由、OAuth detach、images capability 均完好
- 待 GitHub Actions 验证：前端 embed 构建、`go test ./...`、后端 Linux 二进制构建、远程部署与健康检查

---

### 2026-05-24：跟进 upstream v0.1.127-v0.1.130 并保留本地补丁
**类型**：上游同步 / rebase / 冲突处理

**背景**：
- 官方 `upstream/main` 从 `18790386` 推进到 `63b0631a`，最新 tag 为 `v0.1.130`。
- 官方更新包含：OpenAI/Codex UA 与 Responses 路由修复、OpenAI 账号 cooldown 调度、Chat Completions 测试路径、风险控制按模型生效、邮箱白名单通配符、邮件模板/通知、渠道监控 API 模式、Bedrock Claude Code 兼容、用户用量按平台/日明细、OpenAI Images `n` 参数与图片错误透传、反代真实 IP/ACL、依赖安全修复等。
- 本地 `main` 的 69 个魔改提交需要继续 replay 到官方主线。

**冲突文件与解决策略**：
- OpenAI Images / web2api / 图片计费：保留本地 free OAuth 非流式文生图默认走 `web2api`；采用 upstream 新的统一图片计费 helper 入口，但恢复本地语义：只有官方 `1024x1024` 算 `1K`，自定义/未知有效尺寸最低 `2K`，超过 `2560*1440` 像素或超宽/超高边界按 `4K`，非法/双边超约束回退 `2K`。
- OpenAI TTFT trace：保留本地 `openai.ttft_trace` 埋点；合并 upstream 在 account slot 释放上的 defer 语义，避免重复释放；保留 stream 首 SSE、首 chat chunk、forward_ms 等 trace 字段。
- OpenAI OAuth legacy `/responses`：确认继续保留 upstream detached upstream context 语义，避免客户端断开影响上游收尾/计费。
- Account scheduled test：同时保留 upstream APIKey 不支持 Responses 时的 Chat Completions 测试路径，以及本地 OAuth 定时轻量探测 `wham/usage` 语义。
- `setting_service.go`：同时保留 upstream OpenAI Codex UA 设置缓存和本地 Available Models runtime 开关。
- Kiro 相关：保留 Kiro 401 临时冷却、OpenAI 格式端点兼容、白名单+映射同时生效、credits 用量记录与展示；合并 upstream 用量表图片尺寸字段和本地 `kiro_credits` 字段。

**本地补丁影响评估**：
- `openai_images_web2api.go`、`shouldUseOpenAIImagesWeb2API`、`shouldFailoverOpenAIImagesOAuthResponse` 语义需在 Actions 后重点确认。
- `NormalizeImageBillingTierOrDefault` 现在是 OpenAI Images 与其他图片路径共用入口；后续 upstream 若继续改图片尺寸分级，要优先保护本地 1K/2K/4K 分级规则。
- `openai_first_token_trace.go` 与 handler/service trace 埋点仍为本地排障能力；后续 OpenAI gateway 冲突需继续防止丢失。
- Kiro credits 迁移 `backend/migrations/136_add_usage_log_kiro_credits.sql` 与 usage log 50 列插入/扫描顺序需要由 CI/Actions 验证。

**验证结果**：
- 源码级 rebase 已完成，`upstream/main=63b0631a` 已成为当前 `main` 祖先。
- 已执行轻量检查：`gofmt` 处理手工合并过的 Go 文件；`git diff --check` 通过；backend/frontend 源码未发现真实冲突标记。
- 未在服务器本机执行 `pnpm install`、`pnpm run build`、`go test ./...`、`go build` 或 `systemctl restart`。
- 待 GitHub Actions 验证：前端 embed 构建、`go test ./...`、后端 Linux 二进制构建、远程部署与健康检查。

---

### 2026-06-07：Rebase 到 upstream v0.1.133 (aa69e394)
**类型**：upstream 跟进

**背景**：
- 上游从 v0.1.130 (f7ac5e59) 推进到 v0.1.133 (aa69e394)，约 60 个 first-parent 提交
- 主要变化：Codex Responses ↔ Chat Completions 桥接重构、WS Codex 生图桥接修复、OpenAI OOM 处理、failover 请求体重映射、WS usage dedup、OAuth refresh enrichment (#2881)、添加账号时同步上游模型、用量窗口/tooltip 修复、Claude Code count_tokens 修复、admin usage 优化、platform quota 连接优化、Gemini 消息工具排序修复、账号配额阈值自动暂停、定价元数据更新
- 上游还做了大量 gateway 重构：introduce request body refs、remove parsed request object graphs、introduce OpenAI request view、defer request map decoding、snapshot usage worker inputs

**冲突解决摘要**：
- `gateway.go` 路由：保留本地 embeddings 路由 + 采纳上游 `/images/capability` 路由
- `openai_images.go`：采纳上游 `RequiredCapability` 检查，保留本地 `requestModel` 变量
- `openai_account_scheduler.go`：保留本地 native→basic 回退逻辑（free OAuth web2api 所需）
- `openai_images_test.go`：保留双方测试（error body read limit + capability unsupported params）
- `openai_chat_completions.go` / `openai_gateway_handler.go`：合并上游 `QuotaPlatform` 签名 + 本地 TTFT trace 计时
- `openai_gateway_service.go`：只保留 TTFT trace timing，去掉已被上游重构废弃的 body re-serialize 块
- `setting_service.go`：保留双方（本地 Codex plugin 缓存 + 上游 Available Models runtime）
- `CreateAccountModal.vue`：保留 probe-models button + 本地 `:sync-credentials` prop
- `EditAccountModal.vue`：保留 pool mode retry status codes 解析 + probingModels ref
- `ratelimit_service.go`：保留上游详细注释 + handleAuthError/break + 本地 Kiro 类型判断
- `openai_oauth_service.go` / `wire.go`：保留 `resolveChatGPTSubscriptionAccountID` + `enrichTokenInfoFromWhamUsage` + `ProvideOpenAIOAuthService`

**跳过的本地补丁**：
- `fix(gateway): retry once on upstream empty stream for Responses/CC paths` — 上游对 gateway forward 做了 request body refs 重构，旧的 `attemptOnce` 闭包实现与新代码结构不兼容。需要在新上游代码结构上重新实现。

**待办**：
- [ ] 在新上游代码结构上重新实现 empty stream retry（检测空流→同账号重试一次→对客户端透明）
- [ ] 待 GitHub Actions 验证：前端 embed 构建、`go test ./...`、后端 Linux 二进制构建、远程部署与健康检查

**验证结果**：
- 源码级 rebase 已完成，`upstream/main=aa69e394` 已成为当前 `main` 祖先
- 本地 83 个补丁 commit 在 upstream/main 之上
- 源码无残留冲突标记
- 未在服务器本机执行构建/测试/重启
- 待 GitHub Actions 验证

---

### 2026-06-07：Rebase 到 upstream v0.1.134 (f868f7cb)
**类型**：upstream 跟进 / rebase / 源码级冲突修复

**背景**：
- 上游从 `v0.1.133` (`aa69e394`) 推进到 `v0.1.134` (`f868f7cb`)，first-parent 约 43 个提交。
- 主要变化包括：Go patch version bump 到 `1.26.4`、Responses/Chat Completions stream lifecycle 修复、OpenAI image upstream error passthrough、OpenAI image ratelimit cooldown failover、OpenAI Messages terminal event failover、DeepSeek/CC Responses bridge、sticky account / scheduler snapshot sync、usage cache token split、usage error requests、proxy quality pass、DB pool/leader lock/Postgres DSN 修复、Redis replicate commands 兼容、CWE stored XSS/key oracle 修复等。
- 本地需要在新上游基础上继续保留：free OAuth 生图 web2api、图片尺寸计费分级、OAuth legacy /responses detached upstream context、TTFT trace、Kiro/Antigravity/available models/usage credits、empty stream retry 与 GitHub Actions 部署流程。

**冲突解决摘要**：
- `backend/internal/payment/provider/easypay.go`：合并上游 query 响应解析增强与本地 ezfpy findorder/code_url 兼容，保留 fallback 查询与 metadata pid。
- `openai_chat_completions.go` / `openai_gateway_handler.go`：保留上游严格 `stream` 字段类型校验，并补回本地 `MaybeStartOpenAITTFTTrace`。
- `openai_gateway_chat_completions.go` / `openai_gateway_service.go`：合并上游 stream failover/terminal event 语义与本地 TTFT trace 标记，保留空流/failed event failover 与模型替换、图片输出归一化。
- `frontend/src/stores/app.ts` / `frontend/src/types/index.ts`：同时保留上游 `service_quota_enabled` 与本地 `available_models_enabled` public setting 字段。
- `UsageTable.vue` / `UsageView.vue`：合并上游图片/Token 计费 tooltip 与本地 Kiro credits 展示、CSV 导出、空值安全。
- `backend/cmd/server/wire_gen.go`：解决 token refresh 补丁与当前 OpenAI OAuth provider 构造冲突，保留单一 `ProvideOpenAIOAuthService(..., privacyClientFactory)`。
- `backend/internal/service/wire.go`：rebase 后补正 provider set，使用 `ProvideOpenAIOAuthService` 替代直接 `NewOpenAIOAuthService`，避免未来重新生成 wire 时丢失 privacy factory 注入。

**关键本地补丁复核**：
- OpenAI free OAuth 生图 web2api 路由仍在：`shouldUseOpenAIImagesWeb2API(account, parsed)` 仍接入 OAuth images 分支，`plan_type=free` 与 `openai_images_transport` 覆盖项保留。
- 图片尺寸计费分级仍在：`normalizeOpenAIImageSizeTier` / `NormalizeImageBillingTierOrDefault` 与相关 size tier 测试仍存在。
- OAuth legacy `/responses` detached upstream context 仍在：OAuth 账号路径保留 `detachUpstreamContext(ctx)`，防止客户端 cancel 影响上游计费语义。
- Empty stream retry 已保留在新结构：`gateway_forward_as_responses.go` 与 `gateway_forward_as_chat_completions.go` 的 buffered path 继续在空流时同账号重试一次。
- 文生图适配代码未被本次冲突直接破坏；上游新增 image error passthrough / ratelimit cooldown failover 与本地 web2api 路由并存。

**验证结果**：
- 源码级 rebase 已完成，`upstream/main=f868f7cb` 已成为当前 `main` 祖先。
- 已执行 `git diff --check` 通过。
- 本地定向 Go 测试尝试未作为验证依据：上游 `go.mod` 已 bump 到 `go 1.26.4`，当前服务器未缓存对应 toolchain，`go test` 下载/编译多次超时；按 playbook 交由 GitHub Actions 完成全量验证。
- GitHub Actions 首轮/二轮发现并修复 rebase 语义错位：EasyPay 查询状态优先级、billing token breakdown 测试 helper。
- GitHub Actions 第三轮验证已通过：`CI` success（frontend、unit tests、integration tests、golangci-lint）、`Build sub2api modded` success（frontend embed、`go test ./...`、Linux amd64 二进制构建、artifact 上传）、`Security Scan` success。
- 未在服务器本机执行前端构建、后端全量测试、后端构建或手动重启。
- 部署：`workflow_dispatch deploy=true` 因当前 GitHub token 权限不足返回 HTTP 403；改用 `[deploy]` 提交触发 build workflow 的受控部署路径。
- GitHub Actions deploy run `27084212652` 已成功：`Detect changed areas`、`Build embedded Linux binary`、`Deploy built binary to server` 全部 success；artifact `sub2api-linux-amd64-3252167f3af8066a12624c3a4da1d5aa5083339f` 上传成功，远端安装后 `systemctl is-active sub2api-modded.service` 返回 `active`，宿主机 health 与 `npm-app` 回源 health 均通过 workflow 判定。

---

### 2026-06-15：Rebase 到 upstream v0.1.136 (e34ad2b1)
**类型**：upstream 跟进 / rebase / 源码级冲突修复

**背景**：
- 上游从 `v0.1.134` (`f868f7cb`) 推进到 `v0.1.136` (`e34ad2b1`)，first-parent 24 个提交。
- 主要变化包括：`v0.1.135`/`v0.1.136` 版本同步、admin compliance acknowledgement gate、账号分组调度索引、OpenAI body replacement 预计算修复、idempotency UTF-8 截断、gateway upstream error double-write 修复、debug log loop 优化、Bedrock beta token/error passthrough 修复、Claude Fable 5、admin users 按 API key 分组过滤、非流式响应 Content-Type 修复、5h ResetsAt 同步、代理有效期与失败回退、exclusive group access/sticky group 校验、OpenAI 跨组 previous_response_id 保护、prompt cache key 传递等。
- 本地需要继续保留：OpenAI free OAuth 生图 web2api、图片尺寸计费分级、OAuth legacy `/responses` detached upstream context、empty stream retry、TTFT trace、Kiro credits、available models、GitHub Actions 构建与受控部署流程。

**冲突解决摘要**：
- `frontend/src/api/admin/accounts.ts`：本地 `probeModels` 导出与上游 `revertProxyFallback` 导出在 `accountsAPI` 对象末尾冲突；合并策略为同时保留两者，避免丢失本地模型探测入口或上游代理回退入口。
- 其余提交自动重放；未发现未解决冲突文件。

**关键本地补丁复核**：
- OpenAI free OAuth 生图 web2api 仍在：`shouldUseOpenAIImagesWeb2API(account, parsed)` 继续接入 OAuth images 分支；`plan_type=free` 默认走 web2api，`openai_images_transport=web2api/responses` 覆盖项仍生效；API key、team/plus/pro 与流式/高级参数请求继续走原链路。
- web2api failover 仍在：`shouldFailoverOpenAIImagesOAuthResponse` 继续识别 `Tool choice 'image_generation' not found in 'tools' parameter.` 类错误并触发 failover。
- 图片尺寸计费分级仍在：`ClassifyImageBillingTier` / `NormalizeImageBillingTierOrDefault` 继续保持本地语义，仅 `1024x1024` 为 `1K`，自定义有效尺寸最低 `2K`，超过 `2560*1440` 像素为 `4K`，非法尺寸回退 `2K` 且不阻断 passthrough。
- OAuth legacy `/responses` detached upstream context 仍在：OAuth 上游请求继续使用 `detachUpstreamContext(ctx)`；TTFT `build_upstream_ms` 埋点仍围绕构建上游请求设置。
- Empty stream retry 仍在：`gateway_forward_as_responses.go` 与 `gateway_forward_as_chat_completions.go` 的 buffered path 仍会在空流时同账号重试一次，streaming client path 不重试。
- Kiro / usage / available models / TTFT trace 仍在：`AccountTypeKiro`、`credits_per_dollar`、`usage_logs.kiro_credits` 迁移与 repository insert/scan、usage DTO/前端 Kiro credits 展示与 CSV 导出、available models 页面/侧栏/public flag、`openai.ttft_trace` 与 `first_token_ms` 链路均静态复核存在。

**验证结果**：
- 源码级 rebase 已完成，`upstream/main=e34ad2b1` 已成为当前 `main` 祖先。
- 已执行轻量检查：`git diff --check` 通过；backend/frontend/docs 源码未发现真实冲突标记；`frontend/src/api/admin/accounts.ts` 已确认同时导出 `revertProxyFallback` 与 `probeModels`。
- 未在服务器本机执行 `pnpm install`、`pnpm run build`、`go test ./...`、`go build` 或手动 `systemctl restart`；生产部署通过 GitHub Actions `[deploy]` 提交触发完成。
- GitHub Actions 已通过：`Security Scan` run `27524175751` success（backend-security / frontend-security）；`CI` run `27524175740` success（frontend / test / golangci-lint）；`Build sub2api modded` run `27524175765` success（Detect changed areas / Build embedded Linux binary / Deploy built binary to server）。
- Build workflow 已执行 `pnpm --dir frontend install --frozen-lockfile`、`pnpm --dir frontend run build`（embed 前端产物 `backend/internal/web/dist/index.html` 验证通过）、`go test ./...`、`go build -tags embed -trimpath -ldflags "-s -w"`，并上传 artifact `sub2api-linux-amd64-a625edd3fc6fa75ce567baaddf8b386ae84c8b83`（artifact ID `7629231569`，size `27923229` bytes，zip digest `sha256:fad104a9a4ad7d4db7f03f0a5d9a8989be4ca03843caa5117f291cbc73ce3a8b`）。
- Deploy job 下载该 artifact 后安装到远端并重启 `sub2api-modded.service`；日志显示服务状态 `active`。宿主机 `http://172.19.0.1:18081/health` 与 `npm-app` 容器回源 health 均在 workflow 脚本中作为硬性条件通过，否则 job 会回滚并失败；本次 deploy job success，可判定远端健康检查通过。

---

### 2026-06-16：Rebase 到 upstream v0.1.137 (4a5665da)
**类型**：upstream 跟进 / rebase / 源码级冲突修复

**背景**：
- 上游从 `v0.1.136` (`e34ad2b1`) 推进到 `v0.1.137` (`4a5665da`)，本次 diff 涉及 176 个文件、约 11000 行新增。
- 主要变化包括：国产 LLM 兜底定价（GLM / Kimi / MiniMax、DeepSeek V4 Pro/Flash、kimi-for-coding、doubao-embedding-vision 图文差别）、thinking-protocol 协议感知的 thinking-block 过滤、国产模型 `thinking.type=enabled` 自动填充 `reasoning_effort` 默认值、DeepSeek `reasoning_effort` `max`→`xhigh` 归一化、OpenAI 账号配额查询/重置（`openai_quota_service` + 前端 `OpenAIQuotaResetCell`）、`cyber_policy` 硬阻断全链路透传/审计/计费、`cyber_session_block` 开关、渠道监控检测间隔随机抖动、scheduler outbox dedup_key 去重与清理、token refresh 重试退避、上游 zstd 响应体解压、non-JSON 2xx failover、账号列表展示 account id、Claude OAuth system prompt blocks 配置、OpenAI `/responses` 能力探测增加工具调用校验、Anthropic 429 窗口冷却保留、antigravity system role 合并、IP ACL 拒绝消息含 client ip、`form-data` 依赖 bump 等。
- 本地需要继续保留：OpenAI free OAuth 生图 web2api、图片尺寸计费分级、OAuth legacy `/responses` detached upstream context、empty stream retry、TTFT trace、Kiro 账号类型/credits 计费、Kiro probe-models + account_id 保存凭据探测、available models、GitHub Actions 构建与受控部署流程。

**冲突解决摘要**（本次仅 3 个文件产生文本冲突，其余 98 个提交自动重放）：
- `backend/internal/server/api_contract_test.go`：上游新增 `cyber_session_block_enabled` / `cyber_session_block_ttl_seconds` 字段与本地 `available_models_enabled` 字段在公开设置契约 JSON 中相邻冲突；该断言使用 `require.JSONEq`（字段集合比较，顺序无关），合并策略为三字段全部保留，与 `settings_view.go` 中同一结构体的实际序列化字段集合一致。
- `frontend/src/api/admin/accounts.ts`：`accountsAPI` 导出对象末尾，上游新增 `queryOpenAIQuota` / `resetOpenAIQuota` 与本地 `probeModels` 冲突；合并策略为三者同时保留，函数定义本身（probeModels:631、queryOpenAIQuota:783、resetOpenAIQuota:791）由自动合并保留。
- `backend/internal/service/gateway_forward_as_chat_completions.go`：上游 `a05d9e87`（国产模型 thinking-enabled 填充默认 `reasoning_effort`）在原单一执行路径插入 `extractCCReasoningEffortFromBody` + `ApplyThinkingEnabledFallback`，与本地 `480d9142`（empty stream retry 把 buffered path 包成重试循环、并将路径拆为 streaming/buffered）结构冲突。合并策略：将 `ApplyThinkingEnabledFallback(reasoningEffort, body, mappedModel)` 上移到两条路径共享的 `reasoningEffort` 声明处（第 134/138 行），使 streaming 与 buffered 路径都受益于国产模型默认 effort 补充；冲突区只保留本地 buffered 重试循环，去掉上游重复的 `reasoningEffort` 声明。对应的 `gateway_forward_as_responses.go` 由自动合并正确保留了上游 `ApplyThinkingEnabledFallback`（第 83 行）与本地 empty stream retry（第 172 行）共存。

**关键本地补丁复核**（rebase 后只读源码自检，7 项全部存在且语义正确）：
- OpenAI free OAuth 生图 web2api 仍在：`shouldUseOpenAIImagesWeb2API`（openai_images_web2api.go:38）transport 覆盖优先、回退 `isOpenAIFreePlan`；`shouldFailoverOpenAIImagesOAuthResponse`（openai_images_responses.go:1327）仍按 400 + `tool choice`/`image_generation`/`not found`/`tools` 子串组合识别并 failover。
- 图片尺寸计费分级仍在：`ClassifyImageBillingTier` / `NormalizeImageBillingTierOrDefault`（image_billing_size.go）保持本地语义——仅 `1024x1024` 为 `1K`，官方白名单 2K/4K，自定义有效尺寸最低 `2K`，超过 `2560*1440` 像素为 `4K`，宽屏超长边（longSide≥3840 且 shortSide≤1024）按 oversize 归 `4K`，非法尺寸回退 `2K` 且不阻断 passthrough。（注：原 patch log 提及的 `parseOpenAIImageSizeDimensions` 标识符已是历史名，等价实现为 `parseImageBillingDimensions`，功能完整。）
- OAuth legacy `/responses` detached upstream context 仍在：`openai_gateway_service.go` OAuth 分支继续使用 `detachUpstreamContext(ctx)`（:2978）；`openai_oauth_passthrough_test.go` 的 `TestOpenAIGatewayService_OAuthLegacy_UpstreamRequestIgnoresClientCancel` 仍在。
- Empty stream retry 仍在：`gateway_forward_empty_stream.go`（`maxEmptyStreamRetries`、`isEmptyStreamResult`）被 responses/CC 两路 buffered path 使用，streaming client path 不重试。
- Kiro probe-models + account_id 修复仍在：`ProbeModelsRequest.APIKey` 已取消 `binding:"required"` 并新增 `AccountID`；api_key 为空时按 account_id 读 `account.GetCredential("api_key")`；安全约束：使用保存 key 时 Kiro 账号无保存 base_url 直接 400、不回退默认 Anthropic URL，请求 base_url 与账号 base_url 不一致也 400；成功响应只返回 `{models,count}` 不回传 key；测试 `account_handler_available_models_test.go` 覆盖 provided key / account 凭据回退 / 跨 base_url 拒绝 / Kiro 无 base_url 拒绝 / 缺 key 400，并 `NotContains` 校验不泄露 key。
- Kiro 账号类型/计费仍在：`AccountTypeKiro`（domain/constants.go:35）、credits-based billing、前端 `EditAccountModal.vue` Kiro 探测传 `account_id`、`types/index.ts` AccountType union 含 `'kiro'`。
- available models 页面/侧栏/public flag 仍在（契约测试已含 `available_models_enabled`）。

**验证结果**：
- 源码级 rebase 已完成，`upstream/main=4a5665da`（v0.1.137）已成为当前 `main` 祖先，`backend/cmd/server/VERSION=0.1.137`。
- 已执行只读自检：全仓 tracked 文件无整行冲突标记（`<<<<<<<`/`>>>>>>>` 均无命中；`=======` 唯一命中为 antigravity 提示词模板字符串字面量，非冲突标记）；上述 3 个冲突文件已确认无残留标记；所有关键本地补丁 commit 在 rebase 后历史中完整保留。
- 已创建备份分支 `backup/pre-rebase-v0.1.137-20260616` 指向 rebase 前的 `main`。
- 未在服务器本机执行 `pnpm install`、`pnpm run build`、`go test ./...`、`go build` 或手动 `systemctl restart`。
- **GitHub Actions 验证（commit `9303c092`，无 `[deploy]`，仅验证）**：
  - `Security Scan` run `27659367368` success（form-data advisory 由上游 bump 到 >=4.0.6 + 本地 audit 例外双重消除）。
  - `CI` run `27659367398` success（frontend / test / golangci-lint，`go test ./...` 通过，证明 rebase 后合并语义正确）。
  - `Build sub2api modded` run `27659367353` success：`Detect changed areas` success、`Build embedded Linux binary` success（embed 前端构建 + `go build -tags embed`，artifact `sub2api-linux-amd64-9303c0922fd801380951f5ef825e0dd59efcd021`，id `7683902110`，size `28010500` bytes）、`Deploy built binary to server` skipped（未带 `[deploy]`，生产未变更）。
- **部署触发**：随后以带 `[deploy]` 的提交推送，由 Build workflow 的 deploy job 下载 artifact、安装到远端并重启 `sub2api-modded.service`，宿主机 `http://172.19.0.1:18081/health` 与 `npm-app` 容器回源 health 作为硬性条件校验，失败则自动回滚。

---

### 2026-06-18：管理员账号清理页面
**类型**：功能 / 管理员工具 / 本地魔改

**背景**：
- 需要管理员在 Sub2API 左侧平级菜单进入“账号清理”页面。
- 管理员可指定源分组，并自行选择需要清理的账号状态。
- 清理动作支持删除账号，或将账号移动到指定目标分组；移动时按本地需求覆盖账号全部原分组，仅保留目标分组。

**影响文件**：
- 新增后端隔离文件：
  - `backend/internal/service/account_cleanup.go`
  - `backend/internal/handler/admin/account_cleanup_handler.go`
  - `backend/internal/server/routes/account_cleanup.go`
- 最小后端接入：
  - `backend/internal/server/routes/admin.go`
- 新增前端隔离文件：
  - `frontend/src/api/admin/accountCleanup.ts`
  - `frontend/src/views/admin/AccountCleanupView.vue`
  - `frontend/src/i18n/locales/modded/zh.ts`
  - `frontend/src/i18n/locales/modded/en.ts`
- 最小前端接入：
  - `frontend/src/api/admin/index.ts`
  - `frontend/src/router/index.ts`
  - `frontend/src/components/layout/AppSidebar.vue`
  - `frontend/src/i18n/index.ts`

**改动摘要**：
- 新增 `/api/v1/admin/accounts/cleanup/preview` 和 `/api/v1/admin/accounts/cleanup`。
- 预览接口按源分组、账号状态、平台、类型、搜索条件匹配账号，并返回分页列表与状态/平台统计。
- 执行接口支持 `delete` 与 `move`；删除要求 `confirm_text=DELETE`；移动使用 `BindGroups(accountID, []targetGroupID)` 覆盖原有全部分组。
- 后端没有扩展 `AdminService` 主接口，而是新增窄接口 `AccountCleanupService` 并由 handler 类型断言调用，避免大量测试 stub 跟随修改。
- 前端新增管理员平级菜单“账号清理”和独立页面；页面强制先预览，再确认执行。
- 本地新增 i18n overlay，避免直接修改巨大 upstream locale 文件。

**与官方差异原因**：
- 官方未提供管理员按分组与状态批量清理上游账号的工作流。
- 当前本地运营需要快速隔离或删除不可用账号，并明确允许移动时覆盖账号原分组。

**rebase 风险点**：
- 主要逻辑集中在新增文件，后续 rebase 通常不应与 upstream 冲突。
- 需要保留 `routes/admin.go` 中 `registerAccountCleanupRoutes(accounts, h)` 这一行本地插桩。
- 需要保留 `AppSidebar.vue` 的 `/admin/account-cleanup` 平级菜单项。
- 需要保留 `router/index.ts` 的 `AdminAccountCleanup` route block。
- 需要保留 `frontend/src/i18n/index.ts` 对 `locales/modded/*` overlay 的加载合并逻辑；后续本地文案优先放 overlay，避免反复改 `zh.ts` / `en.ts`。

**验证结果**：
- GitHub Actions 验证提交：`5520e6d7 feat(admin): add account cleanup page`。
- Security Scan run `27745348424` success：`frontend-security`、`backend-security` 均通过。
- CI run `27745348427` success：`frontend`、`test`、`golangci-lint` 均通过。
- Build run `27745348431` success：`Build embedded Linux binary` 通过，`Deploy built binary to server` 因提交未带 `[deploy]` 被跳过。
- 交互会话中误触发的本地 Go 定向测试/编译已停止或超时，无有效验证结果，不作为本补丁验收依据。
- 生产可见性需要后续带 `[deploy]` 的 GitHub Actions 部署提交完成远端部署与健康检查。

---

### 2026-06-23：Rebase 到 upstream v0.1.138 (85a3b122)
**类型**：upstream 跟进 / rebase / 源码级复核

**背景**：
- 上游从 `v0.1.137` (`4a5665da`) 推进到 `upstream/main=85a3b122`，官方 tag `v0.1.138` 位于 `69366878`，head 还包含 VERSION 同步与 sponsors 更新。
- 本次上游更新重点包括：图片生成 `response.incomplete` 软失败识别与 failover、Gemini tool schema 清理、Vertex Anthropic beta 过滤、OpenAI chat-only API key upstream endpoint 日志、Claude Code 新 CLI billing block 识别、GLM reasoning effort 映射、auto mode Claude Code IDE entrypoint 识别、usage cache token tooltip、邮箱绑定后缀白名单、订阅邀请返利、promo 过期时间清空、prefer soonest reset 调度选项、Docker SELinux bind mount label、Node 24/pnpm workflow 调整等。
- 本地需要继续保留：OpenAI free OAuth 生图 web2api、图片尺寸计费分级、OAuth legacy `/responses` detached upstream context、empty stream retry、TTFT trace、Kiro 账号类型/credits 计费、Kiro probe-models + account_id 保存凭据探测、available models、管理员账号清理页面、GitHub Actions embed 构建与受控部署流程。

**rebase 结果**：
- 已 fetch upstream，并将本地 `main` rebase 到 `upstream/main=85a3b122`；本轮 108 个本地提交自动重放，无文本冲突。
- 已创建 rebase 前备份分支：`backup/main-before-upstream-0.1.138-20260622-235646`。
- 上游删除/调整的官方 workflow 与文档文件没有覆盖本地部署工作流；`.github/workflows/build.yml` 仍保留 `workflow_dispatch deploy` 与 push commit message `[deploy]` 触发部署规则。
- `backend/cmd/server/VERSION` 已随上游为 `0.1.138`。

**关键本地补丁复核**：
- OpenAI free OAuth 生图 web2api 仍在：`openai_images_web2api.go`、`shouldUseOpenAIImagesWeb2API(...)`、`openai_images_transport` override、free plan 非流式 generation 路由与相关测试均存在。
- 上游新增的图片 `response.incomplete` 识别已与本地 web2api/Responses failover 共存：`openai_images_responses.go` 中 `response.incomplete`、`summarizeOpenAIImagesNoOutputBody` 和 `openai_images_incomplete_test.go` 均存在。
- 图片尺寸计费分级仍在：`normalizeOpenAIImageSizeTier` / `NormalizeImageBillingTierOrDefault` 与 `TestResolveOpenAIResponsesImageBillingConfigSupportsOfficialAndCustomSizes` 等测试仍存在。
- OAuth legacy `/responses` detached upstream context 仍在：OAuth 分支继续使用 `detachUpstreamContext(ctx)`，`TestOpenAIGatewayService_OAuthLegacy_UpstreamRequestIgnoresClientCancel` 仍存在。
- 管理员账号清理页面仍在：后端 `account_cleanup` handler/service/routes、`registerAccountCleanupRoutes(accounts, h)`、前端 `/admin/account-cleanup` route、左侧平级菜单与 `locales/modded/*` i18n overlay 均存在。
- Kiro probe-models、available models public flag、Kiro 账号类型/credits 计费等长期本地补丁仍可在源码中定位。

**验证结果**：
- 源码级 rebase 已完成，`upstream/main=85a3b122` 已成为当前 `main` 祖先。
- 已执行轻量源码检查：全仓 tracked 文件无整行冲突标记；`git diff --check` 通过。
- 未在服务器本机执行 `pnpm install`、`pnpm run build`、`go test ./...`、`go build` 或手动 `systemctl restart`。
- **GitHub Actions 验证（commit `fc159b20`，无 `[deploy]`，仅验证）**：
  - `Security Scan` run `28002083800` success：`frontend-security`、`backend-security` 均通过。
  - `CI` run `28002083804` success：`frontend`、`test`、`golangci-lint` 均通过。
  - `Build sub2api modded` run `28002083802` success：`Detect changed areas` 与 `Build embedded Linux binary` 均通过，`Deploy built binary to server` 因提交未带 `[deploy]` 被跳过。
  - Build artifact：`sub2api-linux-amd64-fc159b20895924228f2647cfd84dcb4dfccf5739`，artifact id `7811501249`，size `28038729` bytes，digest `sha256:0878c11416e6fac1663a6d69a02aa21327a44529158e87664fee93eeb7b6b961`。
- **部署提交（commit `2df198f8`，带 `[deploy]`）**：
  - `Security Scan` run `28002455916` success：`backend-security`、`frontend-security` 均通过。
  - `CI` run `28002455988` success：`frontend`、`test`、`golangci-lint` 均通过。
  - `Build sub2api modded` run `28002455935` success：`Detect changed areas`、`Build embedded Linux binary`、`Deploy built binary to server` 全部通过。
  - Deploy artifact：`sub2api-linux-amd64-2df198f809193d9f1022b1635efdffca000e09af`，artifact id `7811560486`，size `28038732` bytes，digest `sha256:1eb5fc672cb1d6b0e0cc254b1f3df7d840a0016b7158abae9db1c517b3430cd6`。
  - Deploy job `82877807728` 的 `Install, restart and verify` step success；按 workflow 硬性校验，远端二进制已安装并重启 `sub2api-modded.service`，宿主机 `/health` 与 `npm-app` 容器回源 health 均通过，否则该 job 会失败并回滚。

---

## 后续记录模板

### YYYY-MM-DD：补丁名称
**类型**：功能 / 修复 / 运维适配 / 反代适配 / 风控适配

**背景**：
- 

**影响文件**：
- 

**改动摘要**：
- 

**与官方差异原因**：
- 

**rebase 风险点**：
- 

**验证结果**：
- 
