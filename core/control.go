// Copyright (c) 2016 shawn1m. All rights reserved.
// Use of this source code is governed by The MIT License (MIT) that can be
// found in the LICENSE file.

// Package core implements the essential features.
package core

import (
	"encoding/json"
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
)

// Initiate the server with config file
func InitServer(configFilePath string) {
	loaded, err := config.Load(configFilePath)
	if err != nil {
		log.Fatalf("Failed to load config file %s: %s", configFilePath, err)
	}
	conf = loaded
	Start()
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
			log.Fatalf("Server failed to start: %s", err)
		}
	}()
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
	configPath := conf.FilePath
	reloadMu.Unlock()
	next, err := config.Load(configPath)
	if err != nil {
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
		http.Error(w, err.Error(), 500)
		return
	}
	next, err := config.ApplyJSON(current, b)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	io.WriteString(w, "Reload scheduled")
	go reloadWithConfig(next)
}

// Reload config and restart server after the current request has completed.
func Reload() {
	reloadMu.Lock()
	configPath := conf.FilePath
	reloadMu.Unlock()
	next, err := config.Load(configPath)
	if err != nil {
		log.Errorf("Failed to reload config file %s: %s", configPath, err)
		return
	}
	reloadWithConfig(next)
}

func reloadWithConfig(next *config.Config) {
	reloadMu.Lock()
	defer reloadMu.Unlock()
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
