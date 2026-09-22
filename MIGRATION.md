# Overture 升级技术交接文档：v1.8.1 → v2.0.9

> 本文档面向**正在使用 v1.8.1（上游 legacy 基线）的下游程序与集成方**，说明升级到当前修缮版本（v2.0.9）时的兼容性结论、行为变化与注意事项，避免升级翻车。
> 所有"变化"均有 v1.8.1 与 v2.0.9 **双成品实测对拍**依据（真实阿里 DNS 上游，UDP/TCP/DoT/DoH 四协议 × 19 用例 + 可编程假上游对照），非代码推断。

---

## 0. 一句话结论

**标准解析客户端可直接替换二进制 + 沿用旧配置升级**：DNS 应答语义（rcode、记录内容、顺序、路由选择、大小写回显）与 v1.8.1 实测一致，配置格式向后兼容；差异全部是协议正确性方向的改进，唯一需要主动评估的是 **NOTIFY 报文处理**（见 §4-C3）。

---

## 1. 配置兼容性

| 项 | 结论 |
|---|---|
| 旧配置直接使用 | ✅ v1.8.1 配置文件改端口后可直接用于 v2.0.9（实测通过） |
| 旧字段 | 全部保留：`bindAddress` / `debugHTTPAddress` / `dohEnabled` / `primaryDNS` / `alternativeDNS` / `onlyPrimaryDNS` / `ipv6UseAlternativeDNS` / `alternativeDNSConcurrent` / `whenPrimaryDNSAnswerNoneUse` / `ipNetworkFile` / `domainFile` / `hostsFile` / `minimumTTL` / `domainTTLFile` / `cacheSize` / `cacheRedisUrl` / `cacheRedisConnectionPoolSize` / `rejectQType` / `socks5Address` / `ednsClientSubnet.*` |
| 新增字段 | 仅 `debugHTTPToken`（可选，留空 = 旧行为）；无新增必填项 |
| 空路径行为 | 空的 `ipNetworkFile` / `domainFile` / `hostsFile` / `domainTTLFile` 按"功能未启用"静默处理（v1.8.1 会打 ERROR/WARN）——仅日志差异 |
| YAML | 已升级 yaml.v3；1.8.1 写法（含别名/锚点/布尔）兼容；YAML 1.1 风格歧义标量（如 `on`/`yes`）建议加引号 |

技术栈附带变化（与业务无关，见原 §）：Go 1.27 构建；Redis 缓存驱动 go-redis v9（`cacheRedisUrl` 写法不变）；TCP/TLS 连接池为自研有界实现（`tcpPoolConfig.*` 语义不变）。

---

## 2. 协议支持矩阵（实测）

| 协议 | v1.8.1 | v2.0.9 |
|---|---|---|
| DNS over UDP / TCP | ✅ | ✅ |
| DNS over TLS（`protocol: tcp-tls`） | ✅ | ✅ |
| DNS over HTTPS（`dohEnabled: true`，DoH 客户端/服务端） | ✅ | ✅ |
| **DNS over QUIC / DoH3（`protocol: quic`）** | ❌ 启动即拒 | ❌ 启动即拒 |

两版对 QUIC 一致：**均不支持，配置即拒绝启动**。如需 QUIC 属新功能立项，不在本升级范围。

---

## 3. 下游无感项（实测一致，可放心）

- 常规解析：A / AAAA / TXT / MX / NS / CNAME 链的 rcode、记录内容、顺序、TTL 传递
- 路由：domain 规则分流、IP 网络分流、`ipv6UseAlternativeDNS`、`whenPrimaryDNSAnswerNoneUse`
- `rejectQType`（如 ANY→SERVFAIL rcode 2）行为一致
- NXDOMAIN + 权威 SOA 段传递、NOERROR 空应答传递
- 查询/应答 question 段大小写回显
- DoT / DoH / TCP 下大应答（>512B）的完整传递结构
- hosts 文件覆盖、ECS 注入策略（manual / auto / disable）、cookie 处理

---

## 4. 行为变化清单

### C1. 改进方向（下游可见但更正确，**无需改动**）

| # | 变化 | v1.8.1 | v2.0.9 | 说明 |
|---|---|---|---|---|
| 1 | UDP 大应答（客户端无 EDNS0） | 上游按 512 截断：TC=1 + 部分记录，且 TCP 重试失效 | **TC=0 完整应答**（出站声明 EDNS0 4096 + TC 时自动 TCP 回退一次） | 下游少一次 TCP 往返；影响 DNSSEC/大 TXT/多 A 记录域 |
| 2 | >512B 入站查询 | FORMERR（rcode 1） | 正常解析（rcode 0） | 读缓冲提升至 65535 |
| 3 | 响应报文字节 | 未压缩（实时路径） | 启用名字压缩（与缓存路径一致，平均小 ~1.6×） | 语义不变；见 C3-2 |
| 4 | 缓存命中 TTL | 不老化 | 按驻留时长递减（跨秒） | 更真实的 TTL；亚秒内命中无感知 |
| 5 | 缓存大小写 | 大小写敏感（不同大小写 = 不同条目） | 大小写不敏感；命中时 question 段重写为本次查询原文 | 跨大小写查询少一次回源，输出仍为客户端原文 |
| 6 | DoH 非法请求 | 0-question 可 panic（仅请求级）、非查询 opcode 当普通查询 | 400 / 501（NOTIMP） | 纵深防御 + 协议正确 |
| 7 | reload 失败 | `log.Fatalf` 直接杀进程 | 监听失败返回错误、保留旧配置、不再退出进程 | 可用性自伤修复 |
| 8 | 防投毒 | 接受任意响应 ID | 拒绝 ID 不匹配的 UDP 响应 | 更严格；上游/链路异常时表现为丢弃乱序包 |
| 9 | 调试 HTTP | 无保护 | 非回环监听强制要求 token（`debugHTTPToken`） | 安全加固 |
| 10 | 无效正则规则 | 查询时 panic 可致进程退出 | 加载期校验并跳过（Warn） | 配置失误不再打崩服务 |
| 11 | 空可选配置日志 | ERROR/WARN 噪音 | 静默（按未启用） | 日志可观测性 |

