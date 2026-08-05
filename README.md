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

顶部条：容器健康（healthy=绿）、local/public livez（200=绿）、当前 backend/web image。

左右两栏，各自标出差异：

- **RELEASES**（左）— 最近 5 个 release，`live:` 显示生产当前 commit；匹配上的绿色 `●`，没匹配的灰色 `○`——不匹配说明生产跑的版本不在这 5 条 release 记录里
- **DATABASE**（右）— :80 vs :8082 歌曲/艺人数量对比，diff 高亮；:8082 领先时提示按 `m` 合并

## 职责边界

Jose 只做 :80 部署。代码改动、镜像构建、8082 部署验收全归 ruby。
