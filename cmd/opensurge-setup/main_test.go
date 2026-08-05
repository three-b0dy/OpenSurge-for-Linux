package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/three-b0dy/OpenSurge-for-Linux/internal/controlapi"
)

func TestParseSetupArgsReplaceCertificate(t *testing.T) {
	options, err := parseSetupArgs([]string{"replace-certificate", "--cert", "/tmp/cert.pem", "--key", "/tmp/key.pem"})
	if err != nil {
		t.Fatal(err)
	}
	if options.command != "replace-certificate" || options.certPath != "/tmp/cert.pem" || options.keyPath != "/tmp/key.pem" {
		t.Fatalf("parsed options = %#v", options)
	}
}

func TestParseSetupArgsRequiresCertificatePair(t *testing.T) {
	if _, err := parseSetupArgs([]string{"replace-certificate", "--cert", "/tmp/cert.pem"}); err == nil {
		t.Fatal("parseSetupArgs() accepted a missing private key")
	}
}

func TestManagedTLSPathRejectsOutsidePrivateKeyLocation(t *testing.T) {
	if _, err := managedTLSPath("/tmp/opensurge-key.pem"); err == nil {
		t.Fatal("managedTLSPath() accepted a private key outside the managed directory")
	}
}

func TestParseSetupArgsAcceptsPasswordFDOnlyForInit(t *testing.T) {
	options, err := parseSetupArgs([]string{"init", "--username", "admin", "--password-fd", "9"})
	if err != nil {
		t.Fatal(err)
	}
	if !options.passwordFDSet || options.passwordFD != 9 {
		t.Fatalf("password FD options = %#v", options)
	}
	for _, fd := range []string{"-1", "0", "1", "2"} {
		if _, err := parseSetupArgs([]string{"init", "--username", "admin", "--password-fd", fd}); err == nil || !strings.Contains(err.Error(), "stdin, stdout, or stderr") {
			t.Fatalf("--password-fd %s error = %v, want stdin/stdout/stderr rejection", fd, err)
		}
	}

	for _, args := range [][]string{
		{"reset-password", "--password-fd", "9"},
		{"replace-certificate", "--cert", "/tmp/cert.pem", "--key", "/tmp/key.pem", "--password-fd", "9"},
	} {
		if _, err := parseSetupArgs(args); err == nil || !strings.Contains(err.Error(), "only supported with init") {
			t.Fatalf("parseSetupArgs(%q) error = %v, want init-only rejection", args, err)
		}
	}
}

func TestSetupUsageDocumentsEveryCommandOption(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}, {"help"}, {"reset-password", "--help"}} {
		var output bytes.Buffer
		if err := run(args, nil, &output); err != nil {
			t.Fatalf("run(%q) error = %v", args, err)
		}
		usage := output.String()
		for _, want := range []string{
			"init", "reset-password", "replace-certificate", "reset-permissions",
			"--username", "--store", "--config", "--password-fd", "--cert", "--key", "--check",
			defaultStoreDir, defaultConfigPath, managedTLSDir,
			"single existing administrator",
		} {
			if !strings.Contains(usage, want) {
				t.Errorf("run(%q) usage is missing %q:\n%s", args, want, usage)
			}
		}
	}
}

func TestResetPasswordResetsTheDefaultAdministratorAndReportsIt(t *testing.T) {
	originalTTYReader := passwordFromTTY
	t.Cleanup(func() { passwordFromTTY = originalTTYReader })
	storeDir := filepath.Join(t.TempDir(), "store")
	store := controlapi.NewFileAdminStore(storeDir)
	if err := store.Set("admin", "old-password"); err != nil {
		t.Fatal(err)
	}

	const replacement = "short"
	passwordFromTTY = func(*os.File, io.Writer, string) (string, error) { return replacement, nil }
	var output bytes.Buffer
	if err := runResetPassword(setupOptions{command: "reset-password", storeDir: storeDir}, nil, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "admin") {
		t.Errorf("reset output does not name the administrator: %q", output.String())
	}
	if strings.Contains(output.String(), replacement) {
		t.Error("reset output exposed the new password")
	}
	if err := store.Authenticate("admin", replacement); err != nil {
		t.Fatalf("reset password does not authenticate: %v", err)
	}
}

func TestParseSetupArgsAcceptsResetPermissions(t *testing.T) {
	options, err := parseSetupArgs([]string{"reset-permissions"})
	if err != nil {
		t.Fatal(err)
	}
	if options.command != "reset-permissions" || options.checkOnly {
		t.Fatalf("parsed options = %#v", options)
	}
	checked, err := parseSetupArgs([]string{"reset-permissions", "--check"})
	if err != nil {
		t.Fatal(err)
	}
	if !checked.checkOnly {
		t.Fatalf("--check options = %#v", checked)
	}
	if _, err := parseSetupArgs([]string{"reset-permissions", "--store", "/tmp"}); err == nil {
		t.Fatal("reset-permissions accepted --store; its paths come from the layout")
	}
}

