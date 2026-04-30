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
