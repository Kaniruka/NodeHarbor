package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	mihomoTestChannelTimeout = 10 * time.Second
	ipv4IdentityEndpoint     = "https://api4.ipify.org"
	ipv6IdentityEndpoint     = "https://api6.ipify.org"
)

// MihomoTestChannel owns one isolated local Mihomo listener per Proxy Node.
// The process, configuration directory, listener and HTTP transport are all
// created by NodeHarbor; no foreign Mihomo instance is reused.
type MihomoTestChannel struct {
	executablePath string
	build          MihomoBuild

	mu     sync.Mutex
	leases map[string]*mihomoTestLease
}

type mihomoTestLease struct {
	command   *exec.Cmd
	logFile   *os.File
	directory string
	client    *http.Client
}

func NewMihomoTestChannel(executablePath string, build MihomoBuild) *MihomoTestChannel {
	return &MihomoTestChannel{executablePath: executablePath, build: build, leases: make(map[string]*mihomoTestLease)}
}

func (channel *MihomoTestChannel) Probe(ctx context.Context, node ProxyNode) (ProbeResult, error) {
	attempt, err := channel.ProbeAttempt(ctx, node, DefaultAvailabilityURLs[0])
	if err != nil {
		return ProbeResult{}, err
	}
	candidates, err := channel.DiscoverExitIdentities(ctx, node, "ipv4")
	if err != nil {
		return ProbeResult{Verified: attempt.Verified, Latency: attempt.Latency}, err
	}
	return ProbeResult{Verified: attempt.Verified, Latency: attempt.Latency, ExitIdentities: candidates}, nil
}

