# Changelog

## v2.0.8 (2026-09-22)

Sixth review round (independent full verification; 1 defect + 3 polish, 4 assessed and skipped):

- **Truncated (TC=1) responses are no longer cached**: a truncated response carries only partial answers; caching it and replaying it with TC cleared made clients believe the answer was complete and never retry over TCP. `InsertMessage` now refuses truncated messages, so the next query goes back to the upstream. (End-to-end reproduced by the reviewer with a fake upstream; regression test added.)
- **NOTIFY / non-query opcodes are answered with NOTIMP** on both the DNS and DoH paths instead of being processed as ordinary queries.
- **Empty optional config no longer logs scary errors**: empty IP-network paths, empty domain matcher names and empty finder names are treated as "feature not enabled" instead of "configuration broken".
- Neutral wording for the no-answer-section debug log (an NXDOMAIN or empty answer is normal, not a discarded failure).

Skipped with rationale: Redis cache fan-out dedup (performance, Redis-mode only, caching-semantics risk; backlog), /cache default body inversion (breaking behavior change), hosts TTL documentation detail, CI coverage gating (maintainer decision).

## v2.0.7 (2026-09-22)

Fifth review follow-ups (non-blocking):

- Regression test hardening: `TestApplyJSONOverlayDoesNotMutateCurrent` now also asserts the overlay is actually applied (forward assertions on `primaryDNS` and `rejectQType`), closing the false-negative gap where a regression dropping the overlay entirely would still pass.
- `build.py`: fail fast with a clear message when the `zip` CLI is missing, instead of misreporting every architecture as a compile failure.

Skipped: switching build commands to list-arg subprocess calls (risk negligible; Git tag naming rules exclude shell metacharacters).

## v2.0.6 (2026-09-22)

Fourth code-review round (1 new severe defect + follow-ups):

- **ApplyJSON no longer mutates the running configuration**: a shallow copy shared the backing arrays of `primaryDNS`/`alternativeDNS`/`rejectQType`, and `encoding/json` decodes array elements in place, so `POST /reload/config` could overwrite the live config (data race with DNS request goroutines, and a rejected reload still left the config partially changed). The slice fields are now deep-copied element-by-element (including the `EDNSClientSubnet` pointer) before decoding; a concurrency regression test exercises readers against repeated overlays under `-race`.
- **build.py**: `git describe` version is stripped of the trailing newline, uses `--tags --always` so tag-less checkouts do not abort the build, and is resolved inside the try/except; `cp config.sample.yml` replaced with `shutil.copyfile` (portable).
- **/config endpoint** now requires GET (matches its read-only semantics).
- Removed the unreachable `shutdown()` call in `main_test.go` (dead code after `os.Exit`).

Skipped with rationale: CI coverage gating (maintainer decision; informational codecov config is harmless), build.py zip/POSIX overhaul (actual builds run in WSL), dispatcher_test `os.Chdir` refactor (legacy integration-test design; low value vs. churn).

## v2.0.5 (2026-09-22)

Third code-review findings (3 severe + 8 should-fix; 12 fixed, 7 assessed and skipped):

- **DoH zero-question guard**: a POST with QDCOUNT=0 used to panic on `q.Question[0]` (connection-level DoS noise); now rejected with 400.
- **minimumTTL validation**: a negative value silently became `uint32(-1)` = ~136 years of cached TTL; `Build` now rejects it.
- **No more `log.Fatal` on the request path**: `exchangeByConnWithoutClose` returns an error for a nil conn instead of `os.Exit(1)`.
- **UDP response ID validation**: responses whose transaction ID does not match the query are rejected (spoof/cross-talk defense).
- **mix-list regex rules with colons**: `Split` was cutting `regex:^https?://` into pieces; now uses `SplitN(str, ":", 2)`.
- **hosts invalid IP**: a bad IP line returned `"<nil>"` into the finder; now rejected at load time with a clear warning.
- **DoH Pack error handling**: a failed pack now returns 500 instead of a 200 empty body.
- **X-Forwarded-For chains**: only the leftmost address is used (multi-proxy format) and parsed once.
- **Copy-paste resolver log residue**: TCP/TLS pool init failures now log the real error instead of success info.
- **HTTP status constant**: control reload error uses `http.StatusInternalServerError`.
- **IP round-trip removal**: IP-network matching uses the parsed IP directly.
- **Config suffix check**: `".json"` instead of a bare `"json"` suffix.

