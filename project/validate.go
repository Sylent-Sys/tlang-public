package project

import (
	"net"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,127}$`)

func validManifest(manifest *Manifest) bool {
	if manifest == nil || manifest.SchemaVersion != 1 || manifest.Language != "1" ||
		!validRelativePath(manifest.Entry, true) ||
		!validTarget(manifest.Target) || !validCapabilities(manifest.Capabilities) ||
		!validDatabases(manifest.Databases) || !validLimits(manifest.Limits) {
		return false
	}
	return true
}

func validGrants(grants *Grants) bool {
	if grants == nil || grants.SchemaVersion != 1 || !validDigest(grants.ManifestSHA256) ||
		!validCapabilities(grants.Capabilities) || !validGrantDatabases(grants.Databases) ||
		!validLimits(grants.Limits) {
		return false
	}
	return true
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func validTarget(target Target) bool {
	return uniqueStrings(target.OS, validOS) && uniqueStrings(target.Arch, validArch)
}

func validOS(value string) bool {
	for _, item := range strings.Split(runtime.GOOS+" aix android darwin dragonfly freebsd illumos ios js linux netbsd openbsd plan9 solaris wasip1 windows zos", " ") {
		if value == item {
			return true
		}
	}
	return false
}

func validArch(value string) bool {
	for _, item := range strings.Split("386 amd64 arm arm64 loong64 mips mipsle mips64 mips64le ppc64 ppc64le riscv64 s390x sparc64 wasm", " ") {
		if value == item {
			return true
		}
	}
	return false
}

func validCapabilities(capabilities Capabilities) bool {
	if !uniqueStrings(capabilities.Env, validIdentifier) || !uniqueStrings(capabilities.Lifecycle.Signals, validSignal) {
		return false
	}
	filesystemRoots := make(map[string]bool, len(capabilities.Filesystem))
	for _, filesystem := range capabilities.Filesystem {
		if !validRelativePath(filesystem.Root, false) || !uniqueStrings(filesystem.Modes, validFilesystemMode) || filesystemRoots[filesystem.Root] {
			return false
		}
		filesystemRoots[filesystem.Root] = true
	}
	executables := make(map[string]bool, len(capabilities.Process))
	for _, process := range capabilities.Process {
		if !validExecutable(process.Executable) ||
			!validOptionalBound(process.MaxArgs, 65536) ||
			!validOptionalBound(process.MaxOutputBytes, MaxProcessOutputBytes) || executables[process.Executable] {
			return false
		}
		executables[process.Executable] = true
	}
	for _, rule := range append(append([]NetworkRule(nil), capabilities.Network.Connect...), capabilities.Network.Listen...) {
		if (rule.Protocol != "tcp" && rule.Protocol != "udp") || !validHost(rule.Host) ||
			!uniqueStrings(rule.Groups, validIdentifier) || len(rule.Ports) == 0 {
			return false
		}
		ports := make(map[PortRange]bool, len(rule.Ports))
		for _, port := range rule.Ports {
			if port.From < 1 || port.From > port.To || port.To > 65535 {
				return false
			}
			if ports[port] {
				return false
			}
			ports[port] = true
		}
		if rule.Host == "" && len(rule.Groups) == 0 {
			return false
		}
	}
	return true
}

func validHost(host string) bool {
	if host == "" {
		return true
	}
	if net.ParseIP(host) != nil {
		return true
	}
	if len(host) > 253 || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") || strings.Contains(host, "..") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

func validFilesystemMode(value string) bool {
	switch value {
	case "read", "write", "create", "delete", "list":
		return true
	default:
		return false
	}
}

func validSignal(value string) bool { return value == "SIGINT" || value == "SIGTERM" }

func validIdentifier(value string) bool { return identifierPattern.MatchString(value) }

func validExecutable(value string) bool {
	return value != "" && filepath.Base(value) == value && !strings.ContainsAny(value, `/\`) && validIdentifier(value)
}

func validRelativePath(value string, source bool) bool {
	if value == "." && !source {
		return true
	}
	if value == "" || strings.ContainsAny(value, `\:`) || strings.ContainsRune(value, '\x00') || strings.HasPrefix(value, "/") ||
		filepath.IsAbs(value) || path.IsAbs(value) || path.Clean(value) != value ||
		value == ".." || strings.HasPrefix(value, "../") {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	if source {
		ext := path.Ext(value)
		return ext == ".tlang" || ext == ".ts"
	}
	return true
}

func validDatabases(databases map[string]Database) bool {
	for name, database := range databases {
		if !validIdentifier(name) || !validIdentifier(database.Config) ||
			(database.Engine != "postgres" && database.Engine != "sqlite") {
			return false
		}
	}
	return true
}

func validGrantDatabases(databases map[string]DatabaseGrant) bool {
	for name, grant := range databases {
		if !validIdentifier(name) || !validDatabaseURL(grant.URL) {
			return false
		}
	}
	return true
}

func validDatabaseURLs(requests map[string]Database, grants map[string]DatabaseGrant) bool {
	for name, request := range requests {
		grant, ok := grants[name]
		if !ok {
			return false
		}
		parsed, err := url.Parse(grant.URL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			if request.Engine != "sqlite" || err != nil ||
				(parsed.Scheme != "file" && parsed.Scheme != "sqlite") || parsed.Path == "" {
				return false
			}
		}
		if request.Engine == "postgres" && parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
			return false
		}
		if request.Engine == "sqlite" && parsed.Scheme != "file" && parsed.Scheme != "sqlite" {
			return false
		}
	}
	return true
}

func validDatabaseURL(value string) bool {
	if strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\x00") {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" {
		return false
	}
	switch parsed.Scheme {
	case "postgres", "postgresql":
		return parsed.Host != ""
	case "file", "sqlite":
		return parsed.Path != ""
	default:
		return false
	}
}

func validLimits(limits Limits) bool {
	return validOptionalBound(limits.MaxWorkers, MaxWorkers) &&
		validOptionalBound(limits.MaxQueueCapacity, MaxQueueCapacity) &&
		validOptionalBound(limits.MaxDeadlineMs, MaxDeadlineMs) &&
		validOptionalBound(limits.MaxFileBytes, MaxFileBytes) &&
		validOptionalBound(limits.MaxProcessOutputBytes, MaxProcessOutputBytes) &&
		validOptionalBound(limits.MaxNetworkConnections, MaxNetworkConnections) &&
		validOptionalBound(limits.MaxDatabaseConnections, MaxDatabaseConnections)
}

func validOptionalBound(value, maximum int64) bool { return value >= 0 && value <= maximum }

func uniqueStrings(values []string, valid func(string) bool) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !valid(value) {
			return false
		}
		if _, exists := seen[value]; exists {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func normalizeCapabilities(capabilities *Capabilities) {
	if capabilities.Env == nil {
		capabilities.Env = []string{}
	}
	if capabilities.Filesystem == nil {
		capabilities.Filesystem = []FilesystemCapability{}
	}
	if capabilities.Process == nil {
		capabilities.Process = []ProcessCapability{}
	}
	if capabilities.Network.Connect == nil {
		capabilities.Network.Connect = []NetworkRule{}
	}
	if capabilities.Network.Listen == nil {
		capabilities.Network.Listen = []NetworkRule{}
	}
	if capabilities.Lifecycle.Signals == nil {
		capabilities.Lifecycle.Signals = []string{}
	}
	for i := range capabilities.Filesystem {
		if capabilities.Filesystem[i].Modes == nil {
			capabilities.Filesystem[i].Modes = []string{}
		}
	}
	for _, rules := range [][]NetworkRule{capabilities.Network.Connect, capabilities.Network.Listen} {
		for i := range rules {
			if rules[i].Ports == nil {
				rules[i].Ports = []PortRange{}
			}
			if rules[i].Groups == nil {
				rules[i].Groups = []string{}
			}
		}
	}
}

func sameDatabaseNames(requests map[string]Database, grants map[string]DatabaseGrant) bool {
	if len(requests) != len(grants) {
		return false
	}
	for name := range requests {
		if _, exists := grants[name]; !exists {
			return false
		}
	}
	return true
}

func grantLimitsWithinRequest(request, grants Limits) bool {
	requestValues := []int64{request.MaxWorkers, request.MaxQueueCapacity, request.MaxDeadlineMs, request.MaxFileBytes, request.MaxProcessOutputBytes, request.MaxNetworkConnections, request.MaxDatabaseConnections}
	grantValues := []int64{grants.MaxWorkers, grants.MaxQueueCapacity, grants.MaxDeadlineMs, grants.MaxFileBytes, grants.MaxProcessOutputBytes, grants.MaxNetworkConnections, grants.MaxDatabaseConnections}
	for i, grant := range grantValues {
		if requestValues[i] != 0 && (grant == 0 || grant > requestValues[i]) {
			return false
		}
	}
	return true
}

func sameCapabilities(request, grant Capabilities) bool {
	return sameStringSet(request.Env, grant.Env) &&
		sameFilesystem(request.Filesystem, grant.Filesystem) &&
		sameProcesses(request.Process, grant.Process) &&
		sameNetworkRules(request.Network.Connect, grant.Network.Connect) &&
		sameNetworkRules(request.Network.Listen, grant.Network.Listen) &&
		sameStringSet(request.Lifecycle.Signals, grant.Lifecycle.Signals)
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	left, right := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(left)
	sort.Strings(right)
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func sameFilesystem(a, b []FilesystemCapability) bool {
	if len(a) != len(b) {
		return false
	}
	used := make([]bool, len(b))
	for i := range a {
		match := false
		for j := range b {
			if !used[j] && a[i].Root == b[j].Root && sameStringSet(a[i].Modes, b[j].Modes) {
				match = true
				used[j] = true
				break
			}
		}
		if !match {
			return false
		}
	}
	return true
}

func sameProcesses(a, b []ProcessCapability) bool {
	if len(a) != len(b) {
		return false
	}
	used := make([]bool, len(b))
	for i := range a {
		match := false
		for j := range b {
			if !used[j] && a[i].Executable == b[j].Executable &&
				grantBoundWithinRequest(a[i].MaxArgs, b[j].MaxArgs) &&
				grantBoundWithinRequest(a[i].MaxOutputBytes, b[j].MaxOutputBytes) {
				match = true
				used[j] = true
				break
			}
		}
		if !match {
			return false
		}
	}
	return true
}

func grantBoundWithinRequest(request, grant int64) bool {
	return request == 0 || grant > 0 && grant <= request
}

func sameNetworkRules(a, b []NetworkRule) bool {
	if len(a) != len(b) {
		return false
	}
	used := make([]bool, len(b))
	for i := range a {
		match := false
		for j := range b {
			if !used[j] && a[i].Protocol == b[j].Protocol && a[i].Host == b[j].Host &&
				sameStringSet(a[i].Groups, b[j].Groups) && samePortSet(a[i].Ports, b[j].Ports) {
				match = true
				used[j] = true
				break
			}
		}
		if !match {
			return false
		}
	}
	return true
}

func samePortSet(a, b []PortRange) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[PortRange]bool, len(a))
	for _, port := range a {
		seen[port] = true
	}
	for _, port := range b {
		if !seen[port] {
			return false
		}
	}
	return true
}
