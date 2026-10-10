package project

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validManifestJSON = `{
  "schemaVersion": 1,
  "language": "1",
  "entry": "src/main.tlang",
  "target": {"os": ["linux"], "arch": ["amd64"]},
  "capabilities": {
    "env": ["APP_MODE"],
    "filesystem": [{"root": "data", "modes": ["read", "write"]}],
    "process": [{"executable": "git", "maxArgs": 12, "maxOutputBytes": 4096}],
    "network": {
      "connect": [{"protocol": "tcp", "host": "db.example", "ports": [{"from": 5432, "to": 5432}], "groups": ["production"]}],
      "listen": []
    },
    "lifecycle": {"signals": ["SIGINT", "SIGTERM"]}
  },
  "databases": {"primary": {"engine": "postgres", "config": "PRIMARY_DB"}},
  "limits": {"maxWorkers": 8, "maxDeadlineMs": 30000}
}`

func TestParseManifestValidAndDefaults(t *testing.T) {
	manifest, err := ParseManifest([]byte(validManifestJSON))
	if err != nil {
		t.Fatalf("ParseManifest() error = %v", err)
	}
	if manifest.Entry != "src/main.tlang" || manifest.Language != "1" || manifest.SchemaVersion != 1 {
		t.Fatalf("parsed identity = version %d language %q entry %q", manifest.SchemaVersion, manifest.Language, manifest.Entry)
	}
	if len(manifest.Capabilities.Network.Connect) != 1 || manifest.Limits.MaxWorkers != 8 {
		t.Fatalf("manifest content not retained: %+v", manifest)
	}
	if manifest.Capabilities.Network.Listen == nil || manifest.Capabilities.Lifecycle.Signals == nil {
		t.Fatal("empty capability lists should normalize to non-nil lists")
	}
}

func TestParseManifestRejectsUnknownFieldsEverywhere(t *testing.T) {
	cases := map[string]string{
		"top level":  `"extra": true`,
		"target":     `"extra": true`,
		"filesystem": `"extra": true`,
		"network":    `"extra": true`,
		"database":   `"extra": true`,
		"limits":     `"extra": true`,
	}
	for name, insertion := range cases {
		t.Run(name, func(t *testing.T) {
			input := validManifestJSON
			switch name {
			case "top level":
				input = strings.Replace(input, `"language": "1",`, `"language": "1", `+insertion+`,`, 1)
			case "target":
				input = strings.Replace(input, `"target": {"os": ["linux"], "arch": ["amd64"]}`, `"target": {"os": ["linux"], "arch": ["amd64"], `+insertion+`}`, 1)
			case "filesystem":
				input = strings.Replace(input, `"root": "data", "modes": ["read", "write"]`, `"root": "data", "modes": ["read", "write"], `+insertion, 1)
			case "network":
				input = strings.Replace(input, `"connect": [`, `"extra": true, "connect": [`, 1)
			case "database":
				input = strings.Replace(input, `"engine": "postgres", "config": "PRIMARY_DB"`, `"engine": "postgres", "config": "PRIMARY_DB", `+insertion, 1)
			case "limits":
				input = strings.Replace(input, `"maxWorkers": 8`, `"maxWorkers": 8, `+insertion, 1)
			}
			if _, err := ParseManifest([]byte(input)); !errors.Is(err, ErrInvalidManifest) {
				t.Fatalf("ParseManifest() error = %v, want ErrInvalidManifest", err)
			}
		})
	}
}