Skipped with rationale (no functional regression): connection-pool Release/Close race (report vs code mismatch: the release is already inside the mutex), SetTTLByMap Answer-only behavior (intended), cache LRU eviction (evolution), dead exported API cleanup (compat), NewResolver default Fatalf (unreachable after Build validation), HTTPS URL build-time validation (format variance), UDP TC fallback (behavior change out of scope).

## v2.0.4 (2026-09-22)

Follow-up review fixes (v2.0.3 audit):

- **Reload rollback**: if a reloaded listener fails to bind after the pre-check passed (TOCTOU window), the server now rolls back to the last known-good configuration and keeps serving on the original address instead of going silent with old listeners closed and no new ones up. The initial start (no rollback target) and a failed rollback terminate via `log.Fatalf` so a supervisor can restart the service.
- **Listener teardown on partial startup failure**: when one listener fails to bind, the successfully bound siblings (e.g. a debug HTTP server) are shut down too, so `Run` returns promptly and the caller can react.
- **Cache nil-entry defense**: `Hit` and the local lookup drop corrupt entries with a nil message instead of panicking (protects against incompatible Redis payloads).

## v2.0.3 (2026-09-22)

Security and robustness review fixes (audited against the fork diff):

- **No remote crash on a broken regex rule (Critical)**: `mix-list` now validates `regex:` rules at load time and stores the compiled pattern, so a single malformed rule (e.g. `regex:foo(`) can no longer panic the query path and take the whole process down. `config` reports and skips invalid rules instead of swallowing the error.
- **Rule ordering no longer silently drops rules (High)**: when a `domain` rule in `mix-list` is a mid-label suffix of the query (`notexample.com` vs `example.com`), the scan continues to the remaining rules instead of returning early, so later `keyword`/`regex`/`full` rules are always evaluated.
- **Reload hardening**: `Stop` now waits for the listeners to fully stop before closing the dispatcher (removes a race where a concurrent reload could close resources in use); a listener bind failure returns an error instead of `log.Fatalf`, and a graceful shutdown (`ErrServerClosed`) is no longer misreported as a fatal error.
- **`minimumTTL` honored across all sections**: TTLs are raised on Answer/Ns/Extra (OPT skipped), so a low-TTL SOA in the authority section can no longer bypass the configured minimum and shrink the cache lifetime.
- **Cache hits echo the exact query**: the response question section is rewritten to the current query, so a shared case-normalized cache entry returns the exact query text.
- **Misc**: failed regex compilations are memoized to avoid recompiling broken patterns on every query; documented the trusted-reverse-proxy assumption behind `X-Forwarded-For` handling in DoH.

## v2.0.2 (2026-09-21)

- **Case-insensitive domain matching**: `full-map`, `full-list`, `suffix-tree` and `mix-list` (domain/keyword/full) matchers now treat DNS names case-insensitively, so rules stored as `Example.COM` also match queries for `EXAMPLE.com`. Regex rules keep their original case semantics (write lower-case patterns or use `(?i)`).
- **Case-insensitive hosts lookup**: hosts-file entries and lookups are normalized to lower case.
- **Case-normalized cache keys**: the cache now shares entries between `WWW.EXAMPLE.COM` and `www.example.com` queries.
- **Fork-branded docs**: README badges, release and contributor links now point at this repository instead of the upstream `shawn1m/overture`; the runtime log banner points to the fork as well. The stale codecov badge was dropped because the fork does not publish coverage.

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
