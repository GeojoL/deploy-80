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
| `d` | 部署最新 release（apply → verify → browser evidence 三步连跑） |
| `r` | 回滚代码/镜像到上一个 release（不恢复 DB） |
| `m` | 把 :8082 业务数据并入 :80（需二次确认） |
| `l` | 查看容器日志（backend / scheduler / proxy / db） |
| `q` | 退出 |

## 面板内容

**顶部状态栏**（2 行）：
- Line 1：容器状态简写 `containers: backend● scheduler● proxy● db●`（绿●=running，红●=down）
- Line 2：health checks 和 images `livez: local 200 • public 200 • be:hash we:hash`

**左右两栏**：
- **RELEASES**（左）— 最近 5 个 release，每行格式 `● hash date (live)` 或 `○ hash date`；live commit 高亮绿色
- **DATABASE**（右）— :80 vs :8082 数据对比（songs/artists），diff 高亮；:8082 领先时提示按 `m` 合并

## 职责边界

Jose 只做 :80 部署。代码改动、镜像构建、8082 部署验收全归 ruby。
