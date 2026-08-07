# dp80 — :80 Deploy Panel

Jose 的 Gandalf 80 端口部署面板。Go + bubbletea TUI，源码即本目录。

## 用法

```bash
dp80          # 交互面板
dp80 status   # 只读状态（非交互）
dp80 version  # 完整版本号
```

alias `dp80` 定义在 `~/.my_script/grabalias.sh`（不在本项目里，全局 alias 统一放那），指向本目录编译出的 `dp80` 二进制。**高频命令**，搬迁本项目目录时务必同步改这个 alias。

当前候选版本是 `1.1.0a`。尾字母表示尚未由 GeojoLu 验收；验收通过后应发布为
`1.1.1`，不能只去掉字母复用 `1.1.0`。

## 命令

| 键 | 操作 |
|---|---|
| `d` | 一键部署：取最新已 `prepare` 的 release manifest，确认后本地跑 `production-80.sh apply --execute --allow-port80-downtime`（按键即授权，无额外闸） |
| `r` | 刷新面板（重新拉取容器状态/健康检查/release/DB 计数） |
| `m` | 生成 :8082 → :80 的不可变数据合并预览；只有预览无 blocker 后，按大写 `M` 执行该精确计划 |
| `c` | 开关 :80 采集策略（确认后执行） |
| `k` | 终止 :80 当前 running 采集任务；不改变采集策略 |
| `l` | 查看迁移审计历史 + 容器日志（backend，已过滤 livez 噪音） |
| `q` | 退出 |

`d` 读取 `/Users/geojol/Documents/Projects/datacenter-kimi/release-manifest/production-80/`
下最新的 release（ID 以 UTC 时间戳开头，字典序即时间序），确认屏显示 release id /
commit / 版本号。注意 apply 只能部署**已 prepare 的包**——8082 出了新东西要先
`production-80.sh prepare` 生成新 manifest，`d` 自动就会捡到最新的那个。

## 数据合并保护

`m` 只做预览：从 :8082 导出一次不可变快照，在 :80 侧生成精确计划，并显示完整计数、
源快照 SHA-256、计划 SHA-256、冲突和 blocker。预览不会写生产业务表或迁移审计。

只有无 blocker 的预览可以用大写 `M` 执行。执行前会取得并发锁、锁定相关业务表、
重新生成计划并逐行核对；源快照或生产目标发生漂移都会拒绝。框架、厂牌、艺人、歌曲和
variant 更新在同一个生产事务内提交或回滚。进度来自唯一远端作业的单条事件流，不轮询
数据库，也不伪造百分比。确认界面用 `Tab`/方向键分页展示完整摘要、64 字符 SHA-256 和
全部 blocker 分类，包括重复业务键、缺失 variant、self-link、variant cycle、日期错误和
sequence 配置错误；只有摘要页接受大写 `M`。取消会消费未使用的一次性预览令牌，旧请求
的迟到结果不会重新打开确认页。合并失败不会移除或遮蔽同一工具里的 `k` 任务终止入口。

部署进度同样来自单个长连接 ledger 事件流；本地 timer 只驱动 spinner，不查询远端业务状态。

## 面板内容

**顶部状态栏**（2 行）：
- Line 1：容器状态简写 `containers: backend● scheduler● proxy● db●`（绿●=running，红●=down）
- Line 2：health checks 和 images `livez: local 200 • public 200 • be:hash we:hash`

**左右两栏**：
- **RELEASES**（左）— 最近 5 个 release，每行格式 `● hash date (live)` 或 `○ hash date`；live commit 高亮绿色
- **DATABASE**（右）— :80 vs :8082 数据对比（songs/artists），diff 高亮；:8082 领先时提示按 `m` 合并

## 职责边界

Jose 只做 :80 部署。代码改动、镜像构建、8082 部署验收全归 ruby。
