// Package project parses TLang project manifests and runtime grants.
//
// The version-1 manifest is strict JSON with this shape (all fields are
// required unless marked optional here):
//
//	{
//	  "schemaVersion": 1, "language": "1", "entry": "src/main.tlang",
//	  "target": {"os": ["linux"], "arch": ["amd64"]},
//	  "capabilities": {
//	    "env": ["APP_MODE"],
//	    "filesystem": [{"root": "data", "modes": ["read", "write"]}],
//	    "process": [{"executable": "git", "maxArgs": 32,
//	                 "maxOutputBytes": 1048576}],
//	    "network": {
//	      "connect": [{"protocol": "tcp", "host": "db.example",
//	                    "ports": [{"from": 5432, "to": 5432}],
//	                    "groups": ["production"]}],
//	      "listen": []
//	    },
//	    "lifecycle": {"signals": ["SIGINT", "SIGTERM"]}
//	  },
//	  "databases": {"primary": {"engine": "postgres", "config": "PRIMARY_DB"}},
//	  "limits": {"maxWorkers": 8, "maxDeadlineMs": 30000}
//	}
//
// Target OS and architecture are Go platform names. Filesystem roots are
// project-relative paths; modes are read, write, create, delete, and list.
// Process executable names are exact bare executable identities (never shell
// commands); maxArgs (at most 65536) and maxOutputBytes are optional positive
// upper bounds. Network
// protocols are tcp or udp, hosts are exact DNS names or IP addresses, ports
// are inclusive ranges, and groups are optional named scopes. Lifecycle
// signals are SIGINT and SIGTERM. Database engines are postgres and sqlite;
// config is a non-secret identifier, not a URL or credential. Limit fields are
// optional positive bounds no greater than these hard ceilings: workers 256,
// queue capacity 65536, deadline 86400000 ms, file bytes 1 GiB, process output
// 64 MiB, network connections 65536, and database connections 4096.
//
// The separate grants document has schemaVersion, manifestSha256, capabilities,
// databases, and limits. Its capability shape matches the manifest. Each named
// database grant has a URL field, the only manifest-adjacent location where a
// database URL or credential is accepted. The digest is lowercase SHA-256 of
// the exact manifest bytes. Grants must match the manifest's requested
// capabilities and database names; grant limits may tighten, but never raise,
// the manifest limits or package hard ceilings.
//
// Both document parsers reject unknown fields, duplicate object keys,
// unsupported versions, malformed JSON, oversized input, and invalid values.
// Errors intentionally do not include input bytes, field values, filesystem
// paths, database URLs, or decoder error text.
package project

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	// ManifestName is the standard project manifest filename.
	ManifestName = "tlang.json"
	// MaxDocumentBytes bounds parsing and file discovery to one mebibyte.
	MaxDocumentBytes = 1 << 20

	MaxWorkers             int64 = 256
	MaxQueueCapacity       int64 = 65536
	MaxDeadlineMs          int64 = 86400000
	MaxFileBytes           int64 = 1 << 30
	MaxProcessOutputBytes  int64 = 64 << 20
	MaxNetworkConnections  int64 = 65536
	MaxDatabaseConnections int64 = 4096
)

var (
	// ErrInvalidManifest marks invalid manifest bytes or fields.
	ErrInvalidManifest = errors.New("project: invalid manifest")
	// ErrInvalidGrants marks invalid grant bytes or fields.
	ErrInvalidGrants = errors.New("project: invalid grants")
	// ErrManifestNotFound means no tlang.json was found on the ancestor chain.
	ErrManifestNotFound = errors.New("project: manifest not found")
	// ErrAmbiguousManifest means multiple ancestor directories contain a manifest.
	ErrAmbiguousManifest = errors.New("project: ambiguous manifests")
	// ErrDiscovery means the explicit discovery path could not be inspected.
	ErrDiscovery = errors.New("project: could not discover manifest")
)

// Manifest is the parsed version-1 tlang.json document.
type Manifest struct {
	SchemaVersion int                 `json:"schemaVersion"`
	Language      string              `json:"language"`
	Entry         string              `json:"entry"`
	Target        Target              `json:"target"`
	Capabilities  Capabilities        `json:"capabilities"`
	Databases     map[string]Database `json:"databases"`
	Limits        Limits              `json:"limits"`
}

