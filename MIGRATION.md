# Overture 升级技术交接文档：v1.8.1 → v2.0.9

> 本文档面向**正在使用 v1.8.1（上游 legacy 基线）的下游程序与集成方**，说明升级到本 fork 修缮线时的兼容性结论、行为变化与注意事项，避免升级翻车。
> v1.8.1 → v2.0.9 的对拍结论仍然有效（真实阿里 DNS 上游，UDP/TCP/DoT/DoH 四协议 × 19 用例）。**当前发布版本是 v2.5.0**；v2.0.9 之后均为纯增量（DoQ/DoH3、可观测、健康探测、ECS 按域、DoH3 服务端），默认配置行为不变。

---

## 0. 一句话结论

**标准解析客户端可直接替换二进制 + 沿用旧配置升级**：DNS 应答语义（rcode、记录内容、顺序、路由选择、大小写回显）与 v1.8.1 实测一致，配置格式向后兼容；差异全部是协议正确性方向的改进，唯一需要主动评估的是 **NOTIFY 报文处理**（见 §4-C3）。

---

## 1. 配置兼容性

| 项 | 结论 |
|---|---|
| 旧配置直接使用 | ✅ v1.8.1 配置文件改端口后可直接用于 v2.0.9（实测通过） |
| 旧字段 | 全部保留：`bindAddress` / `debugHTTPAddress` / `dohEnabled` / `primaryDNS` / `alternativeDNS` / `onlyPrimaryDNS` / `ipv6UseAlternativeDNS` / `alternativeDNSConcurrent` / `whenPrimaryDNSAnswerNoneUse` / `ipNetworkFile` / `domainFile` / `hostsFile` / `minimumTTL` / `domainTTLFile` / `cacheSize` / `cacheRedisUrl` / `cacheRedisConnectionPoolSize` / `rejectQType` / `socks5Address` / `ednsClientSubnet.*` |
| 新增字段 | v2.0.9 仅 `debugHTTPToken`（可选）；其后均为可选增量：`upstreamHealthCheck`（v2.4.0）、`domainECSFile` / `upstreamFailover` / `doh3`（v2.5.0）。无新增必填项，不配则行为与 v1.8.1 一致 |
| 空路径行为 | 空的 `ipNetworkFile` / `domainFile` / `hostsFile` / `domainTTLFile` 按"功能未启用"静默处理（v1.8.1 会打 ERROR/WARN）——仅日志差异 |
| YAML | 已升级 yaml.v3；1.8.1 写法（含别名/锚点/布尔）兼容；YAML 1.1 风格歧义标量（如 `on`/`yes`）建议加引号 |

技术栈附带变化（与业务无关，见原 §）：Go 1.27 构建；Redis 缓存驱动 go-redis v9（`cacheRedisUrl` 写法不变）；TCP/TLS 连接池为自研有界实现（`tcpPoolConfig.*` 语义不变）。

---

## 2. 协议支持矩阵（实测）

| 协议 | v1.8.1 | v2.0.9 | v2.5.0 |
|---|---|---|---|
| DNS over UDP / TCP | ✅ | ✅ | ✅ |
| DNS over TLS（`protocol: tcp-tls`） | ✅ | ✅ | ✅ |
| DNS over HTTPS（`dohEnabled: true`，明文 DoH 服务端 + `protocol: https` 客户端） | ✅ | ✅ | ✅ |
| DNS over QUIC 客户端（`protocol: doq`） | ❌ 启动即拒 | ❌ 启动即拒 | ✅ v2.3.0 |
| DNS over HTTP/3 客户端（`protocol: https3`） | ❌ 启动即拒 | ❌ 启动即拒 | ✅ v2.3.1 |
| DNS over HTTP/3 服务端（`doh3.enable`） | ❌ | ❌ | ✅ v2.5.0（需 TLS 证书） |

