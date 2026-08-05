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
| `d` | 一键部署：取最新已 `prepare` 的 release manifest，确认后本地跑 `production-80.sh apply --execute --allow-port80-downtime`（按键即授权，无额外闸） |
| `r` | 刷新面板（重新拉取容器状态/健康检查/release/DB 计数） |
| `m` | 把 :8082 业务数据并入 :80（需二次确认） |
| `l` | 查看迁移审计历史 + 容器日志（backend，已过滤 livez 噪音） |
| `q` | 退出 |

`d` 读取 `/Users/geojol/Documents/Projects/datacenter-kimi/release-manifest/production-80/`
下最新的 release（ID 以 UTC 时间戳开头，字典序即时间序），确认屏显示 release id /
commit / 版本号。注意 apply 只能部署**已 prepare 的包**——8082 出了新东西要先
`production-80.sh prepare` 生成新 manifest，`d` 自动就会捡到最新的那个。

## 面板内容

**顶部状态栏**（2 行）：
- Line 1：容器状态简写 `containers: backend● scheduler● proxy● db●`（绿●=running，红●=down）
- Line 2：health checks 和 images `livez: local 200 • public 200 • be:hash we:hash`

**左右两栏**：
- **RELEASES**（左）— 最近 5 个 release，每行格式 `● hash date (live)` 或 `○ hash date`；live commit 高亮绿色
- **DATABASE**（右）— :80 vs :8082 数据对比（songs/artists），diff 高亮；:8082 领先时提示按 `m` 合并

## 职责边界

Jose 只做 :80 部署。代码改动、镜像构建、8082 部署验收全归 ruby。
