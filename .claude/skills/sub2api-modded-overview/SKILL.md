---
name: sub2api-modded-overview
description: 在本项目中处理 sub2api 魔改、代码定位、构建方式确认、生产与魔改环境区分时优先使用。用于快速理解当前 sub2api-modded 的目录、运行拓扑、边界和维护上下文。
---

# sub2api-modded 项目总览

当任务涉及以下任一主题时，优先加载本技能：
- 需要快速理解这个 sub2api 魔改仓怎么组织
- 不确定该去前端、后端、部署目录还是 systemd 查
- 需要区分旧生产环境与新魔改环境
- 需要为后续魔改、rebase、排障建立上下文

## 一句话定位
`/root/sub2api-modded` 是基于官方 `Wei-Shaw/sub2api` 克隆出来的**独立魔改源码仓**，用于在不影响旧生产 Sub2API 的前提下进行持续源码修改、验证和后续切流准备。

## 当前已确认的环境事实
- 旧生产部署目录：`/root/sub2api-deploy`
- 新魔改源码仓：`/root/sub2api-modded`
- 新依赖部署目录：`/root/sub2api-modded-deploy`
- 新宿主机数据目录：`/root/sub2api-modded-data`
- 新环境变量文件：`/root/sub2api-modded.env`
- 新 systemd 服务：`sub2api-modded.service`
- 新监听地址：`172.19.0.1:18081`
- 新 Postgres：`172.23.0.10:5432`
- 新 Redis：`172.23.0.11:6379`

## 目录地图

### 源码主体
- `frontend/`
  - Vue 3 + Vite 前端
- `backend/`
  - Go 后端
- `backend/cmd/server/`
  - 服务主入口
- `backend/internal/web/dist/`
  - 前端构建产物嵌入目录
- `bin/`
  - 构建出的二进制

### 文档层
优先看：
- `docs/runtime-layout.md`
- `docs/build-and-run.md`
- `docs/rebase-workflow.md`
- `docs/magic-patch-log.md`

### 技能层
- `.claude/skills/sub2api-modded-overview`
- `.claude/skills/sub2api-modded-runtime-ops`
- `.claude/skills/sub2api-modded-rebase-playbook`
- `.claude/skills/sub2api-modded-image2-integration`

## 当前构建模型
- 前端先在 `frontend/` build
- 产物输出到 `backend/internal/web/dist`
- 后端使用 `-tags embed` 编译
- 二进制运行在宿主机 systemd，而不是新的应用容器里

## 魔改特有功能
### Kiro 账号类型
- 通过 kiro-rs 代理转发 Anthropic Messages API
- 支持 credits 计费（`extra.credits_per_dollar`）
- 支持模型白名单 + 模型映射同时生效（合并存储到 `model_mapping`）
- 支持双向模型名回写（请求方向 + 响应方向）
- 前端：CreateAccountModal / EditAccountModal 中有专门的 kiro 区块

### OpenAI Responses API 兼容
- `/v1/responses` 和 `/responses` 端点
- Anthropic 平台分组可通过 `ForwardAsResponses` 接收 OpenAI Responses 格式请求
- 自动转换：Responses → Anthropic Messages → Responses（含流式 SSE 翻译）
- 支持 Codex CLI / Codex Desktop 等 OpenAI Responses API 客户端

## 当前运行模型
- 外部链路必须是：`Cloudflare -> NPM -> 172.19.0.1:18081`
- 新应用本体跑宿主机
- 新 Redis / Postgres 继续容器化
- 旧生产环境继续跑，不主动动

## 维护时的关键边界
1. 不要把旧生产目录当魔改仓使用
2. 不要默认复用旧生产 Redis / Postgres
3. 不要把新服务监听改成 `0.0.0.0`
4. 不要绕过 NPM / Cloudflare 设计公网入口
5. 不要把真实密码、JWT、TOTP 密钥写进 docs 或 skills

## 与其他技能的关系
### 遇到这些问题时联动 `sub2api-modded-image2-integration`
- `/v1/images/*`、OpenAI Images、gpt-image-2、能力探测、edits/uploads 相关改动或排障
- `image2-platform` 调用 sub2api 图片网关失败
- 需要判断 Image2 产品层与 sub2api 网关层职责边界
- 有人试图在 sub2api 前端恢复 “Image2 生图” 页面或 `/image2` 路由

### 遇到这些问题时联动 `racknerd-zero-trust-ops`
- NPM 怎么转发到新 sub2api
- UFW 为什么要只放行 `172.19.0.0/16`
- 是否应该监听 `0.0.0.0`
- Cloudflare / NPM / 宿主机互联 / Header 脱敏

## 默认立场
未来在本项目中，先把这里当成“长期魔改主仓 + 独立验证环境”，不要退回到直接改生产容器的工作方式。