// Target restricts supported operating systems and architectures. Empty lists
// mean that the manifest does not impose a restriction for that dimension.
type Target struct {
	OS   []string `json:"os"`
	Arch []string `json:"arch"`
}

// Capabilities contains the program's explicit authority requests.
type Capabilities struct {
	Env        []string               `json:"env"`
	Filesystem []FilesystemCapability `json:"filesystem"`
	Process    []ProcessCapability    `json:"process"`
	Network    NetworkCapabilities    `json:"network"`
	Lifecycle  LifecycleCapability    `json:"lifecycle"`
}

// FilesystemCapability requests modes on a project-relative root.
type FilesystemCapability struct {
	Root  string   `json:"root"`
	Modes []string `json:"modes"`
}

// ProcessCapability requests execution of one exact executable identity.
// MaxArgs and MaxOutputBytes are optional, positive constraints.
type ProcessCapability struct {
	Executable     string `json:"executable"`
	MaxArgs        int64  `json:"maxArgs,omitempty"`
	MaxOutputBytes int64  `json:"maxOutputBytes,omitempty"`
}

// NetworkCapabilities separates outbound and inbound network requests.
type NetworkCapabilities struct {
	Connect []NetworkRule `json:"connect"`
	Listen  []NetworkRule `json:"listen"`
}

// NetworkRule requests a protocol and exact host or named group scope for a
// set of inclusive port ranges.
type NetworkRule struct {
	Protocol string      `json:"protocol"`
	Host     string      `json:"host,omitempty"`
	Ports    []PortRange `json:"ports"`
	Groups   []string    `json:"groups"`
}

// PortRange is an inclusive network port interval.
type PortRange struct {
	From int `json:"from"`
	To   int `json:"to"`
}

// LifecycleCapability requests the runtime's managed shutdown signals.
type LifecycleCapability struct {
	Signals []string `json:"signals"`
}

// Database describes a named database resource without credentials or URLs.
type Database struct {
	Engine string `json:"engine"`
	Config string `json:"config"`
}

// Limits contains optional requested upper bounds. Zero means unspecified.
type Limits struct {
	MaxWorkers             int64 `json:"maxWorkers,omitempty"`
	MaxQueueCapacity       int64 `json:"maxQueueCapacity,omitempty"`
	MaxDeadlineMs          int64 `json:"maxDeadlineMs,omitempty"`
	MaxFileBytes           int64 `json:"maxFileBytes,omitempty"`
	MaxProcessOutputBytes  int64 `json:"maxProcessOutputBytes,omitempty"`
	MaxNetworkConnections  int64 `json:"maxNetworkConnections,omitempty"`
	MaxDatabaseConnections int64 `json:"maxDatabaseConnections,omitempty"`
}

// Grants is a parsed runtime grants document. Database URLs may contain
// credentials; callers must handle this value as secret material.
type Grants struct {
	SchemaVersion  int                      `json:"schemaVersion"`
	ManifestSHA256 string                   `json:"manifestSha256"`
	Capabilities   Capabilities             `json:"capabilities"`
	Databases      map[string]DatabaseGrant `json:"databases"`
	Limits         Limits                   `json:"limits"`
}

// DatabaseGrant supplies a secret URL for a manifest-declared database.
type DatabaseGrant struct {
	URL string `json:"url"`
}

// Project is the discovered manifest and its absolute containing directory.
type Project struct {
	Root           string
	Manifest       *Manifest
	ManifestSHA256 string
}

// ParseManifest parses and validates a version-1 manifest.
func ParseManifest(data []byte) (*Manifest, error) {
	var manifest Manifest
	if strictDecode(data, &manifest) != nil || !validManifest(&manifest) {
		return nil, ErrInvalidManifest
	}
	normalizeCapabilities(&manifest.Capabilities)
	return &manifest, nil
}

