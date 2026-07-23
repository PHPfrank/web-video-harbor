package config

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

const (
	DefaultAddress = "127.0.0.1:17432"
	maxConfigSize  = 64 << 10
)

var (
	writeConfigData = func(file *os.File, data []byte) (int, error) { return file.Write(data) }
	syncConfigFile  = func(file *os.File) error { return file.Sync() }
)

var errConfigAlreadyExists = errors.New("config already exists")

// Config contains the helper's persisted local settings.
type Config struct {
	Address     string `json:"address"`
	Token       string `json:"token"`
	DownloadDir string `json:"download_dir"`
}

// DefaultPath returns the per-user macOS configuration path.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find user home directory: %w", err)
	}
	if home == "" {
		return "", errors.New("find user home directory: empty path")
	}
	return filepath.Join(home, "Library", "Application Support", "网页视频下载器", "config.json"), nil
}

// Load reads path, or creates a secure configuration there on first run.
func Load(path string) (Config, error) {
	if path == "" {
		return Config{}, errors.New("config path is empty")
	}
	path = filepath.Clean(path)
	if err := validateExistingConfigParent(filepath.Dir(path)); err != nil {
		return Config{}, err
	}

	info, err := os.Lstat(path)
	switch {
	case err == nil:
		return loadExisting(path, info)
	case !errors.Is(err, os.ErrNotExist):
		return Config{}, fmt.Errorf("inspect config: %w", err)
	}

	cfg, err := newDefaultConfig()
	if err != nil {
		return Config{}, err
	}
	if err := validateAndPrepare(cfg); err != nil {
		return Config{}, err
	}
	if err := createConfigParent(filepath.Dir(path)); err != nil {
		return Config{}, err
	}
	if err := writeNew(path, cfg); err != nil {
		if !errors.Is(err, errConfigAlreadyExists) {
			return Config{}, err
		}
		info, inspectErr := os.Lstat(path)
		if inspectErr != nil {
			return Config{}, fmt.Errorf("inspect concurrently created config: %w", inspectErr)
		}
		return loadExisting(path, info)
	}
	return cfg, nil
}

func newDefaultConfig() (Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, fmt.Errorf("find user home directory: %w", err)
	}
	if home == "" {
		return Config{}, errors.New("find user home directory: empty path")
	}
	tokenBytes := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, tokenBytes); err != nil {
		return Config{}, fmt.Errorf("generate authentication token: %w", err)
	}
	return Config{
		Address:     DefaultAddress,
		Token:       base64.RawURLEncoding.EncodeToString(tokenBytes),
		DownloadDir: filepath.Join(home, "Downloads", "网页视频下载器"),
	}, nil
}

func loadExisting(path string, info os.FileInfo) (Config, error) {
	if info.Mode()&os.ModeSymlink != 0 {
		return Config{}, errors.New("config path must not be a symbolic link")
	}
	if !info.Mode().IsRegular() {
		return Config{}, errors.New("config path is not a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return Config{}, fmt.Errorf("config permissions %04o are insecure; expected 0600", info.Mode().Perm())
	}
	if info.Size() > maxConfigSize {
		return Config{}, fmt.Errorf("config is too large: maximum size is %d bytes", maxConfigSize)
	}

	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open config: %w", err)
	}
	defer file.Close()

	decoder := json.NewDecoder(io.LimitReader(file, maxConfigSize+1))
	decoder.DisallowUnknownFields()
	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	if err := validateAndPrepare(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func validateAndPrepare(cfg Config) error {
	if err := validateAddress(cfg.Address); err != nil {
		return err
	}
	decoded, err := base64.RawURLEncoding.DecodeString(cfg.Token)
	if err != nil || len(decoded) != 32 {
		return errors.New("config token must be an unpadded URL-safe 256-bit value")
	}
	if !filepath.IsAbs(cfg.DownloadDir) {
		return errors.New("download directory must be an absolute path")
	}
	if err := ensureDownloadDir(cfg.DownloadDir); err != nil {
		return err
	}
	return nil
}

func validateAddress(address string) error {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid loopback address %q: %w", address, err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("invalid loopback address %q: host must be a loopback IP literal", address)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("invalid loopback address %q: port must be between 1 and 65535", address)
	}
	return nil
}

func ensureDownloadDir(path string) error {
	info, err := os.Lstat(path)
	switch {
	case err == nil:
		return validateDownloadDirInfo(info)
	case !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("inspect download directory: %w", err)
	}

	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create download directory: %w", err)
	}
	info, err = os.Lstat(path)
	if err != nil {
		return fmt.Errorf("verify download directory: %w", err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("secure download directory permissions: %w", err)
	}
	info, err = os.Lstat(path)
	if err != nil {
		return fmt.Errorf("verify secured download directory: %w", err)
	}
	return validateDownloadDirInfo(info)
}

func validateDownloadDirInfo(info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("download directory must not be a symbolic link")
	}
	if !info.IsDir() {
		return errors.New("download directory path is not a directory")
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("download directory permissions %04o are insecure; group and other users must not have write access", info.Mode().Perm())
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("download directory owner could not be determined")
	}
	if int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("download directory owner %d does not match current user %d", stat.Uid, os.Getuid())
	}
	return nil
}

func createConfigParent(path string) error {
	info, err := os.Lstat(path)
	switch {
	case err == nil:
		return validateConfigParentInfo(info)
	case !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("inspect config parent: %w", err)
	}

	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create config parent: %w", err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("secure config parent permissions: %w", err)
	}
	return nil
}

func validateExistingConfigParent(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect config parent: %w", err)
	}
	return validateConfigParentInfo(info)
}

func validateConfigParentInfo(info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("config parent must not be a symbolic link")
	}
	if !info.IsDir() {
		return errors.New("config parent is not a directory")
	}
	if info.Mode().Perm() != 0o700 {
		return fmt.Errorf("config parent permissions %04o are insecure; expected 0700", info.Mode().Perm())
	}
	return nil
}

func writeNew(path string, cfg Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	data = append(data, '\n')

	parent := filepath.Dir(path)
	file, err := os.CreateTemp(parent, "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	tempPath := file.Name()
	closed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
		_ = os.Remove(tempPath)
	}()

	if err := file.Chmod(0o600); err != nil {
		return fmt.Errorf("secure temporary config permissions: %w", err)
	}
	written, err := writeConfigData(file, data)
	if err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	if written != len(data) {
		return fmt.Errorf("write config: %w", io.ErrShortWrite)
	}
	if err := syncConfigFile(file); err != nil {
		return fmt.Errorf("sync config: %w", err)
	}
	if err := file.Close(); err != nil {
		closed = true
		return fmt.Errorf("close config: %w", err)
	}
	closed = true

	if err := os.Link(tempPath, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("publish config: %w", errConfigAlreadyExists)
		}
		return fmt.Errorf("publish config: %w", err)
	}
	if err := os.Remove(tempPath); err != nil {
		return fmt.Errorf("remove temporary config: %w", err)
	}

	directory, err := os.Open(parent)
	if err != nil {
		return fmt.Errorf("open config parent for sync: %w", err)
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return fmt.Errorf("sync config parent: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close config parent: %w", closeErr)
	}
	return nil
}
