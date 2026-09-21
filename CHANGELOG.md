# Changelog

## v2.0.1 (2026-09-21)

Bug fixes and hardening on top of v2.0.0:

- **EDNS cookie removal**: rewrite `deleteCookie` so removing a COOKIE option while iterating the OPT options can no longer skip or corrupt neighbouring options.
- **IPv6 reserved ranges**: `ReservedIPNetworkList` now also covers `::1/128`, `::/128`, `fc00::/7` and `fe80::/10`. With the `ednsClientSubnet.policy: auto`, loopback and link-local IPv6 clients are no longer forwarded as EDNS Client Subnet sources.
- **Trailing dot normalization**: domain dispatch lists, domain TTL entries and hosts-file hostnames are normalized by stripping the trailing dot, so entries such as `example.com.` keep matching queries for `example.com` (full-map and suffix-tree alike).
- **CRLF compatibility**: IP network files with Windows line endings are parsed correctly.
- **Config validation**: `Build` now rejects a configuration without any `primaryDNS` upstream instead of silently answering SERVFAIL for every query.
- **Reload safety**: `POST /reload` and `POST /reload/config` verify that new `bindAddress` / `debugHTTPAddress` values can actually be bound before swapping listeners, so an occupied port can no longer kill the running server. Unchanged addresses are skipped to avoid false rejects.
- **Performance**: domain regex patterns used for TTL overrides and hosts lookup are compiled once and cached instead of recompiled on every record.
- **Diagnostics**: `/cache?nobody=false` now returns the complete rdata for multi-token records (e.g. TXT), instead of truncating after the first token.
- **Build script**: `build.py` no longer overwrites an existing local `config.yml`.

## v2.0.0 (2026-09-21)

The 2.0.0 release was maintained with AI assistance (Codex). See the README "2.0 维护公告" section for the full change list from 1.8.1 to 2.0.0:

- Minimum build environment upgraded to Go 1.27, with GCC-based Linux builds, race tests and service smoke tests.
- Upgraded CoreDNS, miekg/dns, logrus, x/net; Redis client migrated to `github.com/redis/go-redis/v9`; YAML parsing migrated to `gopkg.in/yaml.v3`.
- Removed the unmaintained `silenceper/pool` in favor of a built-in bounded TCP/TLS `net.Conn` pool.
- Fixed concurrent alternative-DNS goroutine leaks, a nil pointer when ECS was not configured, stale derived state on JSON/file hot reload, and unclosed Redis/pool resources.
- Fixed the cache debug endpoint race and TTL accounting; cache entries expire by the shortest record TTL and hits decrement per-record TTLs by elapsed time.
- Hardened DoH, TLS address parsing and the public HTTP control service (errors, timeouts, response size limits, credential redaction).
- Added regression tests, GitHub Actions CI, migration notes; passes `go vet`, shuffled tests, race tests, staticcheck and govulncheck.
