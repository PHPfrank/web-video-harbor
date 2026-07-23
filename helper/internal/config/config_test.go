package config

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultPathUsesHome(t *testing.T) {
	home := privateTempDir(t)
	t.Setenv("HOME", home)

	got, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath() error = %v", err)
	}
	want := filepath.Join(home, "Library", "Application Support", "网页视频下载器", "config.json")
	if got != want {
		t.Fatalf("DefaultPath() = %q, want %q", got, want)
	}
}

func TestLoadFirstRunCreatesSecureDefaults(t *testing.T) {
	home := privateTempDir(t)
	t.Setenv("HOME", home)
	path := filepath.Join(home, "Library", "Application Support", "网页视频下载器", "config.json")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Address != DefaultAddress {
		t.Errorf("Address = %q, want %q", cfg.Address, DefaultAddress)
	}
	wantDownloadDir := filepath.Join(home, "Downloads", "网页视频下载器")
	if cfg.DownloadDir != wantDownloadDir {
		t.Errorf("DownloadDir = %q, want %q", cfg.DownloadDir, wantDownloadDir)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(cfg.Token)
	if err != nil {
		t.Fatalf("Token is not unpadded URL-safe base64: %v", err)
	}
	if len(decoded) != 32 {
		t.Fatalf("decoded token length = %d, want 32", len(decoded))
	}

	assertMode(t, path, 0o600)
	assertMode(t, filepath.Dir(path), 0o700)
	info, err := os.Stat(wantDownloadDir)
	if err != nil {
		t.Fatalf("stat download dir: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("download path is not a directory")
	}
}

func TestLoadPreservesExistingToken(t *testing.T) {
	home := privateTempDir(t)
	t.Setenv("HOME", home)
	path := filepath.Join(home, "app", "config.json")

	first, err := Load(path)
	if err != nil {
		t.Fatalf("first Load() error = %v", err)
	}
	second, err := Load(path)
	if err != nil {
		t.Fatalf("second Load() error = %v", err)
	}
	if second.Token != first.Token {
		t.Fatalf("token changed across loads")
	}
}

func TestLoadFirstRunRejectsInsecureExistingConfigParent(t *testing.T) {
	home := privateTempDir(t)
	t.Setenv("HOME", home)
	parent := filepath.Join(home, "shared")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "config.json")

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "config parent permissions") {
		t.Fatalf("Load() error = %v, want config parent permissions error", err)
	}
	if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
		t.Fatalf("config was created in insecure parent: %v", statErr)
	}
}

