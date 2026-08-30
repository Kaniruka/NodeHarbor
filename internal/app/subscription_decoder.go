package app

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

func proxyURIListToYAML(document []byte) ([]byte, bool) {
	lines := strings.Split(strings.TrimSpace(string(document)), "\n")
	proxies := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.Contains(line, "://") {
			return nil, false
		}
		proxy, ok := parseProxyURI(line)
		if !ok {
			return nil, false
		}
		proxies = append(proxies, proxy)
	}
	if len(proxies) == 0 {
		return nil, false
	}
	converted, err := yaml.Marshal(map[string]any{"proxies": proxies})
	return converted, err == nil
}

func parseProxyURI(raw string) (map[string]any, bool) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "ss":
		if parsed.Host == "" {
			return nil, false
		}
		return parseShadowsocksURI(parsed)
	case "vmess":
		return parseVmessURI(parsed)
	case "vless":
		if parsed.Host == "" {
			return nil, false
		}
		return parseVlessURI(parsed)
	case "trojan":
		if parsed.Host == "" {
			return nil, false
		}
		return parseTrojanURI(parsed)
	case "hysteria", "hysteria2", "hy2":
		if parsed.Host == "" {
			return nil, false
		}
		return parseHysteriaURI(parsed)
	default:
		return nil, false
	}
}

func parseShadowsocksURI(parsed *url.URL) (map[string]any, bool) {
	if parsed.Hostname() == "" {
		return nil, false
	}
	method, password, ok := shadowsocksCredentials(parsed)
	if !ok {
		return nil, false
	}
	port, ok := parsedPort(parsed)
	if !ok {
		return nil, false
	}
	proxy := map[string]any{"name": proxyURIName(parsed, "Shadowsocks"), "type": "ss", "server": parsed.Hostname(), "port": port, "cipher": method, "password": password}
	return proxy, true
}

func shadowsocksCredentials(parsed *url.URL) (string, string, bool) {
	if parsed.User == nil {
		return "", "", false
	}
	username := parsed.User.Username()
	password, hasPassword := parsed.User.Password()
	if !hasPassword {
		decoded, ok := decodeBase64Payload(username)
		if !ok {
			return "", "", false
		}
		credentials := string(decoded)
		separator := strings.IndexByte(credentials, ':')
		if separator <= 0 {
			return "", "", false
		}
		return credentials[:separator], credentials[separator+1:], true
	}
	return username, password, username != ""
}

func parseVmessURI(parsed *url.URL) (map[string]any, bool) {
	encoded := strings.TrimPrefix(parsed.Opaque, "//")
	if encoded == "" {
		encoded = strings.TrimPrefix(parsed.Path, "//")
	}
	decoded, ok := decodeBase64Payload(encoded)
	if !ok {
		return nil, false
	}
	var values map[string]any
	if json.Unmarshal(decoded, &values) != nil {
		return nil, false
	}
	server := stringValue(values, "add")
	uuid := stringValue(values, "id")
	port, ok := integerValue(values, "port")
	if server == "" || uuid == "" || !ok || port <= 0 {
		return nil, false
	}
	proxy := map[string]any{
		"name":    stringOrDefault(values, "ps", proxyURIName(parsed, "VMess")),
		"type":    "vmess",
		"server":  server,
		"port":    port,
		"uuid":    uuid,
		"alterId": integerOrDefault(values, "aid", 0),
		"cipher":  stringOrDefault(values, "scy", "auto"),
	}
	if strings.EqualFold(stringValue(values, "tls"), "tls") || strings.EqualFold(stringValue(values, "tls"), "true") {
		proxy["tls"] = true
	}
	addTransportOptions(proxy, stringValue(values, "net"), stringValue(values, "path"), stringValue(values, "host"), stringValue(values, "sni"))
	return proxy, true
}

func parseVlessURI(parsed *url.URL) (map[string]any, bool) {
	uuid := ""
	if parsed.User != nil {
		uuid = parsed.User.Username()
	}
	port, ok := parsedPort(parsed)
	if uuid == "" || parsed.Hostname() == "" || !ok {
		return nil, false
	}
	query := parsed.Query()
	proxy := map[string]any{"name": proxyURIName(parsed, "VLESS"), "type": "vless", "server": parsed.Hostname(), "port": port, "uuid": uuid, "encryption": query.Get("encryption")}
	if proxy["encryption"] == "" {
		proxy["encryption"] = "none"
	}
	if flow := query.Get("flow"); flow != "" {
		proxy["flow"] = flow
	}
	addTransportOptions(proxy, query.Get("type"), query.Get("path"), query.Get("host"), firstNonEmpty(query.Get("sni"), query.Get("serverName")))
	if security := strings.ToLower(query.Get("security")); security == "tls" || security == "reality" {
		proxy["tls"] = true
		if sni := firstNonEmpty(query.Get("sni"), query.Get("serverName")); sni != "" {
			proxy["servername"] = sni
		}
	}
	if fingerprint := query.Get("fp"); fingerprint != "" {
		proxy["client-fingerprint"] = fingerprint
	}
	if publicKey := query.Get("pbk"); publicKey != "" {
		proxy["reality-opts"] = map[string]any{"public-key": publicKey, "short-id": query.Get("sid")}
	}
	return proxy, true
}

