# Overture Fork 维护路线（v2.0.9 之后）

> 本文件是 fork（github.com/xiaoran0503/overture）的权威维护路线，随决策更新。变更明细见 `CHANGELOG.md`，下游升级影响见 `MIGRATION.md`。

## 一、现状基线

- **已完成**：v2.0.1 → **v2.6.0**。阶段一质量地基、阶段二可观测/性能、阶段三 QUIC/DoH3 与运维增量均已收官。
- **质量基线**：gofmt / vet / shuffle / race / staticcheck / govulncheck / build 全绿；双版本（v1.8.1 vs 当前版）四协议 × 19 用例对拍无回归。
- **交接**：MIGRATION.md（下游升级指南）已入库。
- **CI**：覆盖率门禁、多平台构建、artifact 上传、tag 触发 GitHub Release 均已落地（v2.1.x）。

## 二、维护决策（已确认）

| # | 决策点 | 结论 |
|---|---|---|
| ① | QUIC/DoH3 | **完成**（v2.3.0 DoQ 客户端 → v2.3.1 DoH3 客户端 → v2.5.0 DoH3 服务端） |
| ② | 覆盖率门禁 | 分阶段按包差异化：v2.1 总 ≥55% + 关键安全包 ≥70%（matcher/mix、cache、inbound/server、common）；v2.2 后总 ≥60% + 关键包 ≥75%；**不引入 golangci-lint** |
| ③ | 跟随上游 | **废止**（2026-09 确认：上游仓库 `shawn1m/overture` 已 404 不可访问，来源不复存在）。安全修复改由依赖生态承担：CI `govulncheck` 例行化（已在）+ 依赖升级审查（quic-go / miekg/dns / go-redis 等安全版本跟进），自身代码缺陷靠本仓库持续审查闭环 |
| ④ | 发布节奏 | 混合：安全/正确性修复立即发 patch；功能/优化攒批发 minor（阶段目标达成即发）；tag 触发 CI 构建 + GitHub Release 自动生成；单线 master + tag |

## 三、维护路线

### 阶段一：质量地基（v2.1.x）
1. 补测试缺口：`outbound/clients`、`main` 关键路径；新增 fuzz（DNS 报文 / config / matcher 解析）
2. 清理低风险 backlog：死代码（cache.Search*、core.Reload()）、NewResolver Fatalf 死路、SetTTLByMap 复杂度与覆盖段、hosts TTL 文档
3. CI 补强：覆盖率门禁（见决策②；v2.1 为总 >=55% / 关键包 >=70%，v2.3.2 起按 v2.2 决策上调为总 >=60% / 关键包 >=75%）、多平台构建 + artifact 上传、release workflow（tag 触发）
4. 配置 schema 版本化：`configVersion` + 迁移钩子
5. ~~上游追踪例行化~~ ⏭️（上游仓库已失效，废止，见决策③）

### 阶段二：性能与可观测（v2.2.x）
1. 性能：Redis 缓存查询放大（dispatcher 按 key 去重）✅（v2.2.1 singleflight）、LRU 逐出 ✅（v2.2.0 regex 缓存 LRU）、regex 缓存上限 ✅（v2.2.0）、TTL 映射索引化 ⏭️（评估后跳过：规则量小、LRU 已摊销编译，索引化需贯穿 config→dispatcher→clients 类型链，收益可忽略且易破坏正则语义）
2. 可观测性：结构化日志 ✅（v2.2.2 -j JSON）、Prometheus /metrics ✅（v2.2.0）、/healthz ✅（v2.2.0）
3. 运维：优雅关闭完善 ✅（SIGTERM/SIGINT 已有）、/reload 鉴权 ✅（已有 token）、审计日志 ✅（v2.2.1）
4. **阶段二（v2.2.x）收官**：进入阶段三（QUIC/DoH3 立项）
5. **阶段三（v2.3.x）**：QUIC/DoH3 —— DoQ 客户端 ✅（v2.3.0，RFC 9250，实测 Quad9 真实解析）、DoH3 客户端 ✅（v2.3.1，`protocol: https3`，实测阿里 DoH h3 真实解析）、DoH3 服务端 ✅（v2.5.0：`doh3.enable` + TLS 证书，独立 h3 监听）

### 阶段三：功能演进（v2.3.x+）
1. **QUIC/DoH3** ✅：DoQ 客户端（v2.3.0）→ DoH3 客户端（v2.3.1）→ DoH3 服务端（v2.5.0）
2. DoH 服务端完整化（GET 模式、缓存控制头细化）✅（v2.3.2：RFC 8484 GET/POST、整数 max-age / 错误 no-store、405/415；UDP/TCP 空 question 改 FORMERR）
3. 上游健康探测与自动摘除/恢复 ✅（v2.4.0：opt-in `upstreamHealthCheck`，被动+主动探测，fail-open；Quad9 DoQ 对照实测）
4. ECS 按域策略；上游故障转移语义可配置 ✅（v2.5.0：`domainECSFile` 最长后缀覆盖；`upstreamFailover: concurrent|sequential`）
5. 依赖安全例行化（替代上游跟随）：CI govulncheck 持续开启（已在）+ 每季度依赖升级审查 ✅（v2.5.0：2026-09 审查无可达漏洞，`scripts/dep-audit.sh`；后续按季跑该脚本）

### 阶段三收官后（例行维护）
- 不主动开新功能阶段，除非有明确下游需求或安全驱动
- 每季度跑 `scripts/dep-audit.sh`（govulncheck + 直接依赖版本审查）；安全修复立即发 patch，功能/优化攒批发 minor
- 分流决策缓存 ✅（v2.6.0：可选 `routeCache`，默认关闭）

## 四、维护工作流（每轮固化）

**输入 → 判定 → 修复 → 验证 → 发布 → 文档**
1. 输入：审查报告 / 用户指令 / backlog 取项（P0 正确性 > P1 性能 > P2 体验）+ 依赖安全扫描（govulncheck）
2. 判定：真实缺陷 + 低风险 + 有回归测试 = 修；纯优化/行为变更/高风险重构 = 跳过并书面附理由
3. 修复：代码 + 回归测试 + CHANGELOG + MIGRATION（影响下游输出时）
4. 验证：编译统一走 GitHub Actions；本地 WSL 不编译，仅用 CI artifact 做部署冒烟 + 双版本输出对拍（复用 `.cmp_bins` / `.cmp_data` 基线）；影响输出传递的改动必须跑双版本对拍
5. 发布：按决策④节奏；bump → push master + tag → CI 构建 → GitHub Release
6. 文档：README 维护公告 + MIGRATION 变更标注（C1/C2/C3 分级）

## 五、治理机制

- **版本策略**：语义化。行为变更 → minor；bugfix → patch；影响下游输出 → MIGRATION 必须标注
- **Backlog**：低风险项在本路线 §三-阶段一；性能项在阶段二；功能候选在阶段三。从 P0 取项，完成后移入 CHANGELOG
- **风险登记**（持续维护）：
  - DoQ / DoH3 客户端与 DoH3 服务端已落地；doq/https3 上游仍不支持 SOCKS5（直连）
  - NOTIFY → NOTIMP（主从场景）
  - WSL 回环 UDP 1472B 上限（对拍实验须规避）
  - 缓存命中响应带 OPT（极严格客户端）
- **兼容性红线**：不破坏 v1.8.1 配置直用；不影响标准解析输出；行为变更必进 MIGRATION