func TestLoadFirstRunAcceptsExisting0700ConfigParent(t *testing.T) {
	home := privateTempDir(t)
	t.Setenv("HOME", home)
	parent := filepath.Join(home, "private")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(filepath.Join(parent, "config.json")); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadExistingRejectsInsecureOrSymlinkConfigParent(t *testing.T) {
	t.Run("permissions", func(t *testing.T) {
		home := privateTempDir(t)
		t.Setenv("HOME", home)
		parent := filepath.Join(home, "app")
		path := filepath.Join(parent, "config.json")
		writeConfig(t, path, Config{Address: DefaultAddress, Token: testToken(8), DownloadDir: filepath.Join(home, "downloads")}, 0o600)
		if err := os.Chmod(parent, 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := Load(path)
		if err == nil || !strings.Contains(err.Error(), "config parent permissions") {
			t.Fatalf("Load() error = %v", err)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		home := privateTempDir(t)
		t.Setenv("HOME", home)
		realParent := filepath.Join(home, "real")
		realPath := filepath.Join(realParent, "config.json")
		writeConfig(t, realPath, Config{Address: DefaultAddress, Token: testToken(9), DownloadDir: filepath.Join(home, "downloads")}, 0o600)
		linkParent := filepath.Join(home, "linked")
		if err := os.Symlink(realParent, linkParent); err != nil {
			t.Fatal(err)
		}
		_, err := Load(filepath.Join(linkParent, "config.json"))
		if err == nil || !strings.Contains(err.Error(), "config parent") || !strings.Contains(err.Error(), "symbolic link") {
			t.Fatalf("Load() error = %v", err)
		}
	})
}

func TestLoadUsesConfiguredAddressAndDownloadDirectory(t *testing.T) {
	home := privateTempDir(t)
	t.Setenv("HOME", home)
	path := filepath.Join(home, "app", "config.json")
	downloadDir := filepath.Join(home, "chosen", "videos")
	want := Config{
		Address:     "127.0.0.1:18080",
		Token:       testToken(7),
		DownloadDir: downloadDir,
	}
	writeConfig(t, path, want, 0o600)

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got != want {
		t.Fatalf("Load() = %#v, want %#v", got, want)
	}
	info, err := os.Stat(downloadDir)
	if err != nil {
		t.Fatalf("stat configured download dir: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("configured download path is not a directory")
	}
}

func TestLoadRejectsUnsafeAddresses(t *testing.T) {
	tests := []string{
		"0.0.0.0:17432",
		"192.168.1.2:17432",
		"example.com:17432",
		":17432",
		"127.0.0.1",
		"127.0.0.1:0",
		"127.0.0.1:not-a-port",
	}
	for _, address := range tests {
		t.Run(address, func(t *testing.T) {
			home := privateTempDir(t)
			t.Setenv("HOME", home)
			path := filepath.Join(home, "config.json")
			writeConfig(t, path, Config{
				Address:     address,
				Token:       testToken(1),
				DownloadDir: filepath.Join(home, "downloads"),
			}, 0o600)

			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), "loopback address") {
				t.Fatalf("Load() error = %v, want clear loopback address error", err)
			}
		})
	}
}

func TestLoadAcceptsIPv6Loopback(t *testing.T) {
	home := privateTempDir(t)
	t.Setenv("HOME", home)
	path := filepath.Join(home, "config.json")
	writeConfig(t, path, Config{
		Address:     "[::1]:17432",
		Token:       testToken(2),
		DownloadDir: filepath.Join(home, "downloads"),
	}, 0o600)

	if _, err := Load(path); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadRefusesConfigSymlink(t *testing.T) {
	home := privateTempDir(t)
	t.Setenv("HOME", home)
	target := filepath.Join(home, "target.json")
	writeConfig(t, target, Config{
		Address:     DefaultAddress,
		Token:       testToken(3),
		DownloadDir: filepath.Join(home, "downloads"),
	}, 0o600)
	link := filepath.Join(home, "config.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	_, err := Load(link)
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("Load() error = %v, want symbolic link error", err)
	}
}

func TestLoadRejectsMalformedConfig(t *testing.T) {
	home := privateTempDir(t)
	t.Setenv("HOME", home)
	path := filepath.Join(home, "config.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write malformed config: %v", err)
	}

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "decode config") {
		t.Fatalf("Load() error = %v, want decode config error", err)
	}
}

func TestLoadRejectsOversizedConfig(t *testing.T) {
	home := privateTempDir(t)
	t.Setenv("HOME", home)
	path := filepath.Join(home, "config.json")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", maxConfigSize+1)), 0o600); err != nil {
		t.Fatalf("write oversized config: %v", err)
	}

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("Load() error = %v, want too large error", err)
	}
}

func TestLoadRejectsInsecureConfigPermissions(t *testing.T) {
	home := privateTempDir(t)
	t.Setenv("HOME", home)
	path := filepath.Join(home, "config.json")
	writeConfig(t, path, Config{
		Address:     DefaultAddress,
		Token:       testToken(4),
		DownloadDir: filepath.Join(home, "downloads"),
	}, 0o644)

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "permissions") {
		t.Fatalf("Load() error = %v, want permissions error", err)
	}
}

func TestLoadRejectsSymlinkDownloadDirectory(t *testing.T) {
	home := privateTempDir(t)
	t.Setenv("HOME", home)
	realDir := filepath.Join(home, "real-downloads")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatalf("create real download dir: %v", err)
	}
	link := filepath.Join(home, "downloads")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatalf("create download symlink: %v", err)
	}
	path := filepath.Join(home, "config.json")
	writeConfig(t, path, Config{
		Address:     DefaultAddress,
		Token:       testToken(5),
		DownloadDir: link,
	}, 0o600)

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "download directory") || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("Load() error = %v, want download directory symbolic link error", err)
	}
}

func TestLoadRejectsRelativeDownloadDirectory(t *testing.T) {
	home := privateTempDir(t)
	t.Setenv("HOME", home)
	path := filepath.Join(home, "config.json")
	writeConfig(t, path, Config{
		Address:     DefaultAddress,
		Token:       testToken(6),
		DownloadDir: "relative/downloads",
	}, 0o600)

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("Load() error = %v, want absolute path error", err)
	}
}

func writeConfig(t *testing.T, path string, cfg Config, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create config parent: %v", err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("chmod config parent: %v", err)
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod config: %v", err)
	}
}

func testToken(fill byte) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat(string([]byte{fill}), 32)))
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode for %s = %04o, want %04o", path, got, want)
	}
}

func privateTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("chmod private temp dir: %v", err)
	}
	return dir
}