func parseTrojanURI(parsed *url.URL) (map[string]any, bool) {
	if parsed.User == nil || parsed.Hostname() == "" {
		return nil, false
	}
	password, ok := parsed.User.Password()
	if !ok || password == "" {
		password = parsed.User.Username()
	}
	if password == "" {
		return nil, false
	}
	port, ok := parsedPort(parsed)
	if !ok {
		return nil, false
	}
	query := parsed.Query()
	proxy := map[string]any{"name": proxyURIName(parsed, "Trojan"), "type": "trojan", "server": parsed.Hostname(), "port": port, "password": password, "tls": true}
	addTransportOptions(proxy, query.Get("type"), query.Get("path"), query.Get("host"), firstNonEmpty(query.Get("sni"), query.Get("peer")))
	if allowInsecure := strings.ToLower(firstNonEmpty(query.Get("allowinsecure"), query.Get("allowInsecure"), query.Get("insecure"))); allowInsecure == "1" || allowInsecure == "true" {
		proxy["skip-cert-verify"] = true
	}
	return proxy, true
}

func parseHysteriaURI(parsed *url.URL) (map[string]any, bool) {
	port, ok := parsedPort(parsed)
	if !ok {
		return nil, false
	}
	query := parsed.Query()
	protocol := strings.ToLower(parsed.Scheme)
	proxy := map[string]any{"name": proxyURIName(parsed, "Hysteria"), "type": protocol, "server": parsed.Hostname(), "port": port}
	if protocol == "hy2" {
		proxy["type"] = "hysteria2"
	}
	if parsed.User != nil {
		if password, ok := parsed.User.Password(); ok && password != "" {
			proxy["password"] = password
		} else if password := parsed.User.Username(); password != "" {
			proxy["password"] = password
		}
	}
	if protocol == "hysteria" {
		if auth := firstNonEmpty(query.Get("auth"), query.Get("auth-str")); auth != "" {
			proxy["auth-str"] = auth
		}
		if transport := query.Get("protocol"); transport != "" {
			proxy["protocol"] = transport
		}
	} else {
		if obfs := query.Get("obfs"); obfs != "" {
			proxy["obfs"] = obfs
		}
		if obfsPassword := query.Get("obfs-password"); obfsPassword != "" {
			proxy["obfs-password"] = obfsPassword
		}
	}
	if sni := firstNonEmpty(query.Get("sni"), query.Get("peer")); sni != "" {
		proxy["sni"] = sni
	}
	if insecure := strings.ToLower(query.Get("insecure")); insecure == "1" || insecure == "true" {
		proxy["skip-cert-verify"] = true
	}
	if up := firstNonEmpty(query.Get("upmbps"), query.Get("up")); up != "" {
		proxy["up"] = up
	}
	if down := firstNonEmpty(query.Get("downmbps"), query.Get("down")); down != "" {
		proxy["down"] = down
	}
	if alpn := query.Get("alpn"); alpn != "" {
		proxy["alpn"] = strings.Split(alpn, ",")
	}
	return proxy, true
}

func addTransportOptions(proxy map[string]any, network, path, host, servername string) {
	network = strings.ToLower(network)
	if network != "" && network != "tcp" {
		proxy["network"] = network
	}
	if servername != "" {
		proxy["servername"] = servername
	}
	switch network {
	case "ws":
		options := map[string]any{}
		if path != "" {
			options["path"] = path
		}
		if host != "" {
			options["headers"] = map[string]any{"Host": host}
		}
		if len(options) > 0 {
			proxy["ws-opts"] = options
		}
	case "grpc":
		if path != "" {
			proxy["grpc-opts"] = map[string]any{"grpc-service-name": path}
		}
	}
}

func decodeBase64Payload(value string) ([]byte, bool) {
	compact := strings.Join(strings.Fields(value), "")
	if compact == "" {
		return nil, false
	}
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		decoded, err := encoding.DecodeString(compact)
		if err == nil && len(decoded) > 0 {
			return decoded, true
		}
	}
	return nil, false
}

func parsedPort(parsed *url.URL) (int, bool) {
	port, err := strconv.Atoi(parsed.Port())
	return port, err == nil && port > 0 && port <= 65535
}

func proxyURIName(parsed *url.URL, fallback string) string {
	if name, err := url.PathUnescape(parsed.Fragment); err == nil && strings.TrimSpace(name) != "" {
		return name
	}
	return fmt.Sprintf("%s %s", fallback, parsed.Hostname())
}

func stringValue(values map[string]any, key string) string {
	value, ok := values[key]
	if !ok {
		return ""
	}
	return fmt.Sprint(value)
}

func stringOrDefault(values map[string]any, key, fallback string) string {
	if value := stringValue(values, key); value != "" {
		return value
	}
	return fallback
}

func integerValue(values map[string]any, key string) (int, bool) {
	value, ok := values[key]
	if !ok {
		return 0, false
	}
	switch item := value.(type) {
	case float64:
		return int(item), item == float64(int(item))
	case string:
		parsed, err := strconv.Atoi(item)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func integerOrDefault(values map[string]any, key string, fallback int) int {
	if value, ok := integerValue(values, key); ok {
		return value
	}
	return fallback
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