// The read-only report is the one form an operator can run before escalating, so
// it must not be blocked by the root check that guards every writing command.
func TestResetPermissionsCheckDoesNotRequireRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root")
	}
	err := run([]string{"reset-permissions", "--check"}, nil, &bytes.Buffer{})
	if err != nil && strings.Contains(err.Error(), "must run as root") {
		t.Fatalf("reset-permissions --check was rejected for not being root: %v", err)
	}
	if err := run([]string{"reset-permissions"}, nil, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "must run as root") {
		t.Fatalf("reset-permissions error = %v, want a root requirement", err)
	}
}

func TestParseSetupArgsRejectsUsernameOnResetPassword(t *testing.T) {
	for _, args := range [][]string{
		{"reset-password", "--username", "admin"},
		{"reset-password", "--username=admin"},
	} {
		if _, err := parseSetupArgs(args); err == nil || !strings.Contains(err.Error(), "no longer takes --username") {
			t.Fatalf("parseSetupArgs(%q) error = %v, want a --username removal message", args, err)
		}
	}
	if _, err := parseSetupArgs([]string{"reset-password"}); err != nil {
		t.Fatalf("parseSetupArgs(reset-password) error = %v, want acceptance without --username", err)
	}
}

func TestSetupUsageIsNotTreatedAsACommand(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"help"}} {
		if _, err := parseSetupArgs(args); err == nil {
			t.Fatalf("parseSetupArgs(%q) accepted a usage request as a command", args)
		}
	}
	if _, err := parseSetupArgs(nil); err == nil || !strings.Contains(err.Error(), "--help") {
		t.Fatalf("parseSetupArgs(nil) error = %v, want a --help hint", err)
	}
}

func TestReadPasswordFromInheritedPipe(t *testing.T) {
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readEnd.Close()
	if _, err := writeEnd.WriteString("correct-horse-battery-staple\n"); err != nil {
		t.Fatal(err)
	}
	if err := writeEnd.Close(); err != nil {
		t.Fatal(err)
	}

	password, err := readPasswordFromInheritedPipe(int(readEnd.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	if password != "correct-horse-battery-staple" {
		t.Fatalf("pipe password = %q", password)
	}
}

func TestReadPasswordFromInheritedPipeRejectsUnsafeDescriptorsAndInput(t *testing.T) {
	t.Run("negative descriptor", func(t *testing.T) {
		if _, err := readPasswordFromInheritedPipe(-1); err == nil || !strings.Contains(err.Error(), "stdin, stdout, or stderr") {
			t.Fatalf("error = %v, want stdin/stdout/stderr rejection", err)
		}
	})

	t.Run("stdin, stdout, and stderr descriptors", func(t *testing.T) {
		for _, fd := range []int{0, 1, 2} {
			if _, err := readPasswordFromInheritedPipe(fd); err == nil || !strings.Contains(err.Error(), "stdin, stdout, or stderr") {
				t.Fatalf("fd %d error = %v, want stdin/stdout/stderr rejection", fd, err)
			}
		}
	})

	t.Run("closed descriptor", func(t *testing.T) {
		readEnd, writeEnd, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		_ = writeEnd.Close()
		fd := int(readEnd.Fd())
		if err := readEnd.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := readPasswordFromInheritedPipe(fd); err == nil || !strings.Contains(err.Error(), "open") {
			t.Fatalf("error = %v, want closed descriptor rejection", err)
		}
	})

	t.Run("write-only pipe descriptor", func(t *testing.T) {
		readEnd, writeEnd, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer readEnd.Close()
		defer writeEnd.Close()
		if _, err := readPasswordFromInheritedPipe(int(writeEnd.Fd())); err == nil || !strings.Contains(err.Error(), "read password pipe") {
			t.Fatalf("error = %v, want unreadable pipe rejection", err)
		}
	})

	t.Run("regular file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "password")
		if err := os.WriteFile(path, []byte("correct-horse-battery-staple\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		if _, err := readPasswordFromInheritedPipe(int(file.Fd())); err == nil || !strings.Contains(err.Error(), "pipe") {
			t.Fatalf("error = %v, want pipe rejection", err)
		}
	})

	t.Run("character device including terminals", func(t *testing.T) {
		file, err := os.Open(os.DevNull)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		if _, err := readPasswordFromInheritedPipe(int(file.Fd())); err == nil || !strings.Contains(err.Error(), "pipe") {
			t.Fatalf("error = %v, want character-device rejection", err)
		}
	})

	for name, input := range map[string]string{
		"missing newline": "correct-horse-battery-staple",
		"two lines":       "correct-horse\nbattery-staple\n",
		"carriage return": "correct-horse-battery-staple\r\n",
		"too long":        strings.Repeat("a", maxInstallerPasswordBytes+1) + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			readEnd, writeEnd, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer readEnd.Close()
			if _, err := writeEnd.WriteString(input); err != nil {
				t.Fatal(err)
			}
			if err := writeEnd.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := readPasswordFromInheritedPipe(int(readEnd.Fd())); err == nil {
				t.Fatal("readPasswordFromInheritedPipe accepted unsafe password input")
			}
		})
	}
}