v1.8.1 / v2.0.9 对 QUIC 一致：均不支持、配置即拒绝启动。**v2.3.0 起新增 DoQ 客户端**（RFC 9250，实测 `dns.quad9.net:853`）；**v2.3.1 起新增 DoH3 客户端**（实测阿里 `https://dns.alidns.com/dns-query` h3）；**v2.5.0 起新增 DoH3 服务端**（`doh3.enable` + `certFile`/`keyFile`，独立 h3 监听）。DoQ/https3 地址格式见 README。

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
| 12 | 并发同 key 缓存未命中（v2.2.1） | 每个并发请求各自回源（放大） | singleflight 合并为一次上游查询 | 热点域名突发并发回源量 N→1；各调用者独立副本，应答语义不变 |
| 13 | 新增 doq 上游协议（v2.3.0） | 无此能力 | `protocol: doq` 可配置 DoQ 上游 | 可选新能力：旧配置完全兼容、默认行为不变；仅当你把上游协议改为 doq 才生效 |
| 14 | 新增 https3 上游协议（v2.3.1） | 无此能力 | `protocol: https3` 可配置 DoH over HTTP/3 上游 | 可选新能力：旧配置完全兼容、默认行为不变；仅当你把上游协议改为 https3 才生效 |
| 15 | DoH 服务端 GET / 缓存头 / 方法码（v2.3.2） | POST 为主；Cache-Control 为 `max-age=%f` 浮点；非法方法 400 | GET+POST；整数 `max-age`；错误应答 `no-store`；非 GET/POST 为 405；POST 缺 Content-Type 为 415 | RFC 8484 对齐；仅影响走 `/dns-query` 的 HTTP 客户端，标准 DNS 解析无感 |
| 16 | UDP/TCP 空 question（v2.3.2） | `Question[0]` panic | FORMERR | 畸形报文不再打崩进程 |
| 17 | 上游健康探测（v2.4.0） | 无此能力；每次都问选中组内全部上游 | 可选 `upstreamHealthCheck.enable`；默认关闭，行为与 v1.8.1 一致 | 打开后才摘除/恢复；全组 down 时 fail-open |
| 18 | ECS 按域策略（v2.5.0） | 仅按上游 `ednsClientSubnet` | 可选 `domainECSFile` 覆盖 | 默认不配文件则行为不变 |
| 19 | 组内 failover（v2.5.0） | 组内始终并发竞速 | 可选 `upstreamFailover: sequential` | 默认 concurrent = 旧行为 |
| 20 | DoH3 服务端（v2.5.0） | 无 | 可选 `doh3.enable` + 证书 | 默认关闭；明文 DoH 不变 |

### C2. 上游可感知（下游一般无感）

- **出站查询总是携带 EDNS0 OPT（bufsize 4096）**：即使客户端无 EDNS0、ECS 为 disable。自建/第三方上游会看到查询带 OPT（RFC 6891 标准行为）；依赖"无 OPT 即 512 截断"的上游策略将不再触发截断。
- **debug HTTP 新增 `/healthz`（存活探针）与 `/metrics`（Prometheus）**（v2.2.0）：纯增量端点，不影响既有 `/cache`、`/reload` 等路径；与其它 debug 路径一样受 `debugHTTPToken` 保护（非回环强制 token，回环且未配置 token 时本地开放）。监控接入方如直接暴露 debug 端口，需为这两个端点配置鉴权。
- **DoH `/dns-query` Cache-Control 格式**（v2.3.2）：由浮点 `max-age=300.000000` 改为整数 `max-age=300`；SERVFAIL 等改为 `no-store`。仅 HTTP 中间缓存 / DoH 客户端可见，DNS 载荷不变。
- **Prometheus `overture_upstream_up`**（v2.4.0）：仅在开启 `upstreamHealthCheck` 后有序列；默认关闭时不出现。
- **Prometheus `overture_doh3_requests_total`**（v2.5.0）：仅 DoH3 监听收到的查询；未开启 `doh3.enable` 时无序列。

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
4. **协议核对**：旧配置继续用 `udp / tcp / tcp-tls / https` 即可。新能力按需启用：`doq`（v2.3.0+）、`https3`（v2.3.1+）、`doh3.enable`（v2.5.0+，需证书）。
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

v2.0.1 接管修复 → … → v2.0.9 EDNS0/TCP 回退/压缩 → v2.1.x 质量地基 → v2.2.x 可观测与性能 → v2.3.0 DoQ 客户端 → v2.3.1 DoH3 客户端 → v2.3.2 DoH 服务端 RFC 8484 → v2.4.0 上游健康探测 → **v2.5.0 ECS 按域 / sequential failover / DoH3 服务端 / 依赖审查**。
每版变更明细见 `CHANGELOG.md`。
