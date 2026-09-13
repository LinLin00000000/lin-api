# 模型路由：阶段 A 运行语义与兼容边界

本页记录已实现的源码候选，不表示已发布。身份/服务定价及权限的新模式不属于阶段 A，也不因本页启用。项目一般发布流程不能覆盖本次明确限制：未授权 commit、push、合入、DEV/生产部署、重启或数据迁移。

## 排序与候选

- `abilities(group, model, channel_id).priority` 是排序真源；`channels.priority` 仅是**新增路由默认优先级**。普通保存、同步、tag、copy、batch 和修复不覆盖已有三元组的人工排序；复制保留精确共有三元组排序，删除后重加按新关联初始化。
- 管理 key 使用精确组/物理模型名，不 trim 或借规范化创建关联。nil priority 按 0，支持负数。enabled、weight、tag 仍随渠道同步，没有新增模型级启停/权重产品规则。
- 同渠道写入共用事务锁并重读；支持行锁的数据库使用行锁，多渠道按 id 升序；SQLite 使用写事务和忙重试。持久化失败整体回滚，修复不清表。
- DB/cache 均过滤启用渠道及 Ability，沿实际命中的模型 key 取 priority 和 retry 档位。已有同档权重算法差异保留，不保证分布完全相同，也不保证同档穷尽才降档。降级不隐式换模型或跨实际服务组；原有显式 auto 选组语义不扩展。
- 快照完整构建后一次发布。读取失败保留旧快照内容但不再用于选路，转当前 DB 路径；DB 不可用则拒绝，而非使用陈旧候选。首次无快照失败也不冒充健康。后续成功完整刷新恢复缓存。

## 管理 API 与旧客户端

`GET /api/channel/model_priority?group=...&model=...` 和 `PUT /api/channel/model_priority` 使用 ChannelRead/ChannelWrite 权限。PUT 只接受精确 `group`、`model`、`channel_id`、`priority` 四字段，限定已有且声明有效的三元组；未知关联返回 400，不修改渠道默认优先级。停用渠道在管理视图可见。

**完整编辑旧客户端必须先 GET `/api/channel/:id` 取得 revision，再将同一次编辑快照的 revision 随 PUT `/api/channel/` 提交。** 缺少 revision 为 400，陈旧 revision 为 409。冲突后保留输入并由用户刷新、核对再提交；不可后台取新 revision 给旧 payload 自动续写。新建不需要编辑 revision；动作型接口不应被误改为完整编辑请求。

渠道编辑的异步风险/缺失模型确认绑定同一 payload、目标和 revision。详情变化、关闭、切换目标或卸载会取消旧确认，不能把旧输入配新 revision 提交。排序视图按组/模型隔离，保存后 GET 回读；主列表不再用渠道 Priority 转轮代表实际排序。

## 已提交与缓存降级

管理响应区分持久化结果和刷新结果：`committed=true, degraded=true` 表示数据库已提交、缓存刷新失败，当前使用 DB 选路；这不是可以重发原写入的普通失败。检查响应 `success`、`committed`、`degraded`、`partial` 和逐渠道 outcomes，不能仅凭 HTTP 200 判定全部成功。修复刷新原因后仅重试刷新，不盲目重发新增、删除或已提交批次。

上游模型更新系统任务保留逐渠道结果。扫描/部分渠道失败使用 failed 终态；已提交但只刷新失败仍保留成功及 degraded 事实。历史界面以安全汇总展示，不输出原始供应商错误或秘密；刷新历史按钮只 GET，不重发写入。

## 验证与上线门

回归入口包括普通 `go test ./model ./service ./controller ./router -count=1`、路由专项 race、真实权限中间件/HTTP/临时 SQLite 测试及渠道/任务历史组件测试。PostgreSQL 使用独立临时实例验证事务、锁、回滚、游标合并及刷新失败回退；这不是全部 HTTP PostgreSQL 端到端覆盖。具体候选与结果见阶段 A 验收报告。整体 race 尚未闭合，专项通过不等于整体绿色。

没有新增 schema 不代表没有数据语义迁移。上线前仍需备份与恢复证明、现存 Ability 排序/成员漂移检查、旧客户端升级、目标数据库验证、构建产物及前后端共同验收和单独发布授权。只承诺本实例管理回读确认后的生效；多实例必须另验失效/轮询时限。在途请求不承诺追溯取消。回滚前保护人工 Ability 排序，旧程序可能按 channel default 重建；不能只换旧镜像。
