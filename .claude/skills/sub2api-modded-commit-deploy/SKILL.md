---
name: sub2api-modded-commit-deploy
description: 当用户明确要求“提交并部署 / commit and deploy / 推送并部署 / 发版”sub2api-modded 时使用。固定本项目从检查、测试、提交、推送到 GitHub Actions 构建部署、健康检查与失败回滚的安全流程。
---

# sub2api-modded 提交并部署 SOP

当用户明确要求以下任一动作组合时优先使用本技能：
- “提交并部署”
- “commit and deploy”
- “推送并部署”
- “发版”
- “把这次改动提交然后上线/重启生效”

仅出现“提交”时只执行提交流程；仅出现“部署”时优先走 GitHub Actions 部署流程。没有明确要求提交时，不要创建 git commit。

## 运行边界

- 源码仓：`/root/sub2api-modded`
- 远程仓库：`origin`，分支：`main`
- 上游仓库：`upstream`，不要推送到 upstream
- GitHub Actions workflow：`.github/workflows/build.yml`
- GitHub Actions 部署触发方式：push 到 `main` 且最新 commit message 包含 `[deploy]`
- GitHub Actions 部署完成标记：`/root/sub2api-modded-data/deployments/last-github-actions-deploy.json`
- 二进制：`/root/sub2api-modded/bin/sub2api`
- 服务：`sub2api-modded.service`
- 健康检查：`http://172.19.0.1:18081/health`
- NPM 容器健康检查：`docker exec npm-app curl -sS -o /dev/null -w '%{http_code}\n' http://172.19.0.1:18081/health`

## 绝对禁止

1. 不要使用破坏性 git 命令：`git reset --hard`、`git checkout -- .`、`git restore .`、`git clean -f`、强推。
2. 不要提交 `.env`、密钥、凭据、数据库 dump、token、私钥。
3. 不要推送到 `upstream`。
4. 不要停旧生产 `sub2api`，只操作 `sub2api-modded.service`。
5. 不要跳过测试或 hook，除非用户明确要求。
6. 默认不要在服务器本机执行前端 build；构建压力应交给 GitHub Actions。

## GitHub Secrets 前置条件

远程部署需要 GitHub 仓库配置 Secrets：

- `DEPLOY_HOST`：服务器公网 SSH 地址
- `DEPLOY_PORT`：SSH 端口，未配置时 workflow 默认 22
- `DEPLOY_USER`：SSH 用户，未配置时 workflow 默认 root
- `DEPLOY_SSH_KEY`：可登录服务器并操作 `/root/sub2api-modded/bin/sub2api`、`systemctl restart sub2api-modded.service` 的私钥

如果这些 Secrets 未配置，push 后只会完成 build，deploy job 会失败。不要回退到本机前端 build，除非用户明确要求本机部署。

## 提交流程

1. 并行检查：
   - `git status --short`
   - `git diff --stat && git diff`
   - `git log --oneline -5`
   - `git remote -v`
2. 审查改动：
   - 确认改动范围只包含本次任务相关文件。
   - 如发现疑似密钥或大文件，停止并提示用户。
3. 运行必要验证：
   - 后端改动：不要在本机跑 `go test`；完整后端测试由 GitHub Actions 执行。仅做轻量语法检查（如确认 import 无误）。
   - 前端改动：优先只做轻量语法/JSON 校验；完整前端 build 由 GitHub Actions 执行。
   - 配置/脚本改动：做格式或语法基础校验。
4. 只 stage 相关文件，避免 `git add -A` 误加敏感文件。
5. 创建新 commit，不 amend。
   - 只提交：正常 commit message，例如 `fix(openai): ...`。
   - 提交并部署：commit message 末尾必须包含 `[deploy]`，例如 `fix(openai): use wham usage probe for scheduled tests [deploy]`。
6. 推送：`git push origin main`。

## GitHub Actions 构建部署流程

`build.yml` 会：
1. 检测 frontend/backend/workflow/deploy 变更范围并打印构建计划。
2. 仅当前端 dist cache 未命中时，在 GitHub-hosted runner 上安装 Node/pnpm 依赖并构建前端。
3. 仅当 backend 或 build workflow 变更时运行 `go test ./...`；frontend-only 部署不会跑后端测试。
4. 只要需要构建/部署，仍会执行 `go build -tags embed`，因为前端 dist 被嵌入 Go 二进制。
5. 上传 artifact。
6. 如果 commit message 包含 `[deploy]`，deploy job 会通过 SSH：
   - 上传新二进制到 `/tmp/sub2api-${GITHUB_SHA}`
   - 备份旧二进制到 `/root/sub2api-modded/bin/sub2api.bak.<run_id>.<attempt>`
   - 安装新二进制
   - 重启 `sub2api-modded.service`
   - 检查宿主机 `/health`
   - 检查 `npm-app` 容器访问 `/health`
   - 成功后写入 `/root/sub2api-modded-data/deployments/last-github-actions-deploy.json`
   - 失败时恢复备份并重启

## 助手等待部署完成

提交并部署后，助手不要去 GitHub 页面点按钮。推送后在服务器本地等待 GitHub Actions 写入 marker 文件：

```bash
/root/sub2api-modded-data/deployments/last-github-actions-deploy.json
```

等待逻辑：
- 读取当前 commit SHA。
- 最多等待 30 分钟。
- 每 10-20 秒检查 marker 文件是否存在且 `commit` 等于当前 SHA。
- marker 命中后再检查：
  - `systemctl is-active sub2api-modded.service`
  - 宿主机 `/health`
  - NPM 容器 `/health`

如果 30 分钟内未命中 marker：
- 报告 GitHub Actions 构建/部署可能未完成或失败。
- 不要本机前端 build 补救，除非用户明确要求。

## 本机紧急部署例外

只有用户明确说“不要等 GitHub Actions / 直接本机部署 / 本地构建部署”时，才使用本机部署：
- 前端：`pnpm --dir frontend run build` 或用户明确允许时 `build:fast`
- 后端：`/usr/local/go1.26.2/bin/go build -tags embed -o /root/sub2api-modded/bin/sub2api ./cmd/server`
- 重启和健康检查按 runtime ops SOP 执行

## 对外汇报模板

完成后简洁报告：
- commit hash
- 是否已 push 到 `origin/main`
- 是否触发 GitHub Actions deploy（commit 是否包含 `[deploy]`）
- GitHub Actions marker 是否命中
- 服务状态
- 宿主机 `/health` 状态码
- NPM 容器 `/health` 状态码
- 是否有回滚或遗留风险
