# dp80 — :80 Deploy Panel

Jose 的 Gandalf 80 端口部署面板。

## 用法

```bash
dp80          # 交互面板
dp80 status   # 只读状态（非交互）
```

## 命令

| 键 | 操作 |
|---|---|
| `d` | 部署最新 release（apply → verify → browser evidence 三步连跑） |
| `r` | 回滚代码/镜像到上一个 release（不恢复 DB） |
| `m` | 把 :8082 业务数据并入 :80（需二次确认） |
| `l` | 查看容器日志（backend / scheduler / proxy / db） |
| `q` | 退出 |

## 面板内容

- CONTAINERS — 4 个容器健康状态（healthy=绿，down=红）
- HEALTH — local / public livez 响应码（200=绿，其他=红）
- IMAGES — 当前运行的 backend / web image digest 前缀
- DATA — :80 vs :8082 歌曲/艺人数量对比，diff 黄色高亮
- GIT — 生产 repo HEAD
- RELEASES — 最近 5 个 release，有 manifest 的显示 commit hash

差异提示：:8082 歌曲多于 :80 时底部自动出现 `merge` 提示。

## 职责边界

Jose 只做 :80 部署。代码改动、镜像构建、8082 部署验收全归 ruby。
