package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/three-b0dy/OpenSurge-for-Linux/internal/config"
	"github.com/three-b0dy/OpenSurge-for-Linux/internal/controlapi"
	"golang.org/x/term"
)

const (
	defaultConfigPath         = "/etc/opensurge/config.yaml"
	defaultStoreDir           = "/var/lib/opensurge"
	managedTLSDir             = "/etc/opensurge/tls"
	maxInstallerPasswordBytes = 256
)

type setupOptions struct {
	command       string
	configPath    string
	storeDir      string
	username      string
	certPath      string
	keyPath       string
	passwordFD    int
	passwordFDSet bool
}

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func parseSetupArgs(args []string) (setupOptions, error) {
	if len(args) == 0 {
		return setupOptions{}, errors.New("usage: opensurge-setup init|reset-password|replace-certificate")
	}
	options := setupOptions{command: args[0], configPath: defaultConfigPath, storeDir: defaultStoreDir, passwordFD: -1}
	if args[0] != "init" && hasPasswordFDOption(args[1:]) {
		return setupOptions{}, errors.New("--password-fd is only supported with init")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	switch args[0] {
	case "init":
		flags.StringVar(&options.configPath, "config", options.configPath, "gateway configuration path")
		flags.StringVar(&options.storeDir, "store", options.storeDir, "control-plane state directory")
		flags.StringVar(&options.username, "username", "", "administrator username")
		flags.IntVar(&options.passwordFD, "password-fd", options.passwordFD, "inherited installer password pipe descriptor")
	case "reset-password":
		flags.StringVar(&options.storeDir, "store", options.storeDir, "control-plane state directory")
		flags.StringVar(&options.username, "username", "", "administrator username")
	case "replace-certificate":
		flags.StringVar(&options.configPath, "config", options.configPath, "gateway configuration path")
		flags.StringVar(&options.certPath, "cert", "", "replacement certificate PEM")
		flags.StringVar(&options.keyPath, "key", "", "replacement private-key PEM")
	default:
		return setupOptions{}, fmt.Errorf("unknown setup command %q", args[0])
	}
	if err := flags.Parse(args[1:]); err != nil {
		return setupOptions{}, err
	}
	if flags.NArg() != 0 {
		return setupOptions{}, fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	flags.Visit(func(current *flag.Flag) {
		if current.Name == "password-fd" {
			options.passwordFDSet = true
		}
	})
	if options.passwordFDSet && options.passwordFD < 0 {
		return setupOptions{}, errors.New("--password-fd must be a non-negative inherited pipe descriptor")
	}
	switch options.command {
	case "init", "reset-password":
		if strings.TrimSpace(options.username) == "" {
			return setupOptions{}, errors.New("--username is required")
		}
	case "replace-certificate":
		if strings.TrimSpace(options.certPath) == "" || strings.TrimSpace(options.keyPath) == "" {
			return setupOptions{}, errors.New("--cert and --key are required")
		}
	}
	return options, nil
}

func hasPasswordFDOption(args []string) bool {
	for _, arg := range args {
		if arg == "--password-fd" || strings.HasPrefix(arg, "--password-fd=") {
			return true
		}
	}
	return false
}

func run(args []string, stdin *os.File, output io.Writer) error {
	if os.Geteuid() != 0 {
		return errors.New("opensurge-setup must run as root")
	}
	options, err := parseSetupArgs(args)
	if err != nil {
		return err
	}
	switch options.command {
	case "init":
		return runInit(options, stdin, output)
	case "reset-password":
		return runResetPassword(options, stdin, output)
	case "replace-certificate":
		return runReplaceCertificate(options, output)
	default:
		return fmt.Errorf("unknown setup command %q", options.command)
	}
}

func runInit(options setupOptions, stdin *os.File, output io.Writer) error {
	_, certPath, keyPath, listenerIP, err := managedTLSConfig(options.configPath)
	if err != nil {
		return err
	}
	store := controlapi.NewFileAdminStore(options.storeDir)
	initialized, err := store.Initialized()
	if err != nil {
		return fmt.Errorf("check administrator setup: %w", err)
	}
	if initialized {
		return errors.New("administrator setup has already been completed")
	}
	var password string
	if options.passwordFDSet {
		password, err = readPasswordFromInheritedPipe(options.passwordFD)
		if err != nil {
			return err
		}
	} else {
		password, err = passwordFromTTY(stdin, output, "Administrator password: ")
		if err != nil {
			return err
		}
		confirmation, err := passwordFromTTY(stdin, output, "Repeat administrator password: ")
		if err != nil {
			return err
		}
		if password != confirmation {
			return errors.New("administrator passwords do not match")
		}
	}
	if err := controlapi.EnsureSelfSigned(certPath, keyPath, []net.IP{listenerIP}, timeNow()); err != nil {
		return err
	}
	if err := setManagedTLSOwnershipFn(certPath, keyPath); err != nil {
		return err
	}
	if err := store.Set(options.username, password); err != nil {
		return fmt.Errorf("save administrator: %w", err)
	}
	_, _ = fmt.Fprintf(output, "OpenSurge administrator and HTTPS certificate initialized for %s\n", listenerIP)
	return nil
}

func runResetPassword(options setupOptions, stdin *os.File, output io.Writer) error {
	password, err := readTTYPassword(stdin, output, "New administrator password: ")
	if err != nil {
		return err
	}
	confirmation, err := readTTYPassword(stdin, output, "Repeat administrator password: ")
	if err != nil {
		return err
	}
	if password != confirmation {
		return errors.New("administrator passwords do not match")
	}
	store := controlapi.NewFileAdminStore(options.storeDir)
	if err := store.Reset(options.username, password); err != nil {
		return fmt.Errorf("reset administrator password: %w", err)
	}
	_, _ = fmt.Fprintln(output, "OpenSurge administrator password reset")
	return nil
}

func runReplaceCertificate(options setupOptions, output io.Writer) error {
	_, certPath, keyPath, listenerIP, err := managedTLSConfig(options.configPath)
	if err != nil {
		return err
	}
	keySource, err := managedTLSPath(options.keyPath)
	if err != nil {
		return fmt.Errorf("replacement private key: %w", err)
	}
	certPEM, err := os.ReadFile(options.certPath)
	if err != nil {
		return fmt.Errorf("read replacement certificate: %w", err)
	}
	keyPEM, err := os.ReadFile(keySource)
	if err != nil {
		return fmt.Errorf("read replacement private key: %w", err)
	}
	if _, err := controlapi.ValidateCertificatePairForHost(certPEM, keyPEM, listenerIP.String()); err != nil {
		return err
	}
	if err := controlapi.ReplaceCertificate(certPath, keyPath, certPEM, keyPEM); err != nil {
		return err
	}
	if err := setManagedTLSOwnershipFn(certPath, keyPath); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(output, "OpenSurge HTTPS certificate replaced")
	return nil
}

func managedTLSConfig(configPath string) (config.Config, string, string, net.IP, error) {
	cfg, err := config.LoadRuntime(configPath)
	if err != nil {
		return config.Config{}, "", "", nil, fmt.Errorf("load management configuration: %w", err)
	}
	certPath := cfg.Management.TLSCertFile
	if certPath == "" {
		certPath = controlapi.DefaultTLSCertFile
	}
	keyPath := cfg.Management.TLSKeyFile
	if keyPath == "" {
		keyPath = controlapi.DefaultTLSKeyFile
	}
	certPath, err = managedTLSPath(certPath)
	if err != nil {
		return config.Config{}, "", "", nil, err
	}
	keyPath, err = managedTLSPath(keyPath)
	if err != nil {
		return config.Config{}, "", "", nil, err
	}
	host, _, err := net.SplitHostPort(cfg.Management.Listen)
	if err != nil {
		return config.Config{}, "", "", nil, fmt.Errorf("management.listen: %w", err)
	}
	listenerIP := net.ParseIP(host).To4()
	if listenerIP == nil {
		return config.Config{}, "", "", nil, errors.New("management.listen must use an IPv4 address")
	}
	return cfg, certPath, keyPath, listenerIP, nil
}

func managedTLSPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	root, err := filepath.Abs(managedTLSDirectory)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(root, absolute)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("TLS path %q must remain under %s", path, managedTLSDirectory)
	}
	return absolute, nil
}