### C2. 上游可感知（下游一般无感）

- **出站查询总是携带 EDNS0 OPT（bufsize 4096）**：即使客户端无 EDNS0、ECS 为 disable。自建/第三方上游会看到查询带 OPT（RFC 6891 标准行为）；依赖"无 OPT 即 512 截断"的上游策略将不再触发截断。

### C3. 需下游主动评估（潜在翻车点）

| # | 注意点 | 影响与处置 |
|---|---|---|
| 1 | **NOTIFY 及其它非查询 opcode → NOTIMP**（原 v1.8.1 会当普通查询回源） | 若下游是**从 DNS 服务器/主从通知**场景向 overture 发 NOTIFY：行为从"转发"变"拒绝"。处置：确认下游是否发送 NOTIFY；普通解析器/客户端不受影响 |
| 2 | **响应报文字节变化**（压缩、缓存命中带 OPT） | 若下游按字节硬比对（DPI/抓包比对/测试断言），需更新基线；所有标准 DNS 解析器均兼容 |
| 3 | 缓存命中响应**可能带 OPT 记录**（客户端查询无 OPT 时也会回带 OPT） | 合法（RFC 6891）；极严格客户端如拒绝无查询 OPT 的响应，需评估 |
| 4 | **日志格式/措辞变化**（如"No answer which will be discarded"改为中性措辞；空配置不再打 ERROR） | 若下游有日志关键字告警规则，需同步更新 |
| 5 | 多轮修复引入的**行为修正**（分流规则不再顺序短路、缓存 TTL 下限覆盖权威段、reload 串行化等） | 属于缺陷修复；仅影响依赖旧缺陷行为的场景（如依赖"规则顺序导致误路由"的测试） |

---

## 5. 升级检查清单（部署前）

1. **备份**：旧二进制 + 旧配置。
2. **配置自检**：用旧配置启动 v2.0.9，观察日志（重点：无 ERROR、监听成功、matcher/finder 正常）。
3. **灰度**：先切 1 台/1 个分片，用 `dig` 验证：
   - `dig @<新实例> example.com A` 与旧实例输出一致；
   - `dig @<新实例> www.google.com TXT`（大应答，验证 TC=0 完整返回）；
   - 同查询两次验证缓存命中（第二次 TTL 略小）；
   - `dig @<新实例> ANY example.com`（确认 SERVFAIL 与旧版一致）；
   - DoH：`curl -H 'Content-Type: application/dns-message' --data-binary @<query> http://<host>/dns-query` 返回 200。
4. **协议核对**：确认上游配置中 `protocol` 只使用 `udp / tcp / tcp-tls / https`（QUIC 不支持）。
5. **日志告警**：同步更新日志关键字规则（§4-C3-4）。
6. **NOTIFY 评估**：确认无主从 NOTIFY 依赖（§4-C3-1）。
7. **回滚**：直接换回 v1.8.1 二进制 + 旧配置即可（双向兼容，无数据迁移）。

---

## 6. 实测验证依据（可复现）

- 双成品：v1.8.1（v1.8.1-legacy，go1.20.14 工具链编译）、v2.0.9（Go 1.27）
- 真实上游：阿里 DNS（UDP/TCP `223.5.5.5`、DoT `dns.alidns.com@223.5.5.5`、DoH `https://dns.alidns.com/dns-query`）
- 矩阵：四协议 × 19 用例（真实域名 + 可编程假上游截断/多记录/NXDOMAIN/分流/ECS）
- 对拍数据与成品保留于：`E:\缓存\ClearDNS\.cmp_bins\`、`E:\缓存\ClearDNS\.cmp_data\`（本机复验用）
- 结论：除 §4 所列差异外，v2.0.9 与 v1.8.1 对下游输出传递一致，**未发现意外回归**。

---

## 附：版本历史速览（本 fork 维护线）

v2.0.1 接管修复 → v2.0.2 大小写不敏感/README → v2.0.3 九项修复（正则 DoS、分流短路、reload 杀进程、TTL 下限等）→ v2.0.4 reload 回滚/缓存 nil 防御 → v2.0.5 十二项修复 → v2.0.6 配置热更新深拷贝 → v2.0.7 构建健壮性 → v2.0.8 截断不入缓存/NOTIMP → v2.0.9 EDNS0 声明/TCP 回退/读缓冲/压缩。
每版变更明细见 `CHANGELOG.md`。
