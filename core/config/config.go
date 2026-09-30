// Copyright (c) 2016 shawn1m. All rights reserved.
// Use of this source code is governed by The MIT License (MIT) that can be
// found in the LICENSE file.

package config

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/shawn1m/overture/core/cache"
	"github.com/shawn1m/overture/core/common"
	"github.com/shawn1m/overture/core/finder"
	finderfull "github.com/shawn1m/overture/core/finder/full"
	finderregex "github.com/shawn1m/overture/core/finder/regex"
	"github.com/shawn1m/overture/core/hosts"
	"github.com/shawn1m/overture/core/matcher"
	matcherfinal "github.com/shawn1m/overture/core/matcher/final"
	matcherfull "github.com/shawn1m/overture/core/matcher/full"
	matchermix "github.com/shawn1m/overture/core/matcher/mix"
	matcherregex "github.com/shawn1m/overture/core/matcher/regex"
	matchersuffix "github.com/shawn1m/overture/core/matcher/suffix"
	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

// DoH3 is the optional DNS-over-HTTP/3 server (v2.5.0+). Zero-value keeps
// the historical plaintext DoH-on-debug-HTTP setup. When Enable is true,
// a dedicated QUIC/h3 listener is started; TLS certificates are required.
type DoH3 struct {
	Enable   bool   `yaml:"enable" json:"enable"`
	Address  string `yaml:"address" json:"address"`
	CertFile string `yaml:"certFile" json:"certFile"`
	KeyFile  string `yaml:"keyFile" json:"keyFile"`
}

// HealthCheck is the optional upstream liveness probe (v2.4.0+). Zero-value
// (enable: false) preserves historical behaviour: every selected upstream is
// queried. When enabled, consecutive exchange/probe failures mark an upstream
// down and it is skipped until it recovers; if every member of a group is
// down the group is still queried (fail-open).
type HealthCheck struct {
	Enable           bool   `yaml:"enable" json:"enable"`
	Interval         int    `yaml:"interval" json:"interval"`
	Timeout          int    `yaml:"timeout" json:"timeout"`
	FailThreshold    int    `yaml:"failThreshold" json:"failThreshold"`
	RecoverThreshold int    `yaml:"recoverThreshold" json:"recoverThreshold"`
	Domain           string `yaml:"domain" json:"domain"`
}

// RouteCache is the optional domain-list decision cache (v2.6.0+). Zero Size
// (the default) keeps historical behaviour: every cache-miss query walks
// domain lists. When Size > 0 the first domain-table classification
// (primary / alternative / undecided) is remembered for TTL seconds so later
// misses skip list scans. IP-network classify is never cached.
type RouteCache struct {
	Size int `yaml:"size" json:"size"`
	TTL  int `yaml:"ttl" json:"ttl"`
}

// ConfigSchemaVersion is the configuration schema version understood by this
// build. The field is optional: configs without it are legacy and load with no
// warnings, configs carrying an older or newer value get a MIGRATION.md
// pointer instead of being rejected, keeping the loader strictly additive.
const ConfigSchemaVersion = "2.1"