// ParseGrants parses and validates a version-1 runtime grants document.
func ParseGrants(data []byte) (*Grants, error) {
	var grants Grants
	if strictDecode(data, &grants) != nil || !validGrants(&grants) {
		return nil, ErrInvalidGrants
	}
	normalizeCapabilities(&grants.Capabilities)
	return &grants, nil
}

// ManifestDigest returns the lowercase SHA-256 digest of the exact manifest
// bytes. Formatting changes therefore require a correspondingly updated grant.
func ManifestDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ValidateGrants verifies manifest binding, exact capability authorization,
// named database URLs, and requested limit ceilings. The supplied digest is
// normally Project.ManifestSHA256 or ManifestDigest of the parsed bytes.
func ValidateGrants(manifest *Manifest, grants *Grants, manifestSHA256 string) error {
	if manifest == nil || grants == nil || !validManifest(manifest) || !validGrants(grants) {
		return ErrInvalidGrants
	}
	if grants.ManifestSHA256 != manifestSHA256 || !validDigest(manifestSHA256) {
		return ErrInvalidGrants
	}
	if !sameCapabilities(manifest.Capabilities, grants.Capabilities) ||
		!sameDatabaseNames(manifest.Databases, grants.Databases) ||
		!validDatabaseURLs(manifest.Databases, grants.Databases) ||
		!grantLimitsWithinRequest(manifest.Limits, grants.Limits) {
		return ErrInvalidGrants
	}
	return nil
}

// ParseAndValidateGrants parses grants and validates them against the manifest.
func ParseAndValidateGrants(data []byte, manifest *Manifest, manifestSHA256 string) (*Grants, error) {
	grants, err := ParseGrants(data)
	if err != nil {
		return nil, err
	}
	if err := ValidateGrants(manifest, grants, manifestSHA256); err != nil {
		return nil, err
	}
	return grants, nil
}

// Discover searches the explicit entry or project path and its ancestors for
// tlang.json. If multiple ancestor directories contain manifests, discovery
// fails rather than choosing a root implicitly. A path that is not yet present
// is treated as an entry path, allowing callers to diagnose missing source
// files after project discovery.
func Discover(start string) (*Project, error) {
	if strings.TrimSpace(start) == "" {
		return nil, ErrDiscovery
	}
	abs, err := filepath.Abs(start)
	if err != nil {
		return nil, ErrDiscovery
	}
	abs = filepath.Clean(abs)
	info, statErr := os.Stat(abs)
	if statErr == nil && !info.IsDir() {
		abs = filepath.Dir(abs)
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return nil, ErrDiscovery
	} else if statErr != nil && filepath.Ext(abs) != "" {
		abs = filepath.Dir(abs)
	}

	type foundManifest struct {
		root    string
		data    []byte
		invalid bool
	}
	var found []foundManifest
	for dir := abs; ; dir = filepath.Dir(dir) {
		manifestPath := filepath.Join(dir, ManifestName)
		data, readErr := readBounded(manifestPath)
		if readErr == nil {
			found = append(found, foundManifest{root: dir, data: data})
		} else if errors.Is(readErr, ErrInvalidManifest) {
			found = append(found, foundManifest{root: dir, invalid: true})
		} else if !os.IsNotExist(readErr) {
			return nil, ErrDiscovery
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	if len(found) == 0 {
		return nil, ErrManifestNotFound
	}
	if len(found) != 1 {
		return nil, ErrAmbiguousManifest
	}
	if found[0].invalid {
		return nil, ErrInvalidManifest
	}
	manifest, err := ParseManifest(found[0].data)
	if err != nil {
		return nil, err
	}
	return &Project{
		Root:           found[0].root,
		Manifest:       manifest,
		ManifestSHA256: ManifestDigest(found[0].data),
	}, nil
}

// EntryPath returns the absolute path to the manifest entry.
func (p *Project) EntryPath() string {
	if p == nil || p.Manifest == nil {
		return ""
	}
	return filepath.Join(p.Root, filepath.FromSlash(p.Manifest.Entry))
}

func readBounded(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxDocumentBytes+1))
	if err != nil {
		return nil, errors.New("project: could not read manifest")
	}
	if len(data) > MaxDocumentBytes {
		return nil, ErrInvalidManifest
	}
	return data, nil
}
