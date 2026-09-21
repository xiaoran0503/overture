# Migration to the Go 1.27 maintenance release

- Build with Go 1.27 or newer. The supported Linux toolchain includes GCC 15.
- Redis caching moved from the archived `github.com/go-redis/redis/v8` path to `github.com/redis/go-redis/v9`. No `cacheRedisUrl` configuration change is required.
- YAML parsing moved to `gopkg.in/yaml.v3`. Existing configuration keys, lists, aliases, anchors, and typed boolean options remain supported. Quote ambiguous YAML 1.1-only scalar values when they are intended to remain strings.
- TCP/TLS resolver pools are now an internal, bounded `net.Conn` implementation. `tcpPoolConfig.enable`, `initialCapacity`, `maxCapacity`, and `idleTimeout` retain their existing meanings.
- `POST /reload` and `POST /reload/config` validate and build the replacement configuration before the active listener is stopped. Invalid requests leave the running configuration intact.