type Config struct {
	FilePath                    string                `yaml:"-" json:"-"`
	ConfigVersion               string                `yaml:"configVersion" json:"configVersion"`
	BindAddress                 string                `yaml:"bindAddress" json:"bindAddress"`
	DebugHTTPAddress            string                `yaml:"debugHTTPAddress" json:"debugHTTPAddress"`
	DebugHTTPToken              string                `yaml:"debugHTTPToken" json:"debugHTTPToken"`
	DohEnabled                  bool                  `yaml:"dohEnabled" json:"dohEnabled"`
	DoH3                        DoH3                  `yaml:"doh3" json:"doh3"`
	PrimaryDNS                  []*common.DNSUpstream `yaml:"primaryDNS" json:"primaryDNS"`
	AlternativeDNS              []*common.DNSUpstream `yaml:"alternativeDNS" json:"alternativeDNS"`
	OnlyPrimaryDNS              bool                  `yaml:"onlyPrimaryDNS" json:"onlyPrimaryDNS"`
	IPv6UseAlternativeDNS       bool                  `yaml:"ipv6UseAlternativeDNS" json:"ipv6UseAlternativeDNS"`
	AlternativeDNSConcurrent    bool                  `yaml:"alternativeDNSConcurrent" json:"alternativeDNSConcurrent"`
	WhenPrimaryDNSAnswerNoneUse string                `yaml:"whenPrimaryDNSAnswerNoneUse" json:"whenPrimaryDNSAnswerNoneUse"`
	IPNetworkFile               struct {
		Primary     string `yaml:"primary" json:"primary"`
		Alternative string `yaml:"alternative" json:"alternative"`
	} `yaml:"ipNetworkFile" json:"ipNetworkFile"`
	DomainFile struct {
		Primary            string `yaml:"primary" json:"primary"`
		Alternative        string `yaml:"alternative" json:"alternative"`
		PrimaryMatcher     string `yaml:"primaryMatcher" json:"primaryMatcher"`
		AlternativeMatcher string `yaml:"alternativeMatcher" json:"alternativeMatcher"`
		Matcher            string `yaml:"matcher" json:"matcher"`
	} `yaml:"domainFile" json:"domainFile"`
	HostsFile struct {
		HostsFile string `yaml:"hostsFile" json:"hostsFile"`
		Finder    string `yaml:"finder" json:"finder"`
	} `yaml:"hostsFile" json:"hostsFile"`
	MinimumTTL                   int         `yaml:"minimumTTL" json:"minimumTTL"`
	DomainTTLFile                string      `yaml:"domainTTLFile" json:"domainTTLFile"`
	CacheSize                    int         `yaml:"cacheSize" json:"cacheSize"`
	CacheRedisUrl                string      `yaml:"cacheRedisUrl" json:"cacheRedisUrl"`
	CacheRedisConnectionPoolSize int         `yaml:"cacheRedisConnectionPoolSize" json:"cacheRedisConnectionPoolSize"`
	RejectQType                  []uint16    `yaml:"rejectQType" json:"rejectQType"`
	UpstreamHealthCheck          HealthCheck `yaml:"upstreamHealthCheck" json:"upstreamHealthCheck"`
	DomainECSFile                string      `yaml:"domainECSFile" json:"domainECSFile"`
	UpstreamFailover             string      `yaml:"upstreamFailover" json:"upstreamFailover"`
	RouteCache                   RouteCache  `yaml:"routeCache" json:"routeCache"`

	DomainTTLMap            map[string]uint32   `yaml:"-" json:"-"`
	DomainECSMap            common.DomainECSMap `yaml:"-" json:"-"`
	DomainPrimaryList       matcher.Matcher     `yaml:"-" json:"-"`
	DomainAlternativeList   matcher.Matcher     `yaml:"-" json:"-"`
	IPNetworkPrimarySet     *common.IPSet       `yaml:"-" json:"-"`
	IPNetworkAlternativeSet *common.IPSet       `yaml:"-" json:"-"`
	Hosts                   *hosts.Hosts        `yaml:"-" json:"-"`
	Cache                   *cache.Cache        `yaml:"-" json:"-"`
}

// NewConfig loads a configuration. It is retained for compatibility with callers
// that expect startup failures to terminate the process.
func NewConfig(configFile string) *Config {
	config, err := Load(configFile)
	if err != nil {
		log.Fatalf("Failed to load config file %s: %s", configFile, err)
	}
	return config
}

// Load parses a configuration file and constructs its runtime-only members.
func Load(configFile string) (*Config, error) {
	config, err := parseConfigFile(configFile)
	if err != nil {
		return nil, err
	}
	config.FilePath = configFile
	return Build(config)
}