func (channel *MihomoTestChannel) ProbeAttempt(ctx context.Context, node ProxyNode, target string) (AvailabilityAttempt, error) {
	client, err := channel.HTTPClient(ctx, node)
	if err != nil {
		return AvailabilityAttempt{}, err
	}
	started := time.Now()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return AvailabilityAttempt{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return AvailabilityAttempt{}, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	_ = response.Body.Close()
	return AvailabilityAttempt{
		Success:  response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices,
		Verified: true,
		Latency:  time.Since(started),
	}, nil
}

func (channel *MihomoTestChannel) DiscoverExitIdentities(ctx context.Context, node ProxyNode, family string) ([]ExitIdentityCandidate, error) {
	endpoint := ipv4IdentityEndpoint
	if family == "ipv6" {
		endpoint = ipv6IdentityEndpoint
	} else if family != "ipv4" {
		return nil, fmt.Errorf("unsupported Exit Identity address family %q", family)
	}
	client, err := channel.HTTPClient(ctx, node)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("Exit Identity endpoint returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 64))
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(strings.TrimSpace(string(body)))
	if ip == nil || addressFamily(ip.String()) != family {
		return nil, errors.New("Exit Identity endpoint returned an invalid address")
	}
	return []ExitIdentityCandidate{{IP: ip.String(), Verified: true}}, nil
}

func (channel *MihomoTestChannel) HTTPClient(ctx context.Context, node ProxyNode) (*http.Client, error) {
	key, err := mihomoNodeKey(node)
	if err != nil {
		return nil, err
	}
	channel.mu.Lock()
	defer channel.mu.Unlock()
	if lease := channel.leases[key]; lease != nil {
		return lease.client, nil
	}
	lease, err := channel.startLease(ctx, node)
	if err != nil {
		return nil, err
	}
	channel.leases[key] = lease
	return lease.client, nil
}

// Release tears down the isolated core after this Proxy Node has finished its
// evaluation. The application calls it even when Availability Check fails.
func (channel *MihomoTestChannel) Release(node ProxyNode) error {
	key, err := mihomoNodeKey(node)
	if err != nil {
		return err
	}
	channel.mu.Lock()
	lease := channel.leases[key]
	channel.mu.Unlock()
	if lease == nil {
		return nil
	}
	if err := stopMihomoLease(lease); err != nil {
		return err
	}
	channel.mu.Lock()
	if channel.leases[key] == lease {
		delete(channel.leases, key)
	}
	channel.mu.Unlock()
	return nil
}

func (channel *MihomoTestChannel) startLease(ctx context.Context, node ProxyNode) (*mihomoTestLease, error) {
	if channel.executablePath == "" {
		return nil, errors.New("Mihomo executable path is required for Test Channel")
	}
	if err := verifyMihomoExecutable(channel.executablePath, channel.build); err != nil {
		return nil, err
	}
	directory, err := os.MkdirTemp("", "nodeharbor-test-channel-")
	if err != nil {
		return nil, fmt.Errorf("create Test Channel directory: %w", err)
	}
	cleanup := func(cause error) error {
		if cleanupErr := os.RemoveAll(directory); cleanupErr != nil {
			return errors.Join(cause, cleanupErr)
		}
		return cause
	}
	proxyPort, err := freeLoopbackPort()
	if err != nil {
		return nil, cleanup(err)
	}
	proxyAddress := fmt.Sprintf("127.0.0.1:%d", proxyPort)
	configPath := filepath.Join(directory, "config.yaml")
	config, err := mihomoTestChannelConfig(node, proxyPort)
	if err != nil {
		return nil, cleanup(err)
	}
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		return nil, cleanup(fmt.Errorf("write Test Channel configuration: %w", err))
	}
	logFile, err := os.OpenFile(filepath.Join(directory, "mihomo.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, cleanup(fmt.Errorf("create Test Channel log: %w", err))
	}
	command := exec.Command(channel.executablePath, "-d", directory, "-f", configPath)
	command.Stdout = logFile
	command.Stderr = logFile
	if err := command.Start(); err != nil {
		closeErr := logFile.Close()
		return nil, cleanup(errors.Join(fmt.Errorf("start isolated Mihomo Test Channel: %w", err), closeErr))
	}
	lease := &mihomoTestLease{command: command, logFile: logFile, directory: directory}
	readyContext, cancel := context.WithTimeout(ctx, mihomoTestChannelTimeout)
	err = waitForLoopbackListener(readyContext, proxyAddress, command)
	cancel()
	if err != nil {
		cleanupErr := stopMihomoLease(lease)
		return nil, errors.Join(fmt.Errorf("wait for Mihomo Test Channel: %w", err), cleanupErr)
	}
	proxy, _ := url.Parse("http://" + proxyAddress)
	transport := &http.Transport{
		Proxy:                 http.ProxyURL(proxy),
		DialContext:           (&net.Dialer{Timeout: mihomoTestChannelTimeout}).DialContext,
		TLSHandshakeTimeout:   mihomoTestChannelTimeout,
		ResponseHeaderTimeout: mihomoTestChannelTimeout,
	}
	lease.client = &http.Client{Transport: transport, Timeout: mihomoTestChannelTimeout}
	return lease, nil
}

func mihomoTestChannelConfig(node ProxyNode, proxyPort int) ([]byte, error) {
	proxy := make(map[string]any, len(node.Config)+1)
	for key, value := range node.Config {
		proxy[key] = value
	}
	proxy["name"] = "NODEHARBOR_TEST_NODE"
	config := map[string]any{
		"mixed-port": proxyPort,
		"allow-lan":  false,
		"mode":       "rule",
		"log-level":  "warning",
		"proxies":    []map[string]any{proxy},
		"proxy-groups": []map[string]any{{
			"name":    "NODEHARBOR_TEST_GROUP",
			"type":    "select",
			"proxies": []string{"NODEHARBOR_TEST_NODE"},
		}},
		"rules": []string{"MATCH,NODEHARBOR_TEST_GROUP"},
	}
	return yaml.Marshal(config)
}

func waitForLoopbackListener(ctx context.Context, address string, command *exec.Cmd) error {
	dialer := &net.Dialer{Timeout: 250 * time.Millisecond}
	for {
		if command.ProcessState != nil && command.ProcessState.Exited() {
			return errors.New("Mihomo Test Channel process exited before readiness")
		}
		connection, err := dialer.DialContext(ctx, "tcp", address)
		if err == nil {
			_ = connection.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func freeLoopbackPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("reserve Test Channel port: %w", err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

func mihomoNodeKey(node ProxyNode) (string, error) {
	data, err := yaml.Marshal(struct {
		Name   string         `yaml:"name"`
		Config map[string]any `yaml:"config"`
	}{Name: node.Name, Config: node.Config})
	if err != nil {
		return "", fmt.Errorf("fingerprint Test Channel node: %w", err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func (channel *MihomoTestChannel) Close() error {
	channel.mu.Lock()
	leases := make([]*mihomoTestLease, 0, len(channel.leases))
	for _, lease := range channel.leases {
		leases = append(leases, lease)
	}
	channel.mu.Unlock()
	var closeErr error
	for _, lease := range leases {
		if err := stopMihomoLease(lease); err != nil {
			closeErr = errors.Join(closeErr, err)
			continue
		}
		channel.mu.Lock()
		for key, current := range channel.leases {
			if current == lease {
				delete(channel.leases, key)
			}
		}
		channel.mu.Unlock()
	}
	return closeErr
}

func stopMihomoLease(lease *mihomoTestLease) error {
	var cleanupErr error
	killed := false
	if lease.command != nil && lease.command.Process != nil {
		if err := lease.command.Process.Kill(); err == nil {
			killed = true
		} else if !errors.Is(err, os.ErrProcessDone) {
			cleanupErr = errors.Join(cleanupErr, err)
		}
		if err := lease.command.Wait(); err != nil && !killed && (lease.command.ProcessState == nil || !lease.command.ProcessState.Exited()) {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	if lease.logFile != nil {
		if err := lease.logFile.Close(); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	if err := os.RemoveAll(lease.directory); err != nil {
		cleanupErr = errors.Join(cleanupErr, err)
	}
	return cleanupErr
}
