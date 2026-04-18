package main

import (
	"crypto/tls"
	"errors"
	"flag"
	"net/http"
	"os"
	"strings"

	logv2 "github.com/ViaQ/logerr/v2/log"
	log "github.com/ViaQ/logerr/v2/log/static"
	"github.com/log-file-metric-exporter/pkg/auth"
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

	// supportedTLSGroups maps OpenShift API TLS group names to Go tls.CurveID values.
	// These names come from configv1.TLSGroup constants defined in openshift/api.
	supportedTLSGroups = map[string]tls.CurveID{
		"X25519":         tls.X25519,
		"secp256r1":      tls.CurveP256,
		"secp384r1":      tls.CurveP384,
		"secp521r1":      tls.CurveP521,
		"X25519MLKEM768": tls.X25519MLKEM768,
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
		if strings.HasPrefix(c, "TLS_") {
			// TLS 1.3 ciphers are not configurable in Go — they are always
			// enabled automatically when TLS 1.3 is negotiated. Skipping
			// from the explicit cipher list does not disable them.
			log.Info("skipping TLS 1.3 cipher suite: automatically enabled by Go runtime", "cipherSuite", c)
			continue
		}
		if strings.HasPrefix(c, "DHE-") {
			// Go's crypto/tls does not implement the DHE key exchange.
			// These ciphers from the TLS profile cannot be honored.
			log.Info("ignoring cipher suite not supported by Go crypto/tls: DHE key exchange is not implemented", "cipherSuite", c)
			continue
		}
		ianaCipher, found := openSSLToIANACiphersMap[c]
		if found {
			ianaCiphers = append(ianaCiphers, ianaCipher)
		} else {
			log.Error(errors.New("unsupported cipher suite"), "unsupported cipher suite", "cipherSuite", c)
		}
	}

	return ianaCiphers
}

// parseTLSGroups converts OpenShift API TLS group names to Go tls.CurveID values.
// Unknown groups are logged and skipped.
func parseTLSGroups(groupNames []string) []tls.CurveID {
	curvePreferences := make([]tls.CurveID, 0, len(groupNames))
	for _, groupName := range groupNames {
		groupName = strings.TrimSpace(groupName)
		curveID, found := supportedTLSGroups[groupName]
		if !found {
			log.Error(errors.New("unsupported TLS group"), "unsupported TLS group", "group", groupName)
		} else {
			curvePreferences = append(curvePreferences, curveID)
		}
	}
	return curvePreferences
}

func InitLogger(verbosity int) {
	logger := logv2.NewLogger("log-file-metric-exporter", logv2.WithVerbosity(verbosity))
	log.SetLogger(logger)
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
		secureMetrics bool
		groups        string
	)
	flag.StringVar(&dir, "dir", logDir, "Directory containing log files")
	flag.IntVar(&verbosity, "verbosity", 0, "set verbosity level")
	flag.StringVar(&addr, "http", ":2112", "HTTP service address where metrics are exposed")
	flag.StringVar(&crtFile, "crtFile", "/etc/fluent/metrics/tls.crt", "cert file for log-file-metric-exporter service")
	flag.StringVar(&keyFile, "keyFile", "/etc/fluent/metrics/tls.key", "key file for log-file-metric-exporter service")
	flag.StringVar(&tlsMinVersion, "tlsMinVersion", "", "minimal TLS version to accept")
	flag.StringVar(&cipherSuites, "cipherSuites", "", "cipher suites to accept")
	flag.BoolVar(&secureMetrics, "secureMetrics", false, "require valid bearer token for metrics scraping")
	flag.StringVar(&groups, "groups", "", "TLS groups/curves to use for key exchange (e.g. X25519,secp256r1,secp384r1)")
	flag.Parse()

	InitLogger(verbosity)
	log.Info("start log metric exporter", "path", dir)

	w, err := logwatch.New(dir)
	if err != nil {
		log.Error(err, "watch error", "path", dir)
		os.Exit(1)
	}
	defer w.Close()
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
		ianaNames := openSSLToIANACipherSuites(strings.Split(cipherSuites, ","))
		cipherSuiteIds := make([]uint16, 0, len(ianaNames))
		for _, suiteName := range ianaNames {
			suiteId, found := supportedCipherSuites[suiteName]
			if !found {
				log.Error(errors.New("unsupported cipher suite"), "unsupported cipher suite", "cipherSuite", suiteName)
			} else {
				cipherSuiteIds = append(cipherSuiteIds, suiteId)
			}
		}
		tlsConfig.CipherSuites = cipherSuiteIds
	}

	// TLS Curves Support
	groups = strings.TrimSpace(groups)
	if groups != "" {
		tlsConfig.CurvePreferences = parseTLSGroups(strings.Split(groups, ","))
	}

	// Build a server:
	httpServer := http.Server{
		Addr:         addr,
		TLSConfig:    &tlsConfig,
		TLSNextProto: make(map[string]func(*http.Server, *tls.Conn, http.Handler)), // disable HTTP/2
	}
	handler := http.Handler(promhttp.Handler())
	if secureMetrics {
		authenticator, err := auth.NewKubeAuthenticator()
		if err != nil {
			log.Error(err, "failed to create authenticator")
			os.Exit(1)
		}
		log.Info("metrics endpoint secured with bearer token authentication")
		handler = auth.AuthMiddleware(authenticator, handler)
	}
	http.Handle("/metrics", handler)
	if err := httpServer.ListenAndServeTLS(crtFile, keyFile); err != nil {
		log.Error(err, "error in HTTP listen", "addr", addr)
		os.Exit(1)
	}
}