// Build refreshes the runtime-only members after a file or JSON configuration load.
func Build(config *Config) (*Config, error) {
	if config == nil {
		return nil, fmt.Errorf("config is nil")
	}
	switch config.ConfigVersion {
	case "":
		// Legacy configs omit the field; keep them silent to avoid noise.
	case ConfigSchemaVersion:
		log.Infof("Config schema version: %s", ConfigSchemaVersion)
	default:
		log.Warnf("Config schema version %q does not match this build's %q; review MIGRATION.md before upgrading", config.ConfigVersion, ConfigSchemaVersion)
	}
	if config.BindAddress == "" {
		return nil, fmt.Errorf("bindAddress is required")
	}
	if len(config.PrimaryDNS) == 0 {
		return nil, fmt.Errorf("primaryDNS requires at least one upstream")
	}
	if config.MinimumTTL < 0 {
		return nil, fmt.Errorf("minimumTTL must not be negative")
	}
	if config.DebugHTTPAddress != "" && config.DebugHTTPToken == "" && !isLoopbackAddress(config.DebugHTTPAddress) {
		return nil, fmt.Errorf("debugHTTPAddress %s is not loopback; set debugHTTPToken before exposing it", config.DebugHTTPAddress)
	}
	for _, upstreams := range [][]*common.DNSUpstream{config.PrimaryDNS, config.AlternativeDNS} {
		for _, upstream := range upstreams {
			if upstream == nil || upstream.Address == "" || upstream.Protocol == "" {
				return nil, fmt.Errorf("each DNS upstream requires address and protocol")
			}
			switch upstream.Protocol {
			case "udp", "tcp", "tcp-tls", "https", "https3", "doq":
			default:
				return nil, fmt.Errorf("unsupported DNS upstream protocol %q", upstream.Protocol)
			}
			if upstream.Timeout <= 0 {
				return nil, fmt.Errorf("DNS upstream %q timeout must be positive", upstream.Name)
			}
		}
	}
	if config.UpstreamHealthCheck.Enable {
		if config.UpstreamHealthCheck.Interval <= 0 {
			config.UpstreamHealthCheck.Interval = 30
		}
		if config.UpstreamHealthCheck.Timeout <= 0 {
			config.UpstreamHealthCheck.Timeout = 5
		}
		if config.UpstreamHealthCheck.FailThreshold <= 0 {
			config.UpstreamHealthCheck.FailThreshold = 3
		}
		if config.UpstreamHealthCheck.RecoverThreshold <= 0 {
			config.UpstreamHealthCheck.RecoverThreshold = 2
		}
		if config.UpstreamHealthCheck.Domain == "" {
			config.UpstreamHealthCheck.Domain = "example.com."
		}
	}

	switch strings.ToLower(config.UpstreamFailover) {
	case "", "concurrent":
		config.UpstreamFailover = "concurrent"
	case "sequential":
		config.UpstreamFailover = "sequential"
	default:
		return nil, fmt.Errorf("upstreamFailover must be concurrent or sequential")
	}
	if config.RouteCache.Size < 0 {
		return nil, fmt.Errorf("routeCache.size must not be negative")
	}
	if config.RouteCache.TTL < 0 {
		return nil, fmt.Errorf("routeCache.ttl must not be negative")
	}
	if config.RouteCache.Size > 0 {
		if config.RouteCache.TTL == 0 {
			config.RouteCache.TTL = 600
		}
		log.Infof("Route cache enabled: size %d ttl %ds", config.RouteCache.Size, config.RouteCache.TTL)
	}

	if config.DoH3.Enable {
		if config.DoH3.Address == "" || config.DoH3.CertFile == "" || config.DoH3.KeyFile == "" {
			return nil, fmt.Errorf("doh3.enable requires address, certFile and keyFile")
		}
		if _, err := os.Stat(config.DoH3.CertFile); err != nil {
			return nil, fmt.Errorf("doh3 certFile: %w", err)
		}
		if _, err := os.Stat(config.DoH3.KeyFile); err != nil {
			return nil, fmt.Errorf("doh3 keyFile: %w", err)
		}
	}

	config.DomainTTLMap = getDomainTTLMap(config.DomainTTLFile)
	config.DomainECSMap = getDomainECSMap(config.DomainECSFile)

	config.DomainPrimaryList = initDomainMatcher(config.DomainFile.Primary, config.DomainFile.PrimaryMatcher, config.DomainFile.Matcher)
	config.DomainAlternativeList = initDomainMatcher(config.DomainFile.Alternative, config.DomainFile.AlternativeMatcher, config.DomainFile.Matcher)

	config.IPNetworkPrimarySet = getIPNetworkSet(config.IPNetworkFile.Primary)
	config.IPNetworkAlternativeSet = getIPNetworkSet(config.IPNetworkFile.Alternative)

	if config.MinimumTTL > 0 {
		log.Infof("Minimum TTL has been set to %d", config.MinimumTTL)
	} else {
		log.Info("Minimum TTL is disabled")
	}

	config.Cache = cache.New(config.CacheSize, config.CacheRedisUrl, config.CacheRedisConnectionPoolSize)
	if config.CacheSize > 0 {
		log.Infof("CacheSize is %d", config.CacheSize)
	} else {
		log.Info("Cache is disabled")
	}

	h, err := hosts.New(config.HostsFile.HostsFile, getFinder(config.HostsFile.Finder))
	if err != nil {
		log.Warnf("Failed to load hosts file: %s", err)
	} else {
		config.Hosts = h
		log.Info("Hosts file has been loaded successfully")
	}

	return config, nil
}

func isLoopbackAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil || host == "" {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ApplyJSON overlays a partial JSON configuration and rebuilds all runtime state.
func ApplyJSON(current *Config, data []byte) (*Config, error) {
	if current == nil {
		return nil, fmt.Errorf("config is nil")
	}
	next := *current
	// A shallow copy shares the backing arrays of slice fields, and
	// encoding/json decodes array elements in place; decoding would then
	// overwrite the running configuration. MIGRATION.md promises invalid
	// requests leave the running configuration intact, so deep-copy the
	// slice fields (including the pointed-to DNSUpstream objects) before
	// decoding.
	next.PrimaryDNS = cloneUpstreams(current.PrimaryDNS)
	next.AlternativeDNS = cloneUpstreams(current.AlternativeDNS)
	next.RejectQType = append([]uint16(nil), current.RejectQType...)
	if err := json.Unmarshal(data, &next); err != nil {
		return nil, fmt.Errorf("parse JSON config: %w", err)
	}
	return Build(&next)
}

// cloneUpstreams copies the slice and every DNSUpstream value (and its
// EDNSClientSubnet pointer) so decoding into the overlay never touches the
// running configuration.
func cloneUpstreams(us []*common.DNSUpstream) []*common.DNSUpstream {
	if us == nil {
		return nil
	}
	out := make([]*common.DNSUpstream, len(us))
	for i, u := range us {
		if u == nil {
			continue
		}
		c := *u
		if u.EDNSClientSubnet != nil {
			ecs := *u.EDNSClientSubnet
			c.EDNSClientSubnet = &ecs
		}
		out[i] = &c
	}
	return out
}

func parseConfigFile(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	config := new(Config)
	if strings.HasSuffix(path, ".json") {
		err = json.Unmarshal(b, config)
	} else {
		err = yaml.Unmarshal(b, config)
	}

	if err != nil {
		return nil, fmt.Errorf("parse config file: %w", err)
	}

	return config, nil
}

func getDomainECSMap(file string) common.DomainECSMap {
	if file == "" {
		return nil
	}
	f, err := os.Open(file)
	if err != nil {
		log.Errorf("Failed to open domain ECS file %s: %s", file, err)
		return nil
	}
	defer f.Close()

	var rules common.DomainECSMap
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			log.Warnf("Skipping invalid domain ECS line %q", line)
			continue
		}
		policy := strings.ToLower(fields[1])
		if policy != "auto" && policy != "manual" && policy != "disable" {
			log.Warnf("Skipping domain ECS line with unknown policy %q", line)
			continue
		}
		rule := common.DomainECSRule{
			Domain: strings.ToLower(strings.TrimSuffix(fields[0], ".")),
			Policy: policy,
		}
		if len(fields) >= 3 {
			rule.ExternalIP = fields[2]
		}
		if len(fields) >= 4 && (strings.EqualFold(fields[3], "nocookie") || strings.EqualFold(fields[3], "true")) {
			rule.NoCookie = true
		}
		if policy == "manual" && net.ParseIP(rule.ExternalIP) == nil {
			log.Warnf("Skipping domain ECS manual rule without a valid IP: %q", line)
			continue
		}
		rules = append(rules, rule)
	}
	if err := scanner.Err(); err != nil {
		log.Warnf("Reading domain ECS file %s failed: %s", file, err)
	}
	if len(rules) > 0 {
		log.Infof("Domain ECS file %s has been loaded with %d records", file, len(rules))
	}
	return rules
}

func getDomainTTLMap(file string) map[string]uint32 {
	if file == "" {
		return map[string]uint32{}
	}

	f, err := os.Open(file)
	if err != nil {
		log.Errorf("Failed to open domain TTL file %s: %s", file, err)
		return nil
	}
	defer f.Close()

	successes := 0
	failures := 0
	var failedLines []string

	dtl := map[string]uint32{}

	scanner := bufio.NewScanner(f)

	for scanner.Scan() {
		line := scanner.Text()
		if len(line) == 0 {
			continue
		}
		words := strings.Fields(line)
		if len(words) > 1 {
			tempInt64, err := strconv.ParseUint(words[1], 10, 32)
			if err != nil {
				log.WithFields(log.Fields{"domain": words[0], "ttl": words[1]}).Warnf("Invalid TTL for domain %s: %s", words[0], words[1])
				failures++
				failedLines = append(failedLines, line)
				continue
			}
			// Normalize the trailing dot so "example.com." and "example.com" share one entry.
			dtl[strings.TrimSuffix(words[0], ".")] = uint32(tempInt64)
			successes++
		} else {
			failedLines = append(failedLines, line)
			failures++
		}
	}
	if err := scanner.Err(); err != nil {
		log.Warnf("Reading domain TTL file %s failed: %s", file, err)
	}

	if len(dtl) > 0 {
		log.Infof("Domain TTL file %s has been loaded with %d records (%d failed)", file, successes, failures)
		if len(failedLines) > 0 {
			log.Debugf("Failed lines (%s):", file)
			for _, line := range failedLines {
				log.Debug(line)
			}
		}
	} else {
		log.Warnf("No element has been loaded from domain TTL file: %s", file)
		if len(failedLines) > 0 {
			log.Debugf("Failed lines (%s):", file)
			for _, line := range failedLines {
				log.Debug(line)
			}
		}
	}

	return dtl
}

