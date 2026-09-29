package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	coreModule                 = "github.com/BroLabel/brosettlement-mpc-core"
	tssModule                  = "github.com/bnb-chain/tss-lib/v3"
	tssReleaseVersion          = "v3.0.0"
	expectedCoreReleaseVersion = "v0.5.1-0.20260929122625-0619dc001f0f"
	ed25519Module              = "github.com/agl/ed25519"
	ed25519Replacement         = "github.com/binance-chain/edwards25519"
	ed25519ReplacementVersion  = "v0.0.0-20200305024217-f36fc4b53d43"
)

type modulePin struct {
	Path    string
	Version string
	Replace *modulePin
}

type moduleReplacement struct {
	Old modulePin
	New modulePin
}

type moduleManifest struct {
	Require []modulePin
	Replace []moduleReplacement
	Exclude []modulePin
}

func TestReleaseModuleGraph(t *testing.T) {
	root := filepath.Join("..", "..")
	manifest := moduleJSONCommand(t, root, "mod", "edit", "-json")
	graph := moduleJSONCommand(t, root, "list", "-m", "-json", "all")
	if err := verifyReleaseModuleGraph(manifest, graph, expectedCoreReleaseVersion); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyReleaseModuleGraph(t *testing.T) {
	tests := []struct {
		name      string
		change    func(*moduleManifest, *[]modulePin)
		wantError string
	}{
		{name: "exact release graph"},
		{name: "old Core", change: func(m *moduleManifest, g *[]modulePin) { m.Require[0].Version = "v0.4.0"; (*g)[0].Version = "v0.4.0" }, wantError: coreModule},
		{name: "missing Core requirement", change: func(m *moduleManifest, _ *[]modulePin) { m.Require = m.Require[1:] }, wantError: coreModule},
		{name: "missing resolved Core", change: func(_ *moduleManifest, g *[]modulePin) { *g = (*g)[1:] }, wantError: coreModule},
		{name: "other resolved Core", change: func(_ *moduleManifest, g *[]modulePin) { (*g)[0].Version = "v0.6.0" }, wantError: coreModule},
		{name: "Core local replacement", change: func(m *moduleManifest, _ *[]modulePin) {
			m.Replace = append(m.Replace, moduleReplacement{Old: modulePin{Path: coreModule}, New: modulePin{Path: "../mpc-core"}})
		}, wantError: coreModule},
		{name: "Core version replacement", change: func(m *moduleManifest, _ *[]modulePin) {
			m.Replace = append(m.Replace, moduleReplacement{Old: modulePin{Path: coreModule}, New: modulePin{Path: coreModule, Version: "v0.4.0"}})
		}, wantError: coreModule},
		{name: "resolved Core replacement", change: func(_ *moduleManifest, g *[]modulePin) { (*g)[0].Replace = &modulePin{Path: "../mpc-core"} }, wantError: coreModule},
		{name: "missing TSS", change: func(m *moduleManifest, _ *[]modulePin) { m.Require = m.Require[:1] }, wantError: tssModule},
		{name: "other TSS version", change: func(m *moduleManifest, _ *[]modulePin) { m.Require[1].Version = "v3.0.1" }, wantError: tssModule},
		{name: "other resolved TSS version", change: func(_ *moduleManifest, g *[]modulePin) { (*g)[1].Version = "v3.0.1" }, wantError: tssModule},
		{name: "TSS replacement", change: func(m *moduleManifest, _ *[]modulePin) {
			m.Replace = append(m.Replace, moduleReplacement{Old: modulePin{Path: tssModule}, New: modulePin{Path: "../tss-lib"}})
		}, wantError: tssModule},
		{name: "resolved TSS replacement", change: func(_ *moduleManifest, g *[]modulePin) { (*g)[1].Replace = &modulePin{Path: "../tss-lib"} }, wantError: tssModule},
		{name: "old v1 requirement", change: func(m *moduleManifest, _ *[]modulePin) {
			m.Require = append(m.Require, modulePin{Path: "github.com/bnb-chain/tss-lib", Version: "v1.5.0"})
		}, wantError: "github.com/bnb-chain/tss-lib v1.5.0"},
		{name: "old v1 resolved", change: func(_ *moduleManifest, g *[]modulePin) {
			*g = append(*g, modulePin{Path: "github.com/bnb-chain/tss-lib", Version: "v1.5.0"})
		}, wantError: "github.com/bnb-chain/tss-lib v1.5.0"},
		{name: "old v2 resolved", change: func(_ *moduleManifest, g *[]modulePin) {
			*g = append(*g, modulePin{Path: "github.com/bnb-chain/tss-lib/v2", Version: "v2.0.2"})
		}, wantError: "github.com/bnb-chain/tss-lib/v2 v2.0.2"},
		{name: "old upstream name", change: func(_ *moduleManifest, g *[]modulePin) {
			*g = append(*g, modulePin{Path: "github.com/binance-chain/tss-lib", Version: "v1.3.3"})
		}, wantError: "github.com/binance-chain/tss-lib"},
		{name: "missing replacement", change: func(m *moduleManifest, _ *[]modulePin) { m.Replace = nil }, wantError: ed25519Module},
		{name: "different replacement target", change: func(m *moduleManifest, _ *[]modulePin) { m.Replace[0].New.Path = "example.com/ed25519" }, wantError: "example.com/ed25519"},
		{name: "different replacement version", change: func(m *moduleManifest, _ *[]modulePin) { m.Replace[0].New.Version = "v0.0.1" }, wantError: "v0.0.1"},
		{name: "version scoped replacement", change: func(m *moduleManifest, _ *[]modulePin) { m.Replace[0].Old.Version = "v0.0.1" }, wantError: ed25519Module},
		{name: "unrelated replacement", change: func(m *moduleManifest, _ *[]modulePin) {
			m.Replace = append(m.Replace, moduleReplacement{Old: modulePin{Path: "example.com/unrelated"}, New: modulePin{Path: "../unrelated"}})
		}, wantError: "example.com/unrelated"},
		{name: "unrelated resolved replacement", change: func(_ *moduleManifest, g *[]modulePin) {
			*g = append(*g, modulePin{Path: "example.com/unrelated", Version: "v1.0.0", Replace: &modulePin{Path: "../unrelated"}})
		}, wantError: "example.com/unrelated"},
		{name: "duplicate replacement", change: func(m *moduleManifest, _ *[]modulePin) { m.Replace = append(m.Replace, m.Replace[0]) }, wantError: ed25519Module},
		{name: "missing resolved replacement", change: func(_ *moduleManifest, g *[]modulePin) { (*g)[2].Replace = nil }, wantError: ed25519Module},
		{name: "different resolved replacement", change: func(_ *moduleManifest, g *[]modulePin) { (*g)[2].Replace.Version = "v0.0.1" }, wantError: "v0.0.1"},
		{name: "excluded Core", change: func(m *moduleManifest, _ *[]modulePin) {
			m.Exclude = []modulePin{{Path: coreModule, Version: expectedCoreReleaseVersion}}
		}, wantError: coreModule},
		{name: "excluded TSS", change: func(m *moduleManifest, _ *[]modulePin) {
			m.Exclude = []modulePin{{Path: tssModule, Version: tssReleaseVersion}}
		}, wantError: tssModule},
		{name: "unrelated exclusion", change: func(m *moduleManifest, _ *[]modulePin) {
			m.Exclude = []modulePin{{Path: "example.com/unrelated", Version: "v1.0.0"}}
		}, wantError: "example.com/unrelated"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			manifest, graph := validReleaseModules()
			if tc.change != nil {
				tc.change(&manifest, &graph)
			}
			manifestJSON, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			var graphJSON []byte
			for _, module := range graph {
				raw, err := json.Marshal(module)
				if err != nil {
					t.Fatal(err)
				}
				graphJSON = append(graphJSON, raw...)
				graphJSON = append(graphJSON, '\n')
			}
			err = verifyReleaseModuleGraph(manifestJSON, graphJSON, expectedCoreReleaseVersion)
			if tc.wantError == "" {
				if err != nil {
					t.Fatalf("valid graph rejected: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("want error containing %q, got %v", tc.wantError, err)
			}
		})
	}
}

func TestModuleReplacementSyntax(t *testing.T) {
	for _, block := range []bool{false, true} {
		t.Run(fmt.Sprintf("block=%t", block), func(t *testing.T) {
			dir := t.TempDir()
			replacement := fmt.Sprintf("%s => %s %s", ed25519Module, ed25519Replacement, ed25519ReplacementVersion)
			if block {
				replacement = "replace (\n" + replacement + "\n)"
			} else {
				replacement = "replace " + replacement
			}
			manifest := fmt.Sprintf("module example.com/release-test\n\ngo 1.24.0\n\nrequire (\n%s %s\n%s %s\n)\n\n%s\n", coreModule, expectedCoreReleaseVersion, tssModule, tssReleaseVersion, replacement)
			if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(manifest), 0600); err != nil {
				t.Fatal(err)
			}
			var parsed moduleManifest
			if err := json.Unmarshal(moduleJSONCommand(t, dir, "mod", "edit", "-json"), &parsed); err != nil {
				t.Fatal(err)
			}
			if len(parsed.Replace) != 1 || !isAllowedReplacement(parsed.Replace[0]) {
				t.Fatalf("replacement parsed incorrectly: %+v", parsed.Replace)
			}
		})
	}
}

func validReleaseModules() (moduleManifest, []modulePin) {
	replacement := modulePin{Path: ed25519Replacement, Version: ed25519ReplacementVersion}
	manifest := moduleManifest{
		Require: []modulePin{{Path: coreModule, Version: expectedCoreReleaseVersion}, {Path: tssModule, Version: tssReleaseVersion}},
		Replace: []moduleReplacement{{Old: modulePin{Path: ed25519Module}, New: replacement}},
	}
	graph := []modulePin{{Path: coreModule, Version: expectedCoreReleaseVersion}, {Path: tssModule, Version: tssReleaseVersion}, {Path: ed25519Module, Version: "v0.0.0-20170116200512-5312a6153412", Replace: &replacement}}
	return manifest, graph
}

func moduleJSONCommand(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off")
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("go %s failed: %v", strings.Join(args, " "), err)
	}
	return output
}

