# dp80 — :80 Deploy Panel

Jose 的 Gandalf 80 端口部署面板。Go + bubbletea TUI，源码即本目录。

## 用法

```bash
dp80          # 交互面板
dp80 status   # 只读状态（非交互）
```

alias `dp80` 定义在 `~/.my_script/grabalias.sh`（不在本项目里，全局 alias 统一放那），指向本目录编译出的 `dp80` 二进制。**高频命令**，搬迁本项目目录时务必同步改这个 alias。

## 命令

| 键 | 操作 |
|---|---|
| `r` | 刷新面板（重新拉取容器状态/健康检查/release/DB 计数） |
| `m` | 把 :8082 业务数据并入 :80（需二次确认） |
| `l` | 查看迁移审计历史 + 容器日志（backend，已过滤 livez 噪音） |
| `q` | 退出 |

**没有 `d)eploy`**：`scripts/release/production-80.sh` 的 `apply`/`rollback-code`
要求人工审批的 release-id、40 位 commit、镜像 SHA-256 和证据文件（`complete-browser`
甚至要求真人登录点完 8 个页面留证据），没法安全压成 dp80 里的一次按键——这么做等于绕过
整套审批闸门。真要在 dp80 里接部署/回滚，得先定好接到 SOP 哪一步（多半只能是"列出已
`prepare` 好的 release manifest 供选择，再 ssh 执行 `apply`"这种不替用户做审批决定的
形态），这是待定的独立功能，不是现在这版的范围。

## 面板内容

**顶部状态栏**（2 行）：
- Line 1：容器状态简写 `containers: backend● scheduler● proxy● db●`（绿●=running，红●=down）
- Line 2：health checks 和 images `livez: local 200 • public 200 • be:hash we:hash`

**左右两栏**：
- **RELEASES**（左）— 最近 5 个 release，每行格式 `● hash date (live)` 或 `○ hash date`；live commit 高亮绿色
- **DATABASE**（右）— :80 vs :8082 数据对比（songs/artists），diff 高亮；:8082 领先时提示按 `m` 合并

## 职责边界

Jose 只做 :80 部署。代码改动、镜像构建、8082 部署验收全归 ruby。