func TestParseManifestRejectsVersionsLanguageAndSecrets(t *testing.T) {
	cases := []struct {
		name string
		from string
		to   string
	}{
		{name: "schema version", from: `"schemaVersion": 1`, to: `"schemaVersion": 2`},
		{name: "non-integer version", from: `"schemaVersion": 1`, to: `"schemaVersion": 1.0`},
		{name: "language version", from: `"language": "1"`, to: `"language": "2"`},
		{name: "manifest URL secret", from: `"config": "PRIMARY_DB"`, to: `"config": "postgres://user:secret@db/app"`},
		{name: "credential field", from: `"config": "PRIMARY_DB"`, to: `"config": "PRIMARY_DB", "password": "swordfish"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := strings.Replace(validManifestJSON, tc.from, tc.to, 1)
			if _, err := ParseManifest([]byte(input)); !errors.Is(err, ErrInvalidManifest) {
				t.Fatalf("ParseManifest() error = %v, want ErrInvalidManifest", err)
			}
		})
	}
}

func TestParseManifestRejectsDuplicateKeys(t *testing.T) {
	inputs := []string{
		strings.Replace(validManifestJSON, `"schemaVersion": 1,`, `"schemaVersion": 1, "schemaVersion": 1,`, 1),
		strings.Replace(validManifestJSON, `"os": ["linux"]`, `"os": ["linux"], "os": ["windows"]`, 1),
		strings.Replace(validManifestJSON, `"engine": "postgres"`, `"engine": "postgres", "engine": "sqlite"`, 1),
	}
	for index, input := range inputs {
		if _, err := ParseManifest([]byte(input)); !errors.Is(err, ErrInvalidManifest) {
			t.Errorf("case %d: ParseManifest() error = %v, want ErrInvalidManifest", index, err)
		}
	}
}

func TestParseGrantsRejectsDuplicateNestedKeys(t *testing.T) {
	base := grantsJSON(strings.Repeat("b", 64))
	input := strings.Replace(base, `"url":"postgres://user:password@db.example/app"`, `"url":"postgres://user:password@db.example/app","url":"postgres://other/app"`, 1)
	if _, err := ParseGrants([]byte(input)); !errors.Is(err, ErrInvalidGrants) {
		t.Fatalf("duplicate nested grant key error = %v", err)
	}
}

func TestParseManifestRejectsMalformedAndOversizedDocuments(t *testing.T) {
	for _, input := range [][]byte{
		[]byte("{"),
		[]byte("null"),
		[]byte(validManifestJSON + ` {}`),
		[]byte(strings.Replace(validManifestJSON, `"env": ["APP_MODE"]`, `"env": null`, 1)),
		make([]byte, MaxDocumentBytes+1),
	} {
		if _, err := ParseManifest(input); !errors.Is(err, ErrInvalidManifest) {
			t.Errorf("ParseManifest(%d bytes) error = %v, want ErrInvalidManifest", len(input), err)
		}
	}
}

func TestParseManifestRejectsInvalidPathsAndRanges(t *testing.T) {
	changes := map[string]string{
		"parent traversal":   `"entry": "../main.tlang"`,
		"absolute path":      `"entry": "/tmp/main.tlang"`,
		"windows path":       `"entry": "..\\main.tlang"`,
		"dot segment":        `"entry": "src/../main.tlang"`,
		"wrong extension":    `"entry": "main.go"`,
		"filesystem escape":  `"root": "../outside"`,
		"invalid port":       `"from": 5432, "to": 65536`,
		"empty authority":    `"host": "", "ports": [{"from": 5432, "to": 5432}], "groups": []`,
		"unsupported OS":     `"os": ["madeup"], "arch": ["amd64"]`,
		"limit over ceiling": `"maxWorkers": 257`,
	}
	for name, invalidFragment := range changes {
		t.Run(name, func(t *testing.T) {
			input := validManifestJSON
			switch name {
			case "parent traversal", "absolute path", "windows path", "dot segment", "wrong extension":
				input = replaceEntry(input, invalidFragment)
			case "filesystem escape":
				input = strings.Replace(input, `"root": "data"`, invalidFragment, 1)
			case "invalid port":
				input = strings.Replace(input, `"from": 5432, "to": 5432`, invalidFragment, 1)
			case "empty authority":
				input = strings.Replace(input, `"host": "db.example", "ports": [{"from": 5432, "to": 5432}], "groups": ["production"]`, invalidFragment, 1)
			case "unsupported OS":
				input = strings.Replace(input, `"os": ["linux"], "arch": ["amd64"]`, invalidFragment, 1)
			case "limit over ceiling":
				input = strings.Replace(input, `"maxWorkers": 8`, invalidFragment, 1)
			}
			if _, err := ParseManifest([]byte(input)); !errors.Is(err, ErrInvalidManifest) {
				t.Fatalf("ParseManifest() error = %v, want ErrInvalidManifest", err)
			}
		})
	}
}

func replaceEntry(input, fragment string) string {
	return strings.Replace(input, `"entry": "src/main.tlang"`, fragment, 1)
}

func TestErrorsRedactInvalidDocumentContents(t *testing.T) {
	secret := `postgres://user:super-secret@example.test/app`
	invalidManifest := strings.Replace(validManifestJSON, `"config": "PRIMARY_DB"`, `"config": "`+secret+`"`, 1)
	_, err := ParseManifest([]byte(invalidManifest))
	if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "example.test") {
		t.Fatalf("manifest error leaked document content: %v", err)
	}
	invalidGrants := `{"schemaVersion":1,"manifestSha256":"` + strings.Repeat("0", 64) + `","capabilities":{},"databases":{"primary":{"url":"` + secret + `"}},"limits":{}}`
	_, err = ParseGrants([]byte(invalidGrants))
	if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "example.test") {
		t.Fatalf("grants error leaked secret material: %v", err)
	}
}