func readTTYPassword(stdin *os.File, output io.Writer, prompt string) (string, error) {
	if stdin == nil || !term.IsTerminal(int(stdin.Fd())) {
		return "", errors.New("password input must be an interactive TTY")
	}
	_, _ = fmt.Fprint(output, prompt)
	password, err := term.ReadPassword(int(stdin.Fd()))
	_, _ = fmt.Fprintln(output)
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	return string(password), nil
}

// readPasswordFromInheritedPipe accepts the single anonymous pipe descriptor
// passed by the release installer. It deliberately duplicates rather than
// closes the caller-owned descriptor, so only the child process consumes it.
func readPasswordFromInheritedPipe(fd int) (string, error) {
	if fd < 0 {
		return "", errors.New("password file descriptor must be non-negative")
	}
	duplicatedFD, err := syscall.Dup(fd)
	if err != nil {
		return "", fmt.Errorf("password file descriptor must be an open inherited pipe: %w", err)
	}
	input := os.NewFile(uintptr(duplicatedFD), "opensurge-installer-password")
	defer input.Close()

	info, err := input.Stat()
	if err != nil {
		return "", fmt.Errorf("inspect password pipe: %w", err)
	}
	if info.Mode()&os.ModeNamedPipe == 0 {
		return "", errors.New("password file descriptor must be an inherited pipe")
	}

	inputBytes, err := io.ReadAll(io.LimitReader(input, maxInstallerPasswordBytes+2))
	if err != nil {
		return "", fmt.Errorf("read password pipe: %w", err)
	}
	if len(inputBytes) == 0 || len(inputBytes) > maxInstallerPasswordBytes+1 {
		return "", fmt.Errorf("installer password must be one line of at most %d bytes", maxInstallerPasswordBytes)
	}
	if inputBytes[len(inputBytes)-1] != '\n' || bytes.Count(inputBytes, []byte{'\n'}) != 1 || bytes.IndexByte(inputBytes, '\r') >= 0 || bytes.IndexByte(inputBytes, 0) >= 0 {
		return "", errors.New("installer password must be exactly one newline-terminated line")
	}
	password := string(inputBytes[:len(inputBytes)-1])
	if password == "" || !utf8.ValidString(password) {
		return "", errors.New("installer password must be a non-empty UTF-8 line")
	}
	return password, nil
}

func setManagedTLSOwnership(paths ...string) error {
	group, err := user.LookupGroup("opensurge")
	if err != nil {
		return fmt.Errorf("lookup opensurge group: %w", err)
	}
	gid, err := strconv.Atoi(group.Gid)
	if err != nil {
		return fmt.Errorf("parse opensurge group id: %w", err)
	}
	seen := map[string]bool{}
	for _, path := range paths {
		for _, candidate := range []string{path, filepath.Dir(path)} {
			if seen[candidate] {
				continue
			}
			seen[candidate] = true
			if err := os.Chown(candidate, 0, gid); err != nil {
				return fmt.Errorf("set root:opensurge ownership on %s: %w", candidate, err)
			}
		}
	}
	return nil
}

var (
	managedTLSDirectory      = managedTLSDir
	passwordFromTTY          = readTTYPassword
	setManagedTLSOwnershipFn = setManagedTLSOwnership
	timeNow                  = func() time.Time { return time.Now().UTC() }
)
