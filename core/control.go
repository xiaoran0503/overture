// Copyright (c) 2016 shawn1m. All rights reserved.
// Use of this source code is governed by The MIT License (MIT) that can be
// found in the LICENSE file.

// Package core implements the essential features.
package core

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"

	"github.com/shawn1m/overture/core/config"
	"github.com/shawn1m/overture/core/inbound"
	"github.com/shawn1m/overture/core/outbound"
	log "github.com/sirupsen/logrus"
)

var (
	srv      *inbound.Server
	conf     *config.Config
	reloadMu sync.Mutex

	// startErrCh receives listener startup failures so the control layer can
	// roll back to the last known-good configuration instead of leaving the
	// process alive but serving nothing. Buffered so a failing goroutine
	// never blocks on the send.
	startErrCh = make(chan error, 1)

	// fallbackConf is the configuration that was serving when the current one
	// failed to come up; nil means the initial start has no rollback target.
	fallbackConf *config.Config
)

// Initiate the server with config file
func InitServer(configFilePath string) {
	loaded, err := config.Load(configFilePath)
	if err != nil {
		log.Fatalf("Failed to load config file %s: %s", configFilePath, err)
	}
	conf = loaded
	fallbackConf = nil
	Start()
	go watchStartErrors()
}

func Start() {
	// New dispatcher without RemoteClientBundle, RemoteClientBundle must be initiated when server is running
	dispatcher := outbound.Dispatcher{
		PrimaryDNS:                  conf.PrimaryDNS,
		AlternativeDNS:              conf.AlternativeDNS,
		OnlyPrimaryDNS:              conf.OnlyPrimaryDNS,
		WhenPrimaryDNSAnswerNoneUse: conf.WhenPrimaryDNSAnswerNoneUse,
		IPNetworkPrimarySet:         conf.IPNetworkPrimarySet,
		IPNetworkAlternativeSet:     conf.IPNetworkAlternativeSet,
		DomainPrimaryList:           conf.DomainPrimaryList,
		DomainAlternativeList:       conf.DomainAlternativeList,

		RedirectIPv6Record:       conf.IPv6UseAlternativeDNS,
		AlternativeDNSConcurrent: conf.AlternativeDNSConcurrent,
		MinimumTTL:               conf.MinimumTTL,
		DomainTTLMap:             conf.DomainTTLMap,

		Hosts: conf.Hosts,
		Cache: conf.Cache,
	}
	dispatcher.Init()

	srv = inbound.NewServer(conf.BindAddress, conf.DebugHTTPAddress, dispatcher, conf.RejectQType, conf.DohEnabled, conf.DebugHTTPToken)
	srv.HTTPMux.HandleFunc("/reload/config", ReloadConfigHandler)
	srv.HTTPMux.HandleFunc("/reload", ReloadHandler)
	srv.HTTPMux.HandleFunc("/config", ConfigHandler)

	server := srv
	go func() {
		if err := server.Run(); err != nil {
			log.Errorf("Server failed to start: %s", err)
			select {
			case startErrCh <- err:
			default:
			}
		}
	}()
}

// watchStartErrors recovers from listener startup failures. When a reload's
// new listeners cannot bind (a TOCTOU window between CheckBind and the real
// bind), it rolls back to the last known-good configuration so the process
// keeps serving instead of becoming a silent zombie. With no rollback target
// (initial start) or after a failed rollback it terminates via log.Fatalf so
// a supervisor can restart the service.
func watchStartErrors() {
	for err := range startErrCh {
		reloadMu.Lock()
		switch {
		case fallbackConf == nil:
			reloadMu.Unlock()
			log.Fatalf("Server failed to start: %s", err)
		case conf == fallbackConf:
			reloadMu.Unlock()
			log.Fatalf("Server failed to start even after rolling back to the last known-good configuration: %s", err)
		default:
			log.Errorf("Server failed to start (%s); rolling back to the last known-good configuration", err)
			if srv != nil {
				srv.Stop()
			}
			conf = fallbackConf
			Start()
			reloadMu.Unlock()
		}
	}
}

// Stop server
func Stop() {
	if srv != nil {
		srv.Stop()
	}
}

// ReloadHandler is passed to http.Server for handle "/reload" request
func ReloadHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	reloadMu.Lock()
	current := conf
	configPath := conf.FilePath
	reloadMu.Unlock()
	next, err := config.Load(configPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateReloadAddresses(current, next); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	io.WriteString(w, "Reload scheduled")
	go reloadWithConfig(next)
}

func ConfigHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Add("Content-Type", "application/json")
	reloadMu.Lock()
	publicConfig := *conf
	publicConfig.DebugHTTPToken = ""
	publicConfig.CacheRedisUrl = redactRedisURL(publicConfig.CacheRedisUrl)
	reloadMu.Unlock()
	jsonBinary, _ := json.Marshal(&publicConfig)
	_, _ = w.Write(jsonBinary)
}

func ReloadConfigHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	reloadMu.Lock()
	current := conf
	reloadMu.Unlock()
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	defer r.Body.Close()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	next, err := config.ApplyJSON(current, b)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateReloadAddresses(current, next); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	io.WriteString(w, "Reload scheduled")
	go reloadWithConfig(next)
}

// validateReloadAddresses rejects a reload whose new listener addresses cannot
// be bound, so a typo or an occupied port never kills the running server.
// Addresses that are unchanged are skipped because the current listener still
// owns them until the swap happens.
func validateReloadAddresses(current, next *config.Config) error {
	if next == nil {
		return fmt.Errorf("config is nil")
	}
	if next.BindAddress != current.BindAddress {
		if err := inbound.CheckBind(next.BindAddress, true); err != nil {
			return fmt.Errorf("bindAddress %s unavailable: %w", next.BindAddress, err)
		}
	}
	if next.DebugHTTPAddress != current.DebugHTTPAddress && next.DebugHTTPAddress != "" {
		if err := inbound.CheckBind(next.DebugHTTPAddress, false); err != nil {
			return fmt.Errorf("debugHTTPAddress %s unavailable: %w", next.DebugHTTPAddress, err)
		}
	}
	return nil
}

// Reload config and restart server after the current request has completed.
func Reload() {
	reloadMu.Lock()
	current := conf
	configPath := conf.FilePath
	reloadMu.Unlock()
	next, err := config.Load(configPath)
	if err != nil {
		log.Errorf("Failed to reload config file %s: %s", configPath, err)
		return
	}
	if err := validateReloadAddresses(current, next); err != nil {
		log.Errorf("Reload rejected: %s", err)
		return
	}
	reloadWithConfig(next)
}

func reloadWithConfig(next *config.Config) {
	reloadMu.Lock()
	defer reloadMu.Unlock()
	// Keep the serving configuration as the rollback target until the new
	// listeners are actually up; a startup failure then rolls back instead
	// of leaving the process alive but serving nothing.
	fallbackConf = conf
	conf = next
	reloadLocked()
}

func reloadLocked() {
	log.Infof("Reloading")
	Stop()
	Start()
}

func redactRedisURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User == nil {
		return raw
	}
	parsed.User = url.User("REDACTED")
	return parsed.String()
}