func TestDiscoverFromEntryAndDirectory(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, validManifestJSON)
	entry := filepath.Join(root, "src", "main.tlang")
	if err := os.MkdirAll(filepath.Dir(entry), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, start := range []string{entry, filepath.Dir(entry), root} {
		project, err := Discover(start)
		if err != nil {
			t.Fatalf("Discover(%q): %v", start, err)
		}
		if project.Root != root || project.EntryPath() != entry || project.ManifestSHA256 != ManifestDigest([]byte(validManifestJSON)) {
			t.Fatalf("Discover(%q) = %+v, entry path %q", start, project, project.EntryPath())
		}
	}
}

func TestDiscoverRejectsMissingAmbiguousAndInvalidManifests(t *testing.T) {
	root := t.TempDir()
	if _, err := Discover(filepath.Join(root, "src", "main.tlang")); !errors.Is(err, ErrManifestNotFound) {
		t.Fatalf("Discover without manifest error = %v", err)
	}
	writeManifest(t, root, validManifestJSON)
	child := filepath.Join(root, "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, child, validManifestJSON)
	if _, err := Discover(filepath.Join(child, "main.tlang")); !errors.Is(err, ErrAmbiguousManifest) {
		t.Fatalf("ambiguous Discover() error = %v", err)
	}
	if err := os.Remove(filepath.Join(root, ManifestName)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ManifestName), []byte(`{"schemaVersion":4}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Discover(root); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("invalid discovered manifest error = %v", err)
	}
}

func TestDiscoverAmbiguityPrecedesManifestValidation(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, `not-json`)
	child := filepath.Join(root, "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, child, validManifestJSON)
	if _, err := Discover(child); !errors.Is(err, ErrAmbiguousManifest) {
		t.Fatalf("Discover() error = %v, want ambiguity regardless of malformed ancestor", err)
	}
}

func TestDiscoverRedactsManifestPathOnFailure(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, `secret-content-that-must-not-appear`)
	_, err := Discover(root)
	if !errors.Is(err, ErrInvalidManifest) || strings.Contains(err.Error(), root) || strings.Contains(err.Error(), "secret-content") {
		t.Fatalf("Discover() error should be redacted: %v", err)
	}
}

func TestParseAndValidateGrantsBindsManifestAndAllowsDBSecretsOnlyThere(t *testing.T) {
	manifest, err := ParseManifest([]byte(validManifestJSON))
	if err != nil {
		t.Fatal(err)
	}
	digest := ManifestDigest([]byte(validManifestJSON))
	grantsJSON := grantsJSON(digest)
	grants, err := ParseAndValidateGrants([]byte(grantsJSON), manifest, digest)
	if err != nil {
		t.Fatalf("ParseAndValidateGrants() error = %v", err)
	}
	if grants.Databases["primary"].URL != "postgres://user:password@db.example/app" {
		t.Fatal("database URL secret not preserved in grants")
	}
	if _, err := ParseAndValidateGrants([]byte(grantsJSON), manifest, strings.Repeat("0", 64)); !errors.Is(err, ErrInvalidGrants) {
		t.Fatalf("digest mismatch error = %v, want ErrInvalidGrants", err)
	}
}

func TestParseGrantsRejectsUnknownDuplicateMalformedAndUnsupported(t *testing.T) {
	digest := strings.Repeat("a", 64)
	base := grantsJSON(digest)
	cases := map[string]string{
		"unknown top-level":      strings.Replace(base, `"schemaVersion":1,`, `"schemaVersion":1,"extra":true,`, 1),
		"unknown database field": strings.Replace(base, `/app"`, `/app","password":"leak"`, 1),
		"duplicate schema":       strings.Replace(base, `"schemaVersion":1,`, `"schemaVersion":1,"schemaVersion":1,`, 1),
		"version":                strings.Replace(base, `"schemaVersion":1`, `"schemaVersion":2`, 1),
		"malformed":              `{"schemaVersion":`,
		"url scheme":             strings.Replace(base, `postgres://user:password@db.example/app`, `https://db.example/app`, 1),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseGrants([]byte(input)); !errors.Is(err, ErrInvalidGrants) {
				t.Fatalf("ParseGrants() error = %v, want ErrInvalidGrants", err)
			}
		})
	}
	if _, err := ParseGrants(make([]byte, MaxDocumentBytes+1)); !errors.Is(err, ErrInvalidGrants) {
		t.Fatalf("oversized ParseGrants() error = %v", err)
	}
}

