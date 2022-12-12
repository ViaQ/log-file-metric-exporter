package main

import (
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/ViaQ/logerr/log"
	"github.com/log-file-metric-exporter/pkg/logwatch"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	logDir = "/var/log/pods"

	supportedTlsVersions = map[string]uint16{
		"VersionTLS10": tls.VersionTLS10,
		"VersionTLS11": tls.VersionTLS11,
		"VersionTLS12": tls.VersionTLS12,
		"VersionTLS13": tls.VersionTLS13,
	}

	supportedCipherSuites = func() map[string]uint16 {
		cipherSuites := map[string]uint16{}

		for _, suite := range tls.CipherSuites() {
			cipherSuites[suite.Name] = suite.ID
		}
		for _, suite := range tls.InsecureCipherSuites() {
			cipherSuites[suite.Name] = suite.ID
		}

		return cipherSuites
	}()

	// openSSLToIANACiphersMap maps OpenSSL cipher suite names to IANA names
	// ref: https://www.iana.org/assignments/tls-parameters/tls-parameters.xml
	openSSLToIANACiphersMap = map[string]string{
		// TLS 1.3 ciphers - not configurable in go 1.13, all of them are used in TLSv1.3 flows
		//	"TLS_AES_128_GCM_SHA256":       "TLS_AES_128_GCM_SHA256",       // 0x13,0x01
		//	"TLS_AES_256_GCM_SHA384":       "TLS_AES_256_GCM_SHA384",       // 0x13,0x02
		//	"TLS_CHACHA20_POLY1305_SHA256": "TLS_CHACHA20_POLY1305_SHA256", // 0x13,0x03

		// TLS 1.2
		"ECDHE-ECDSA-AES128-GCM-SHA256": "TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256",       // 0xC0,0x2B
		"ECDHE-RSA-AES128-GCM-SHA256":   "TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256",         // 0xC0,0x2F
		"ECDHE-ECDSA-AES256-GCM-SHA384": "TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384",       // 0xC0,0x2C
		"ECDHE-RSA-AES256-GCM-SHA384":   "TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384",         // 0xC0,0x30
		"ECDHE-ECDSA-CHACHA20-POLY1305": "TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256", // 0xCC,0xA9
		"ECDHE-RSA-CHACHA20-POLY1305":   "TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256",   // 0xCC,0xA8
		"ECDHE-ECDSA-AES128-SHA256":     "TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA256",       // 0xC0,0x23
		"ECDHE-RSA-AES128-SHA256":       "TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA256",         // 0xC0,0x27
		"AES128-GCM-SHA256":             "TLS_RSA_WITH_AES_128_GCM_SHA256",               // 0x00,0x9C
		"AES256-GCM-SHA384":             "TLS_RSA_WITH_AES_256_GCM_SHA384",               // 0x00,0x9D
		"AES128-SHA256":                 "TLS_RSA_WITH_AES_128_CBC_SHA256",               // 0x00,0x3C

		// TLS 1
		"ECDHE-ECDSA-AES128-SHA": "TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA", // 0xC0,0x09
		"ECDHE-RSA-AES128-SHA":   "TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA",   // 0xC0,0x13
		"ECDHE-ECDSA-AES256-SHA": "TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA", // 0xC0,0x0A
		"ECDHE-RSA-AES256-SHA":   "TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA",   // 0xC0,0x14

		// SSL 3
		"AES128-SHA":   "TLS_RSA_WITH_AES_128_CBC_SHA",  // 0x00,0x2F
		"AES256-SHA":   "TLS_RSA_WITH_AES_256_CBC_SHA",  // 0x00,0x35
		"DES-CBC3-SHA": "TLS_RSA_WITH_3DES_EDE_CBC_SHA", // 0x00,0x0A
	}
)

// openSSLToIANACipherSuites maps input OpenSSL Cipher Suite names to their
// IANA counterparts.
// Unknown ciphers are left out.
func openSSLToIANACipherSuites(ciphers []string) []string {
	ianaCiphers := make([]string, 0, len(ciphers))

	for _, c := range ciphers {
		ianaCipher, found := openSSLToIANACiphersMap[c]
		if found {
			ianaCiphers = append(ianaCiphers, ianaCipher)
		} else {
			log.Error(errors.New("unsupported cipher suite"), "unsupported cipher suite", "cipherSuite", c)
		}
	}

	return ianaCiphers
}

func main() {
	var (
		dir           string
		addr          string
		crtFile       string
		keyFile       string
		verbosity     int
		tlsMinVersion string
		cipherSuites  string
	)
	flag.StringVar(&dir, "dir", logDir, "Directory containing log files")
	flag.IntVar(&verbosity, "verbosity", 0, "set verbosity level")
	flag.StringVar(&addr, "http", ":2112", "HTTP service address where metrics are exposed")
	flag.StringVar(&crtFile, "crtFile", "/etc/fluent/metrics/tls.crt", "cert file for log-file-metric-exporter service")
	flag.StringVar(&keyFile, "keyFile", "/etc/fluent/metrics/tls.key", "key file for log-file-metric-exporter service")
	flag.StringVar(&tlsMinVersion, "tlsMinVersion", "", "minimal TLS version to accept")
	flag.StringVar(&cipherSuites, "cipherSuites", "", "cipher suites to accept")
	flag.Parse()

	log.SetLogLevel(verbosity)
	log.Info("start log metric exporter", "path", dir)

	w, err := logwatch.New(dir)
	if err != nil {
		log.Error(err, "watch error", "path", dir)
		os.Exit(1)
	}

	go func() {
		if err := w.Watch(); err != nil {
			log.Error(err, "error in watch", "path", dir)
			os.Exit(1)
		}
	}()

	tlsConfig := tls.Config{}

	tlsMinVersion = strings.TrimSpace(tlsMinVersion)
	if tlsMinVersion != "" {
		tlsMinVersionNum, found := supportedTlsVersions[tlsMinVersion]
		if !found {
			log.Error(errors.New("invalid minimal TLS version"), "invalid minimal TLS version", "tlsMinVersion", tlsMinVersion)
			os.Exit(1)
		}
		tlsConfig.MinVersion = tlsMinVersionNum
	}

	cipherSuites = strings.TrimSpace(cipherSuites)
	if cipherSuites != "" {
		cipherSuiteIds := make([]uint16, 10)
		for _, suiteName := range openSSLToIANACipherSuites(strings.Split(cipherSuites, ",")) {
			suiteId, found := supportedCipherSuites[suiteName]
			if !found {
				log.Error(errors.New("unsupported cipher suite"), "unsupported cipher suite", "cipherSuite", suiteName)
			} else {
				fmt.Println(suiteName)
				cipherSuiteIds = append(cipherSuiteIds, suiteId)
			}
		}
		tlsConfig.CipherSuites = cipherSuiteIds
	}

	// Build a server:
	httpServer := http.Server{
		Addr:         addr,
		TLSConfig:    &tlsConfig,
		TLSNextProto: make(map[string]func(*http.Server, *tls.Conn, http.Handler)), // disable HTTP/2
	}
	http.Handle("/metrics", promhttp.Handler())
	if err := httpServer.ListenAndServeTLS(crtFile, keyFile); err != nil {
		log.Error(err, "error in HTTP listen", "addr", addr)
		os.Exit(1)
	}
}
