package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// ProcSurfingRuntimeInspector reads only Linux process metadata. It never
// opens Surfing configuration, module, firewall, route, or DNS files.
type ProcSurfingRuntimeInspector struct {
	ProcRoot            string
	NetRoot             string
	ProbeTargetVerifier SurfingProbeTargetVerifier
}

func (inspector ProcSurfingRuntimeInspector) Inspect(ctx context.Context) (SurfingRuntimeInspection, error) {
	if runtime.GOOS != "android" && inspector.ProcRoot == "" {
		return SurfingRuntimeInspection{Mode: "inactive"}, nil
	}
	root := inspector.ProcRoot
	if root == "" {
		root = "/proc"
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return SurfingRuntimeInspection{}, fmt.Errorf("read process table: %w", err)
	}
	verifier := inspector.ProbeTargetVerifier
	if verifier == nil {
		verifier = LoopbackSurfingProbeTargetVerifier{}
	}
	var candidate *SurfingRuntimeInspection
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return SurfingRuntimeInspection{}, err
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		cmdline, err := os.ReadFile(filepath.Join(root, entry.Name(), "cmdline"))
		if err != nil {
			return SurfingRuntimeInspection{}, fmt.Errorf("read process %s command line: %w", entry.Name(), err)
		}
		if !isSurfingProcess(string(cmdline)) {
			continue
		}
		inspection, err := inspectSurfingProcess(ctx, root, cmdline, verifier)
		if err != nil {
			return SurfingRuntimeInspection{}, err
		}
		if inspection.Mode == "tun" {
			return inspection, nil
		}
		if candidate == nil || candidate.Mode == "inactive" || (candidate.Mode != "unknown" && inspection.Mode == "unknown") {
			copy := inspection
			candidate = &copy
		}
	}
	if candidate != nil {
		tunActive, err := inspector.tunInterfaceActive(root)
		if err != nil {
			return SurfingRuntimeInspection{}, fmt.Errorf("inspect network interfaces: %w", err)
		}
		if tunActive {
			candidate.Mode = "tun"
			candidate.ProcessIdentityVerified = false
			candidate.TestChannelBypassVerified = false
			candidate.ProbeTargetBypassVerified = false
		}
		return *candidate, nil
	}
	return SurfingRuntimeInspection{Mode: "inactive"}, nil
}

func (inspector ProcSurfingRuntimeInspector) tunInterfaceActive(procRoot string) (bool, error) {
	root := inspector.NetRoot
	if root == "" {
		root = filepath.Join(filepath.Dir(procRoot), "sys", "class", "net")
		if procRoot == "/proc" {
			root = "/sys/class/net"
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		name := strings.ToLower(entry.Name())
		if name == "tun" || strings.HasPrefix(name, "tun") || strings.HasPrefix(name, "utun") || strings.HasPrefix(name, "clash") || strings.HasPrefix(name, "mihomo") || strings.HasPrefix(name, "singtun") {
			return true, nil
		}
	}
	return false, nil
}

func inspectSurfingProcess(ctx context.Context, procRoot string, rawCmdline []byte, verifier SurfingProbeTargetVerifier) (SurfingRuntimeInspection, error) {
	args := splitProcCommandLine(rawCmdline)
	mode := surfingMode(args)
	if mode == "" {
		mode = "unknown"
	}
	inspection := SurfingRuntimeInspection{Detected: true, Mode: mode}
	if mode != "redirect" && mode != "tproxy" {
		return inspection, nil
	}
	uid, gid, err := processIdentity(filepath.Join(procRoot, "self", "status"))
	if err != nil {
		return inspection, nil
	}
	allowedUIDs := bypassIDs(args, "uid")
	allowedGIDs := bypassIDs(args, "gid")
	inspection.ProcessIdentityVerified = containsInt(allowedUIDs, uid)
	inspection.TestChannelBypassVerified = inspection.ProcessIdentityVerified && containsInt(allowedGIDs, gid)
	if inspection.TestChannelBypassVerified && verifier != nil {
		verified, err := verifier.Verify(ctx)
		if err != nil {
			return inspection, nil
		}
		inspection.ProbeTargetBypassVerified = verified
	}
	return inspection, nil
}

// LoopbackSurfingProbeTargetVerifier uses a fresh loopback listener as an
// independent target. Transparent-proxy rules must not recapture this target;
// the verifier never contacts Surfing or an external scoring provider.
type LoopbackSurfingProbeTargetVerifier struct{}

func (LoopbackSurfingProbeTargetVerifier) Verify(ctx context.Context) (bool, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return false, err
	}
	defer listener.Close()
	accepted := make(chan bool, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			accepted <- false
			return
		}
		defer connection.Close()
		remote, remoteOK := connection.RemoteAddr().(*net.TCPAddr)
		accepted <- remoteOK && remote.IP.IsLoopback()
	}()
	dialer := &net.Dialer{}
	connection, err := dialer.DialContext(ctx, "tcp", listener.Addr().String())
	if err != nil {
		return false, err
	}
	_ = connection.Close()
	select {
	case verified := <-accepted:
		return verified, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

func isSurfingProcess(rawCmdline string) bool {
	args := splitProcCommandLine([]byte(rawCmdline))
	if len(args) == 0 {
		return false
	}
	name := strings.ToLower(filepath.Base(args[0]))
	if strings.Contains(name, "nodeharbor") {
		return false
	}
	for _, candidate := range []string{"surfing", "mihomo", "clash"} {
		if strings.Contains(name, candidate) {
			return true
		}
	}
	if name == "box" || name == "box64" || strings.HasPrefix(name, "box-") {
		return true
	}
	for _, arg := range args[1:] {
		lower := strings.ToLower(arg)
		if strings.Contains(lower, "surfing") {
			return true
		}
	}
	return false
}

func splitProcCommandLine(raw []byte) []string {
	parts := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			result = append(result, part)
		}
	}
	return result
}