func TestValidateGrantsRejectsEscalationAndLimitIncrease(t *testing.T) {
	manifest, err := ParseManifest([]byte(validManifestJSON))
	if err != nil {
		t.Fatal(err)
	}
	digest := ManifestDigest([]byte(validManifestJSON))
	mutations := map[string]func(*Grants){
		"extra environment name": func(g *Grants) { g.Capabilities.Env = append(g.Capabilities.Env, "SECRET") },
		"extra database":         func(g *Grants) { g.Databases["other"] = DatabaseGrant{URL: "postgres://host/other"} },
		"limit increase":         func(g *Grants) { g.Limits.MaxWorkers = 9 },
		"unbounded request":      func(g *Grants) { g.Limits.MaxWorkers = 0 },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			grants, parseErr := ParseGrants([]byte(grantsJSON(digest)))
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			mutate(grants)
			if err := ValidateGrants(manifest, grants, digest); !errors.Is(err, ErrInvalidGrants) {
				t.Fatalf("ValidateGrants() error = %v, want ErrInvalidGrants", err)
			}
		})
	}
}

func TestGrantProcessConstraintsMayOnlyTightenRequest(t *testing.T) {
	manifest, err := ParseManifest([]byte(validManifestJSON))
	if err != nil {
		t.Fatal(err)
	}
	digest := ManifestDigest([]byte(validManifestJSON))
	grants, err := ParseGrants([]byte(grantsJSON(digest)))
	if err != nil {
		t.Fatal(err)
	}
	grants.Capabilities.Process[0].MaxArgs = 8
	grants.Capabilities.Process[0].MaxOutputBytes = 1024
	if err := ValidateGrants(manifest, grants, digest); err != nil {
		t.Fatalf("narrower process constraints rejected: %v", err)
	}
	grants.Capabilities.Process[0].MaxArgs = 13
	if err := ValidateGrants(manifest, grants, digest); !errors.Is(err, ErrInvalidGrants) {
		t.Fatalf("wider process constraint error = %v", err)
	}
}

func grantsJSON(digest string) string {
	capabilities := strings.TrimSpace(strings.SplitN(validManifestJSON, `"capabilities": `, 2)[1])
	capabilities = strings.SplitN(capabilities, `,
  "databases":`, 2)[0]
	var value any
	if err := json.Unmarshal([]byte(capabilities), &value); err != nil {
		panic(err)
	}
	encodedCapabilities, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return `{"schemaVersion":1,"manifestSha256":"` + digest + `","capabilities":` + string(encodedCapabilities) +
		`,"databases":{"primary":{"url":"postgres://user:password@db.example/app"}},"limits":{"maxWorkers":8,"maxDeadlineMs":30000}}`
}

func writeManifest(t *testing.T, directory, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, ManifestName), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
