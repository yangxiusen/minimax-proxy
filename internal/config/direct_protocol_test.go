package config

import (
	"fmt"
	"path/filepath"
	"testing"
)

func TestLoadAllowsDirectProtocolsWithoutH3Profiles(t *testing.T) {
	cfg, err := Load(writeConfig(t, fmt.Sprintf("database:\n  path: %q\n", filepath.ToSlash(filepath.Join(t.TempDir(), "direct.db")))))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.GenerationProfiles) != 0 {
		t.Fatal("invented H3 profiles")
	}
}