func TestRunInitPasswordSourcesInitializeEquivalentAdminAndTLSState(t *testing.T) {
	password := "correct-horse-battery-staple"
	originalTLSDirectory := managedTLSDirectory
	originalOwnership := setManagedTLSOwnershipFn
	originalTTYReader := passwordFromTTY
	originalTimeNow := timeNow
	managedTLSDirectory = t.TempDir()
	setManagedTLSOwnershipFn = func(...string) error { return nil }
	timeNow = func() time.Time { return time.Date(2026, time.August, 5, 0, 0, 0, 0, time.UTC) }
	t.Cleanup(func() {
		managedTLSDirectory = originalTLSDirectory
		setManagedTLSOwnershipFn = originalOwnership
		passwordFromTTY = originalTTYReader
		timeNow = originalTimeNow
	})

	for _, source := range []string{"tty", "pipe"} {
		t.Run(source, func(t *testing.T) {
			root := t.TempDir()
			configPath := writeSetupConfig(t, root, filepath.Join(managedTLSDirectory, source))
			options := setupOptions{
				command:    "init",
				configPath: configPath,
				storeDir:   filepath.Join(root, "store"),
				username:   "admin",
			}
			var stdin *os.File
			if source == "tty" {
				passwordFromTTY = func(*os.File, io.Writer, string) (string, error) {
					return password, nil
				}
				stdin, _ = os.Open(os.DevNull)
				defer stdin.Close()
			} else {
				readEnd, writeEnd, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				if _, err := writeEnd.WriteString(password + "\n"); err != nil {
					t.Fatal(err)
				}
				if err := writeEnd.Close(); err != nil {
					t.Fatal(err)
				}
				defer readEnd.Close()
				options.passwordFDSet = true
				options.passwordFD = int(readEnd.Fd())
				stdin = readEnd
			}

			var output bytes.Buffer
			if err := runInit(options, stdin, &output); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(output.String(), password) {
				t.Fatal("init output exposed the administrator password")
			}
			store := controlapi.NewFileAdminStore(options.storeDir)
			if err := store.Authenticate("admin", password); err != nil {
				t.Fatalf("initialized administrator did not authenticate: %v", err)
			}
			if _, err := os.Stat(filepath.Join(managedTLSDirectory, source, "cert.pem")); err != nil {
				t.Fatalf("missing generated certificate: %v", err)
			}
			if _, err := os.Stat(filepath.Join(managedTLSDirectory, source, "key.pem")); err != nil {
				t.Fatalf("missing generated private key: %v", err)
			}
		})
	}
}

func TestRunInitWithoutPasswordFDStillRequiresTTY(t *testing.T) {
	originalTLSDirectory := managedTLSDirectory
	managedTLSDirectory = t.TempDir()
	t.Cleanup(func() { managedTLSDirectory = originalTLSDirectory })
	root := t.TempDir()
	options := setupOptions{command: "init", configPath: writeSetupConfig(t, root, managedTLSDirectory), storeDir: filepath.Join(root, "store"), username: "admin"}
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	if err := runInit(options, stdin, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "interactive TTY") {
		t.Fatalf("runInit() error = %v, want interactive TTY rejection", err)
	}
}

func writeSetupConfig(t *testing.T, root, tlsDir string) string {
	t.Helper()
	if err := os.MkdirAll(tlsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "config.yaml")
	contents := "management:\n" +
		"  listen: \"192.0.2.10:61767\"\n" +
		"  tls_cert_file: \"" + filepath.Join(tlsDir, "cert.pem") + "\"\n" +
		"  tls_key_file: \"" + filepath.Join(tlsDir, "key.pem") + "\"\n"
	if err := os.WriteFile(configPath, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath
}