func getDomainMatcher(name string) (m matcher.Matcher) {
	if name == "" {
		// Empty matcher means the feature is not enabled, not a misconfiguration.
		return &matcherfull.Map{DataMap: make(map[string]struct{}, 100)}
	}
	switch name {
	case "suffix-tree":
		return matchersuffix.DefaultDomainTree()
	case "full-map":
		return &matcherfull.Map{DataMap: make(map[string]struct{}, 100)}
	case "full-list":
		return &matcherfull.List{}
	case "regex-list":
		return &matcherregex.List{}
	case "mix-list":
		return &matchermix.List{}
	case "final":
		return &matcherfinal.Default{}
	default:
		log.Warnf("Matcher %s does not exist, using full-map matcher as default", name)
		return &matcherfull.Map{DataMap: make(map[string]struct{}, 100)}
	}
}

func getFinder(name string) (f finder.Finder) {
	if name == "" {
		return &finderfull.Map{DataMap: make(map[string][]string, 100)}
	}
	switch name {
	case "regex-list":
		return &finderregex.List{RegexMap: make(map[string][]string, 100)}
	case "full-map":
		return &finderfull.Map{DataMap: make(map[string][]string, 100)}
	default:
		log.Warnf("Finder %s does not exist, using full-map finder as default", name)
		return &finderfull.Map{DataMap: make(map[string][]string, 100)}
	}
}

func initDomainMatcher(file string, name string, defaultName string) (m matcher.Matcher) {
	if name == "" {
		name = defaultName
	}
	m = getDomainMatcher(name)
	if name == "final" {
		return m
	}
	if file == "" {
		return
	}

	f, err := os.Open(file)
	if err != nil {
		log.Errorf("Failed to open domain file %s: %s", file, err)
		return nil
	}
	defer f.Close()

	lines := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) == 0 {
			continue
		}
		line = strings.TrimSpace(line)
		if line != "" {
			// DNS names are case-insensitive and a trailing dot is
			// insignificant; normalize it so list entries like
			// "example.com." keep matching the query "example.com".
			if err := m.Insert(strings.TrimSuffix(line, ".")); err != nil {
				log.Warnf("Skipping invalid rule %q in domain file %s: %s", line, file, err)
				continue
			}
			lines++
		}
	}
	if err := scanner.Err(); err != nil {
		log.Warnf("Reading domain file %s failed: %s", file, err)
	}

	if lines > 0 {
		log.Infof("Domain file %s has been loaded with %d records (%s)", file, lines, m.Name())
	} else {
		log.Warnf("No element has been loaded from domain file: %s", file)
	}

	return
}

func getIPNetworkSet(file string) *common.IPSet {
	if file == "" {
		// Empty path means the IP network filter is not enabled.
		return nil
	}
	ipNetList := make([]*net.IPNet, 0)

	f, err := os.Open(file)
	if err != nil {
		log.Errorf("Failed to open IP network file: %s", err)
		return nil
	}
	defer f.Close()

	successes := 0
	failures := 0
	var failedLines []string

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) == 0 {
			continue
		}
		// TrimSpace handles both LF and CRLF line endings.
		_, ipNet, err := net.ParseCIDR(strings.TrimSpace(line))
		if err != nil {
			log.Errorf("Error parsing IP network CIDR %s: %s", line, err)
			failures++
			failedLines = append(failedLines, line)
			continue
		}
		ipNetList = append(ipNetList, ipNet)
		successes++
	}
	if err := scanner.Err(); err != nil {
		log.Warnf("Reading IP network file %s failed: %s", file, err)
	}
	if len(ipNetList) > 0 {
		log.Infof("IP network file %s has been loaded with %d records", file, successes)
		if failures > 0 {
			log.Debugf("Failed lines (%s):", file)
			for _, line := range failedLines {
				log.Debug(line)
			}
		}
	} else {
		log.Warnf("No element has been loaded from IP network file: %s", file)
		if failures > 0 {
			log.Debugf("Failed lines (%s):", file)
			for _, line := range failedLines {
				log.Debug(line)
			}
		}
	}

	return common.NewIPSet(ipNetList)
}
