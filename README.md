# deploy-80

Jose 的 Gandalf 80 端口部署操作面板。

## 用法

```bash
dp80            # 交互面板
dp80 status     # 只输出状态（不进交互循环）
```

## 界面

```
╔════════════════════════════════════════════════╗
║     Jose · 80 Port Deploy Panel               ║
╚════════════════════════════════════════════════╝

  CONTAINERS
    backend      running Up 35 minutes (healthy)
    scheduler    running Up 35 minutes (healthy)
    proxy        running Up 34 minutes (healthy)
    db           running Up 35 minutes (healthy)

  HEALTH       local: OK    public: OK

  IMAGES       backend  sha256:64520bf6237a
               web      sha256:805b4edc4d12

  DATA       80 / 8082
    songs        64279  65383    diff: 1104 <- 8082 has more data
    artists      617    617      diff: 0

  GIT          HEAD  c1ee03ad ops(production): separate...
               repo  4 files dirty  -> type 'clean'

  RELEASES
    20260803T074303Z-32aacab8bb5e  32aacab8
    20260802T110741Z-32aacab8bb5e  32aacab8

  ──────────────────────────────────────────────────
  d)eploy   v)erify   b)rowser   r)ollback   rc)ecover
  promote   clean     l)ogs      i)nspect    q)uit

  → repo dirty — run clean | 8082 ahead by 1104 songs — run promote
```

## 命令

| 键 | 操作 |
|---|---|
| `d` | 部署 release |
| `v` | 校验 release |
| `b` | 提交浏览器验证证据 |
| `r` | 回滚代码/镜像（不恢复 DB） |
| `rc` | 恢复中断的部署 |
| `promote` | 8082 数据并入 80 |
| `clean` | 清理生产 repo 未提交修改 |
| `l` | 查看服务日志 |
| `i` | inspect |
| `q` | 退出 |

## 依赖

- `production_80.py`（datacenter-kimi 仓库的 release 模块）
- SSH BatchMode 到 `gandalf`
- PostgreSQL 容器 `datacenter-kimi-production-db-1` / `datacenter-kimi-db-1`