func verifyReleaseModuleGraph(manifestJSON, graphJSON []byte, coreVersion string) error {
	var manifest moduleManifest
	if err := json.Unmarshal(manifestJSON, &manifest); err != nil {
		return fmt.Errorf("decode go.mod JSON: %w", err)
	}
	var graph []modulePin
	decoder := json.NewDecoder(strings.NewReader(string(graphJSON)))
	for {
		var module modulePin
		if err := decoder.Decode(&module); err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("decode resolved module JSON: %w", err)
		}
		graph = append(graph, module)
	}
	for _, exclusion := range manifest.Exclude {
		return fmt.Errorf("module exclusion is forbidden: %s %s", exclusion.Path, exclusion.Version)
	}
	for _, replacement := range manifest.Replace {
		if !isAllowedReplacement(replacement) {
			return fmt.Errorf("module replacement is forbidden: %s %s => %s %s", replacement.Old.Path, replacement.Old.Version, replacement.New.Path, replacement.New.Version)
		}
	}
	if len(manifest.Replace) != 1 {
		return fmt.Errorf("%s requires exactly one replacement to %s %s", ed25519Module, ed25519Replacement, ed25519ReplacementVersion)
	}
	for _, modules := range [][]modulePin{manifest.Require, graph} {
		for _, module := range modules {
			if isLegacyTSS(module.Path) {
				return fmt.Errorf("legacy TSS module is forbidden: %s %s", module.Path, module.Version)
			}
		}
		for _, required := range []modulePin{{Path: coreModule, Version: coreVersion}, {Path: tssModule, Version: tssReleaseVersion}} {
			count := 0
			for _, module := range modules {
				if module.Path == required.Path {
					count++
					if module.Version != required.Version {
						return fmt.Errorf("%s must be exactly %s; found %s", module.Path, required.Version, module.Version)
					}
				}
			}
			if count != 1 {
				return fmt.Errorf("%s %s must occur exactly once; found %d", required.Path, required.Version, count)
			}
		}
	}
	replacements := 0
	for _, module := range graph {
		if module.Replace == nil {
			continue
		}
		if !isAllowedReplacement(moduleReplacement{Old: modulePin{Path: module.Path}, New: *module.Replace}) {
			return fmt.Errorf("resolved module replacement is forbidden: %s %s => %s %s", module.Path, module.Version, module.Replace.Path, module.Replace.Version)
		}
		replacements++
	}
	if replacements != 1 {
		return fmt.Errorf("resolved %s requires exactly one replacement to %s %s; found %d", ed25519Module, ed25519Replacement, ed25519ReplacementVersion, replacements)
	}
	return nil
}

func isLegacyTSS(path string) bool {
	if path == tssModule {
		return false
	}
	for _, legacy := range []string{"github.com/bnb-chain/tss-lib", "github.com/binance-chain/tss-lib"} {
		if path == legacy || strings.HasPrefix(path, legacy+"/") {
			return true
		}
	}
	return false
}

func isAllowedReplacement(replacement moduleReplacement) bool {
	return replacement.Old.Path == ed25519Module && replacement.Old.Version == "" && replacement.New.Path == ed25519Replacement && replacement.New.Version == ed25519ReplacementVersion
}
