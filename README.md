# overture
[![CI](https://github.com/xiaoran0503/overture/actions/workflows/ci.yml/badge.svg)](https://github.com/xiaoran0503/overture/actions/workflows/ci.yml)
[![GoDoc](https://godoc.org/github.com/shawn1m/overture?status.svg)](https://godoc.org/github.com/shawn1m/overture)
[![Go Report Card](https://goreportcard.com/badge/github.com/shawn1m/overture)](https://goreportcard.com/report/github.com/shawn1m/overture)
[![codecov](https://codecov.io/gh/shawn1m/overture/branch/master/graph/badge.svg)](https://codecov.io/gh/shawn1m/overture)

Overture is a customized DNS relay server.

Overture means the orchestral piece at the beginning of a classical music composition, just like DNS which is nearly the
first step of surfing the Internet.

## 2.0 维护公告

当前 `2.0.0` 版本由 Codex（AI）协助维护。维护工作遵循现有 MIT 许可证，保留原作者版权声明；AI 负责依赖更新、缺陷修复、测试与维护文档，发布前仍应由仓库维护者审核。

### 从 1.8.1 到 2.0.0 的变更

- 最低构建环境升级至 Go 1.27，并以 GCC 工具链完成 Linux 构建、竞态测试和服务冒烟测试。
- 升级 CoreDNS、miekg/dns、logrus、x/net 等直接依赖；Redis 客户端迁移至 `github.com/redis/go-redis/v9`，YAML 解析迁移至 `gopkg.in/yaml.v3`。
- 移除长期未维护的 `silenceper/pool`，改为内置有界 TCP/TLS `net.Conn` 连接池，保持既有 `tcpPoolConfig` 配置兼容。
- 修复并发备用 DNS 查询协程泄漏、未配置 ECS 时的空指针、JSON/文件热重载遗留派生状态，以及 Redis 和连接池资源未关闭的问题。
- 修复缓存调试接口竞态与 TTL 计算错误：缓存按最短可缓存记录 TTL 过期，命中响应按实际经过时间递减各记录 TTL。
- 加固 DoH、TLS 地址解析和公开 HTTP 控制服务的错误处理、超时、响应大小限制与凭据脱敏。
- 补充回归测试、GitHub Actions CI、迁移说明，并通过 `go vet`、乱序测试、竞态测试、静态检查和漏洞检查。

完整兼容性注意事项见 [MIGRATION.md](MIGRATION.md)。

**Please note:** 
- Read the **entire README first** is necessary if you want to use overture **safely** or **create an issue** for this project .
- **Production usage is not recommended and there is no guarantee or warranty of it.**
   
## Features

+ Multiple DNS upstream
    + Via UDP/TCP with custom port
    + Via SOCKS5 proxy (TCP only)
    + With EDNS Client Subnet (ECS) [RFC7871](https://tools.ietf.org/html/rfc7871)
+ Dispatcher
    + Custom domain
    + Custom IP network
    + IPv6 record (AAAA) redirection
+ Full IPv6 support
+ Minimum TTL modification
+ Hosts (Both IPv4 and IPv6 are supported and IPs will be returned in a random order. If you want to use regex match hosts, please understand how regex works first)
+ Cache with ECS and Redis(Persistence) support
+ DNS over HTTP server support

### Dispatch process

DNS queries with certain domain will be forced to use selected DNS when matched.

For the IP network dispatch, overture will send queries to primary DNS first. Then, If that answer is empty or not matched, the alternative DNS servers will be used instead.

## Installation

The binary releases are available in [releases](https://github.com/shawn1m/overture/releases).

Building from source requires Go 1.27 or newer. The Linux build is continuously verified with GCC.

## Usages

Start with the default config file `./config.yml`

**Only file having a `.json` suffix will be considered as json format for compatibility and that support is deprecated.**

    $ ./overture

Or use your own config file:

    $ ./overture -c /path/to/config.yml

Verbose mode:

    $ ./overture -v

Log to file:

    $ ./overture -l /path/to/overture.log

For other options, please check the helping menu:

    $ ./overture -h

Tips:

+ Root privilege might be required if you want to let overture listen on port 53 or one of other system ports.

###  Configuration Syntax

Configuration file is "config.yml" by default:

```yaml
bindAddress: :53
debugHTTPAddress: 127.0.0.1:5555
debugHTTPToken:
dohEnabled: false
primaryDNS:
  - name: DNSPod
    address: 119.29.29.29:53
    protocol: udp
    socks5Address:
    timeout: 6
    ednsClientSubnet:
      policy: disable
      externalIP:
      noCookie: true
alternativeDNS:
  - name: 114DNS
    address: 114.114.114.114:53
    protocol: udp
    socks5Address:
    timeout: 6
    ednsClientSubnet:
      policy: disable
      externalIP:
      noCookie: true
onlyPrimaryDNS: false
ipv6UseAlternativeDNS: false
alternativeDNSConcurrent: false
whenPrimaryDNSAnswerNoneUse: primaryDNS
ipNetworkFile:
  primary: ./ip_network_primary_sample
  alternative: ./ip_network_alternative_sample
domainFile:
  primary: ./domain_primary_sample
  alternative: ./domain_alternative_sample
  matcher: full-map
hostsFile:
  hostsFile: ./hosts_sample
  finder: full-map
minimumTTL: 0
domainTTLFile: ./domain_ttl_sample
cacheSize: 0
cacheRedisUrl: redis://localhost:6379/0
cacheRedisConnectionPoolSize: 10 
rejectQType:
  - 255
```

Tips:

+ bindAddress: Specifying any port (e.g. `:53`) will let overture listen on all available addresses (both IPv4 and
IPv6). Overture will handle both TCP and UDP requests. Literal IPv6 addresses are enclosed in square brackets (e.g. `[2001:4860:4860::8888]:53`)
+ debugHTTPAddress: Specifying an HTTP port for debug (**`5555` is the default port despite it is also acknowledged as the android Wi-Fi adb listener port**), currently used to dump DNS cache, and the request url is `/cache`, available query argument is `nobody`(boolean)
+ debugHTTPToken: Required when `debugHTTPAddress` is not a loopback address. Control and profiling endpoints require `Authorization: Bearer <token>`. Reload endpoints accept `POST` requests only. Keep the debug address on loopback when no token is configured.

    * true(default): only get the cache size;

        ```bash
        $ curl 127.0.0.1:5555/cache | jq
        {
          "length": 1,
          "capacity": 100,
          "body": {}
        }
        ```

    * false: get cache size along with cache detail.

        ```bash
        $ curl 127.0.0.1:5555/cache?nobody=false | jq
        {
          "length": 1,
          "capacity": 100,
          "body": {
            "www.baidu.com. 1": [
              {
                "name": "www.baidu.com.",
                "ttl": 1140,
                "type": "CNAME",
                "rdata": "www.a.shifen.com."
              },
              {
                "name": "www.a.shifen.com.",
                "ttl": 300,
                "type": "CNAME",
                "rdata": "www.wshifen.com."
              },
              {
                "name": "www.wshifen.com.",
                "ttl": 300,
                "type": "A",
                "rdata": "104.193.88.123"
              },
              {
                "name": "www.wshifen.com.",
                "ttl": 300,
                "type": "A",
                "rdata": "104.193.88.77"
              }
            ]
          }
        }
        ```
+ dohEnabled: Enable DNS over HTTP server using `DebugHTTPAddress` above with url path `/dns-query`. DNS over HTTPS server can be easily achieved helping by another web server software like caddy or nginx.
+ primaryDNS/alternativeDNS:
    + name: This field is only used for logging.
    + address: Same rule as BindAddress.
    + protocol: `tcp`, `udp`, `tcp-tls` or `https`
        + `tcp-tls`: Address format is "servername:port@serverAddress", try one.one.one.one:853 or one.one.one.one:853@1.1.1.1
        + `https`: Just try https://cloudflare-dns.com/dns-query
        +  Check [DNS Privacy Public Resolvers](https://dnsprivacy.org/wiki/display/DP/DNS+Privacy+Public+Resolvers) for more public `tcp-tls`, `https` resolvers.
    + socks5Address: Forward dns query to this SOCKS5 proxy, `“”` to disable.
    + ednsClientSubnet: Use this to improve DNS accuracy for many reasons. Please check [RFC7871](https://tools.ietf.org/html/rfc7871) for
    details.
        + policy
            + `auto`: If the client IP is not in the reserved IP network, use the client IP. Otherwise, use the external IP.
            + `manual`: Use the external IP if this field is not empty, otherwise use the client IP if it is not one of the reserved IPs.
            + `disable`: Disable this feature.
        + externalIP: If this field is empty, ECS will be disabled when the inbound IP is not an external IP.
        + noCookie: Disable cookie.
+ onlyPrimaryDNS: Disable dispatcher feature, use primary DNS only.
+ ipv6UseAlternativeDNS: For to redirect IPv6 DNS queries to alternative DNS servers.
+ alternativeDNSConcurrent: Query the primaryDNS and alternativeDNS at the same time.
+ whenPrimaryDNSAnswerNoneUse: If the response of primaryDNS exists and there is no `ANSWER SECTION` in it, the final chosen DNS upstream should be defined here. (There is no `AAAA` record for most domains right now) 
+ *File: Both relative like `./file` or absolute path like `/path/to/file` are supported. Especially, for Windows users, please use properly escaped path like
  `C:\\path\\to\\file.txt` in the configuration.
+ domainFile.Matcher: Matching policy and implementation, including "full-list", "full-map", "regex-list", "mix-list", "suffix-tree" and "final". Default value is "full-map".
+ hostsFile.Finder: Finder policy and implementation, including "full-map", "regex-list". Default value is "full-map".
+ domainTTLFile: Regex match only for now;
+ minimumTTL: Set the minimum TTL value (in seconds) in order to improve caching efficiency, use `0` to disable.
+ cacheSize: The number of query record to cache, use `0` to disable.
+ cacheRedisUrl, cacheRedisConnectionPoolSize: Use redis cache instead of local cache.
+ rejectQType: Reject query with specific DNS record types, check [List of DNS record types](https://en.wikipedia.org/wiki/List_of_DNS_record_types) for details.

## Migration notes

- This release raises the minimum source build version to Go 1.27.
- Redis caching now uses `github.com/redis/go-redis/v9`. Existing `redis://` and `rediss://` configuration URLs remain compatible.
- YAML parsing now uses `gopkg.in/yaml.v3`. Field names and normal YAML lists, anchors, aliases, and boolean fields remain compatible. Configurations that rely on YAML 1.1 implicit scalar coercion outside typed fields should quote those values before upgrading.
- TCP and TLS DNS connection pooling no longer depends on `silenceper/pool`; the built-in bounded `net.Conn` pool keeps the existing `tcpPoolConfig` settings unchanged.
- The `/config` endpoint redacts `debugHTTPToken` and Redis credentials. JSON reloads still accept partial configuration objects, but rebuild all runtime state before the service is restarted.

#### Domain file example (full match)

    example.com

#### Domain file example (regex match)

    ^xxx.xx
    
#### IP network file example (CIDR match)

    1.0.1.0/24
    ::1/128
    
#### Domain TTL file example (regex match)
 
     example.com$ 100

#### Hosts file example (full match)

    127.0.0.1 localhost
    ::1 localhost
    
#### Hosts file example (regex match)

    10.8.0.1 example.com$

#### DNS servers with ECS support

+ DNSPod 119.29.29.29:53

For DNSPod, ECS might only work via udp, you can test it by [patched dig](https://www.gsic.uva.es/~jnisigl/dig-edns-client-subnet.html) to certify this argument by comparing answers.
 
**The accuracy depends on the server side.**

```
$ dig @119.29.29.29 www.qq.com +client=119.29.29.29

; <<>> DiG 9.9.3 <<>> @119.29.29.29 www.qq.com +client=119.29.29.29
; (1 server found)
;; global options: +cmd
;; Got answer:
;; ->>HEADER<<- opcode: QUERY, status: NOERROR, id: 64995
;; flags: qr rd ra; QUERY: 1, ANSWER: 1, AUTHORITY: 0, ADDITIONAL: 1

;; OPT PSEUDOSECTION:
; EDNS: version: 0, flags:; udp: 4096
; CLIENT-SUBNET: 119.29.29.29/32/24
;; QUESTION SECTION:
;www.qq.com.            IN  A

;; ANSWER SECTION:
www.qq.com.     300 IN  A   101.226.103.106

;; Query time: 52 msec
;; SERVER: 119.29.29.29#53(119.29.29.29)
;; WHEN: Wed Mar 08 18:00:52 CST 2017
;; MSG SIZE  rcvd: 67
```

```
$ dig @119.29.29.29 www.qq.com +client=119.29.29.29 +tcp

; <<>> DiG 9.9.3 <<>> @119.29.29.29 www.qq.com +client=119.29.29.29 +tcp
; (1 server found)
;; global options: +cmd
;; Got answer:
;; ->>HEADER<<- opcode: QUERY, status: NOERROR, id: 58331
;; flags: qr rd ra; QUERY: 1, ANSWER: 3, AUTHORITY: 0, ADDITIONAL: 1

;; OPT PSEUDOSECTION:
; EDNS: version: 0, flags:; udp: 4096
;; QUESTION SECTION:
;www.qq.com.            IN  A

;; ANSWER SECTION:
www.qq.com.     43  IN  A   59.37.96.63
www.qq.com.     43  IN  A   14.17.32.211
www.qq.com.     43  IN  A   14.17.42.40

;; Query time: 81 msec
;; SERVER: 119.29.29.29#53(119.29.29.29)
;; WHEN: Wed Mar 08 18:01:32 CST 2017
;; MSG SIZE  rcvd: 87
```

## Acknowledgements

+ [dns](https://github.com/miekg/dns): BSD-3-Clause
+ [skydns](https://github.com/skynetservices/skydns): MIT
+ [go-dnsmasq](https://github.com/janeczku/go-dnsmasq):  MIT
+ [All Contributors](https://github.com/shawn1m/overture/graphs/contributors)

## License

This project is under the MIT license. See the [LICENSE](LICENSE) file for the full license text.