func surfingMode(args []string) string {
	for index, arg := range args {
		value := strings.ToLower(strings.TrimSpace(arg))
		if (value == "tun" || value == "--tun" || value == "tun-mode" || value == "--tun-mode" || value == "tun_enabled" || value == "--tun_enabled") && index+1 < len(args) && isFalseValue(args[index+1]) {
			continue
		}
		if isEnabledFlag(value, "tun") || isEnabledFlag(value, "tun-mode") || isEnabledFlag(value, "tun_enabled") {
			return "tun"
		}
	}
	for _, arg := range args {
		value := strings.ToLower(strings.TrimSpace(arg))
		if strings.Contains(value, "tproxy-port") || strings.Contains(value, "tproxy") {
			return "tproxy"
		}
		if strings.Contains(value, "redir-port") || strings.Contains(value, "redirect") {
			return "redirect"
		}
	}
	return ""
}

func isFalseValue(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return value == "false" || value == "0" || value == "off" || value == "no"
}

func isEnabledFlag(value, name string) bool {
	value = strings.TrimPrefix(value, "--")
	if value == name || value == name+":true" || value == name+"=true" || value == name+"=1" {
		return true
	}
	return strings.HasPrefix(value, name+"=") && !strings.HasSuffix(value, "=false") && !strings.HasSuffix(value, "=0")
}

func bypassIDs(args []string, kind string) []int {
	values := make([]int, 0)
	for index, arg := range args {
		lower := strings.ToLower(arg)
		for _, flag := range []string{"--exclude-" + kind, "--bypass-" + kind, "exclude-" + kind + "=", "bypass-" + kind + "="} {
			if strings.HasPrefix(lower, flag+"=") {
				values = append(values, parseIDList(strings.TrimPrefix(lower, flag+"="))...)
				break
			}
			if lower == flag && index+1 < len(args) {
				values = append(values, parseIDList(args[index+1])...)
				break
			}
		}
	}
	return values
}

func parseIDList(value string) []int {
	ids := make([]int, 0)
	for _, item := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ':' || r == ';' }) {
		if id, err := strconv.Atoi(strings.TrimSpace(item)); err == nil && id >= 0 {
			ids = append(ids, id)
		}
	}
	return ids
}

func containsInt(values []int, want int) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func processIdentity(path string) (int, int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, err
	}
	var uid, gid int
	var foundUID, foundGID bool
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || (fields[0] != "Uid:" && fields[0] != "Gid:") {
			continue
		}
		value, err := strconv.Atoi(fields[1])
		if err != nil {
			return 0, 0, err
		}
		if fields[0] == "Uid:" {
			uid = value
			foundUID = true
		} else {
			gid = value
			foundGID = true
		}
	}
	if !foundUID || !foundGID {
		return 0, 0, errors.New("process identity is unavailable")
	}
	return uid, gid, nil
}
