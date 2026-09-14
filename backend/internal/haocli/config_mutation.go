package haocli

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/aoagents/agent-orchestrator/backend/internal/haocontract"
)

const maxConfigBytes = 1 << 20

type configCreateOptions struct {
	nonInteractive bool
	dryRun         bool
	machine        string
	mode           string
	aoVersion      string
	harness        string
	dependencies   string
	serviceEnabled string
	workflow       string
	pairPort       string
}

type configMutationStore struct {
	beforeCommit func()
}

type configRevision struct {
	digest     [sha256.Size]byte
	fromBackup bool
}

func newConfigCreateCommand(deps Deps, rootOpts *options) *cobra.Command {
	opts := &configCreateOptions{}
	cmd := &cobra.Command{
		Use: "create", Short: "Create a complete version 1 configuration", Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := resolveConfigPath(deps, rootOpts.configPath)
			if err != nil {
				return operationalError("resolve configuration path", err)
			}
			values, err := collectConfigValues(cmd, deps.In, opts)
			if err != nil {
				return err
			}
			data, object, err := marshalValidatedConfig(values)
			if err != nil {
				return invalidConfigMutation(path, err)
			}
			if opts.dryRun {
				return renderConfigMutation(cmd, rootOpts.json, "planned", path, object)
			}
			if err := (configMutationStore{}).create(path, data); err != nil {
				return configWriteError(path, err)
			}
			return renderConfigMutation(cmd, rootOpts.json, "created", path, object)
		},
	}
	flags := cmd.Flags()
	flags.BoolVar(&opts.nonInteractive, "non-interactive", false, "never prompt for missing input")
	flags.BoolVar(&opts.dryRun, "dry-run", false, "validate and print without writing")
	flags.StringVar(&opts.machine, "machine", "", "machine name")
	flags.StringVar(&opts.mode, "mode", "", "machine mode: local or pair")
	flags.StringVar(&opts.aoVersion, "ao-version", "", "pinned AO version")
	flags.StringVar(&opts.harness, "harness", "", "harness id")
	flags.StringVar(&opts.dependencies, "install", "", "dependency policy: missing or none")
	flags.StringVar(&opts.serviceEnabled, "service-enabled", "", "whether HAO manages services")
	flags.StringVar(&opts.workflow, "workflow", "", "workflow profile: general or github")
	flags.StringVar(&opts.pairPort, "pair-port", "", "pair gateway listen port")
	return cmd
}

func collectConfigValues(cmd *cobra.Command, input io.Reader, opts *configCreateOptions) (map[string]string, error) {
	values := map[string]string{
		"machine.name": opts.machine, "mode": opts.mode, "components.aoVersion": opts.aoVersion,
		"harness.id": opts.harness, "install.dependencies": opts.dependencies,
		"service.enabled": opts.serviceEnabled, "workflow.profile": opts.workflow,
		"pair.listenPort": opts.pairPort,
	}
	if opts.nonInteractive {
		required := []string{"machine.name", "mode", "components.aoVersion", "harness.id", "install.dependencies", "service.enabled"}
		if values["mode"] == "pair" {
			required = append(required, "pair.listenPort")
		}
		missing := make([]string, 0)
		for _, key := range required {
			if strings.TrimSpace(values[key]) == "" {
				missing = append(missing, key)
			}
		}
		if len(missing) != 0 {
			return nil, commandError{Code: "missing_non_interactive_input", Message: "non-interactive config creation is missing required input", Remediation: "provide every required config creation flag", Details: map[string]any{"missing": missing}, ExitStatus: 2}
		}
		return values, nil
	}

	reader := bufio.NewReader(input)
	prompts := []struct{ key, label, fallback string }{
		{"machine.name", "Machine name", ""}, {"mode", "Mode", "local"},
		{"components.aoVersion", "AO version", AOArtifactVersion}, {"harness.id", "Harness", "claude-code"},
		{"install.dependencies", "Dependency policy", "missing"}, {"service.enabled", "Manage services", "true"},
		{"workflow.profile", "Workflow profile", "general"},
	}
	for _, prompt := range prompts {
		if strings.TrimSpace(values[prompt.key]) != "" {
			continue
		}
		value, err := promptValue(cmd.ErrOrStderr(), reader, prompt.label, prompt.fallback)
		if err != nil {
			return nil, commandError{Code: "invalid_usage", Message: "could not read interactive configuration", Remediation: "retry interactively or use --non-interactive with all required flags", ExitStatus: 2, Cause: err}
		}
		values[prompt.key] = value
	}
	if values["mode"] == "pair" && strings.TrimSpace(values["pair.listenPort"]) == "" {
		value, err := promptValue(cmd.ErrOrStderr(), reader, "Pair listen port", "443")
		if err != nil {
			return nil, commandError{Code: "invalid_usage", Message: "could not read interactive configuration", Remediation: "retry or pass --pair-port", ExitStatus: 2, Cause: err}
		}
		values["pair.listenPort"] = value
	}
	return values, nil
}

func promptValue(w io.Writer, reader *bufio.Reader, label, fallback string) (string, error) {
	if fallback == "" {
		_, _ = fmt.Fprintf(w, "%s: ", label)
	} else {
		_, _ = fmt.Fprintf(w, "%s [%s]: ", label, fallback)
	}
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	value := strings.TrimSpace(line)
	if value == "" {
		value = fallback
	}
	if value == "" {
		return "", errors.New("a required value was empty")
	}
	return value, nil
}

func marshalValidatedConfig(values map[string]string) ([]byte, map[string]any, error) {
	serviceEnabled, err := strconv.ParseBool(values["service.enabled"])
	if err != nil {
		return nil, nil, fmt.Errorf("service.enabled must be true or false")
	}
	object := map[string]any{
		"version":    1,
		"machine":    map[string]any{"name": values["machine.name"]},
		"mode":       values["mode"],
		"components": map[string]any{"aoVersion": values["components.aoVersion"]},
		"harness":    map[string]any{"id": values["harness.id"]},
		"install":    map[string]any{"dependencies": values["install.dependencies"]},
		"service":    map[string]any{"enabled": serviceEnabled},
	}
	if values["workflow.profile"] != "" {
		object["workflow"] = map[string]any{"profile": values["workflow.profile"]}
	}
	if values["mode"] == "pair" {
		port, parseErr := strconv.Atoi(values["pair.listenPort"])
		if parseErr != nil {
			return nil, nil, errors.New("pair.listenPort must be an integer")
		}
		object["pair"] = map[string]any{"listenPort": port}
	}
	data, err := yaml.Marshal(object)
	if err != nil {
		return nil, nil, err
	}
	validated, err := haocontract.ParseConfig(data)
	return data, validated, err
}

func newConfigSetCommand(deps Deps, rootOpts *options) *cobra.Command {
	var dryRun bool
	var nonInteractive bool
	cmd := &cobra.Command{
		Use: "set <supported-key> <value>", Short: "Set one supported configuration value", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := resolveConfigPath(deps, rootOpts.configPath)
			if err != nil {
				return operationalError("resolve configuration path", err)
			}
			store := configMutationStore{}
			object, changed, err := store.set(path, args[0], args[1], dryRun)
			if err != nil {
				var unsupported haocontract.UnsupportedVersionError
				if errors.As(err, &unsupported) {
					return commandError{Code: "unsupported_config_version", Message: "hao configuration version is unsupported", Remediation: "use a version 1 configuration", Details: map[string]any{"path": path}, ExitStatus: 2, Cause: err}
				}
				if errors.Is(err, errUnknownConfigKey) || errors.Is(err, errInvalidConfigValue) {
					return invalidConfigMutation(path, err)
				}
				return configWriteError(path, err)
			}
			status := "unchanged"
			if dryRun && changed {
				status = "planned"
			} else if changed {
				status = "updated"
			}
			return renderConfigMutation(cmd, rootOpts.json, status, path, object)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "validate and print without writing")
	cmd.Flags().BoolVar(&nonInteractive, "non-interactive", false, "never prompt for input")
	return cmd
}

var (
	errUnknownConfigKey   = errors.New("unsupported configuration key")
	errInvalidConfigValue = errors.New("invalid configuration value")
	errConfigBusy         = errors.New("another hao config mutation is active")
	errConfigStale        = errors.New("configuration changed during mutation")
)

func (s configMutationStore) set(path, key, value string, dryRun bool) (map[string]any, bool, error) {
	before, fromBackup, err := readConfigAuthority(path)
	if err != nil {
		return nil, false, err
	}
	expected := configRevision{digest: sha256.Sum256(before), fromBackup: fromBackup}
	object, err := haocontract.ParseConfig(before)
	if err != nil {
		return nil, false, err
	}
	canonicalBefore, err := yaml.Marshal(object)
	if err != nil {
		return nil, false, err
	}
	if err := applyConfigValue(object, key, value); err != nil {
		return nil, false, err
	}
	data, err := yaml.Marshal(object)
	if err != nil {
		return nil, false, err
	}
	validated, err := haocontract.ParseConfig(data)
	if err != nil {
		return nil, false, fmt.Errorf("%w: %w", errInvalidConfigValue, err)
	}
	changed := !bytes.Equal(data, canonicalBefore) || fromBackup
	if dryRun || !changed {
		return validated, changed, nil
	}
	if err := s.replace(path, data, expected); err != nil {
		return nil, false, err
	}
	return validated, true, nil
}

func applyConfigValue(object map[string]any, key, value string) error {
	setNested := func(section, field string, parsed any) error {
		child, ok := object[section].(map[string]any)
		if !ok {
			child = map[string]any{}
			object[section] = child
		}
		child[field] = parsed
		return nil
	}
	switch key {
	case "machine.name":
		return setNested("machine", "name", value)
	case "mode":
		object["mode"] = value
		switch value {
		case "local":
			delete(object, "pair")
		case "pair":
			if _, ok := object["pair"]; !ok {
				object["pair"] = map[string]any{"listenPort": 443}
			}
		}
		return nil
	case "components.aoVersion":
		return setNested("components", "aoVersion", value)
	case "harness.id":
		return setNested("harness", "id", value)
	case "install.dependencies":
		return setNested("install", "dependencies", value)
	case "service.enabled":
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("%w: service.enabled must be true or false", errInvalidConfigValue)
		}
		return setNested("service", "enabled", parsed)
	case "workflow.profile":
		return setNested("workflow", "profile", value)
	case "pair.listenPort":
		if object["mode"] != "pair" {
			return fmt.Errorf("%w: pair.listenPort requires pair mode", errInvalidConfigValue)
		}
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("%w: pair.listenPort must be an integer", errInvalidConfigValue)
		}
		return setNested("pair", "listenPort", parsed)
	default:
		return fmt.Errorf("%w %q", errUnknownConfigKey, key)
	}
}

func (s configMutationStore) create(path string, data []byte) error {
	if err := ensureConfigParent(path); err != nil {
		return err
	}
	lock, err := acquireConfigLock(path + ".lock")
	if err != nil {
		return err
	}
	defer func() { _ = releaseConfigLock(lock) }()
	if _, err := managedLstat(path); err == nil {
		return errors.New("configuration already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return s.commit(path, data, false)
}

func (s configMutationStore) replace(path string, data []byte, expected configRevision) error {
	lock, err := acquireConfigLock(path + ".lock")
	if err != nil {
		return err
	}
	defer func() { _ = releaseConfigLock(lock) }()
	current, fromBackup, err := readConfigAuthority(path)
	if err != nil {
		return err
	}
	if sha256.Sum256(current) != expected.digest || fromBackup != expected.fromBackup {
		return errConfigStale
	}
	if s.beforeCommit != nil {
		s.beforeCommit()
	}
	latest, latestFromBackup, readErr := readConfigAuthority(path)
	if readErr != nil {
		return readErr
	}
	if sha256.Sum256(latest) != expected.digest || latestFromBackup != expected.fromBackup {
		return errConfigStale
	}
	return s.commit(path, data, !fromBackup)
}

func (s configMutationStore) commit(path string, data []byte, backup bool) error {
	if _, err := haocontract.ParseConfig(data); err != nil {
		return err
	}
	if backup {
		current, err := readManagedFile(path, maxConfigBytes)
		if err != nil {
			return err
		}
		if _, err := haocontract.ParseConfig(current); err != nil {
			return err
		}
		if err := durableReplace(path+".bak", current, 0o600); err != nil {
			return fmt.Errorf("write last-known-good backup: %w", err)
		}
	}
	return durableReplace(path, data, 0o600)
}

func readConfigAuthority(path string) ([]byte, bool, error) {
	data, err := readManagedFile(path, maxConfigBytes)
	if err == nil {
		_, parseErr := haocontract.ParseConfig(data)
		if parseErr == nil {
			return data, false, nil
		}
		var unsupported haocontract.UnsupportedVersionError
		if errors.As(parseErr, &unsupported) {
			return nil, false, parseErr
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	backup, backupErr := readManagedFile(path+".bak", maxConfigBytes)
	if backupErr != nil {
		if err != nil {
			return nil, false, err
		}
		return nil, false, errors.New("configuration is invalid and no valid backup is available")
	}
	if _, backupErr = haocontract.ParseConfig(backup); backupErr != nil {
		return nil, false, errors.New("configuration and backup are invalid")
	}
	return backup, true, nil
}

func readManagedFile(path string, limit int64) ([]byte, error) {
	file, err := openManagedRegular(path, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("configuration exceeds size limit")
	}
	return data, nil
}

func ensureConfigParent(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("configuration path must be absolute")
	}
	return managedMkdirAll(filepath.Dir(path), 0o700)
}

func durableReplace(path string, data []byte, mode os.FileMode) error {
	if info, err := managedLstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("configuration target is not a safe regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary, err := randomTemporaryPath(filepath.Dir(path), ".hao-config-")
	if err != nil {
		return err
	}
	file, err := managedCreateExclusive(temporary, mode)
	if err != nil {
		return err
	}
	remove := true
	defer func() {
		if remove {
			_ = removeManagedIfExists(temporary)
		}
	}()
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := managedRename(temporary, path); err != nil {
		return err
	}
	remove = false
	return syncDirectory(filepath.Dir(path))
}

func renderConfigMutation(cmd *cobra.Command, jsonOutput bool, status, path string, object map[string]any) error {
	if jsonOutput {
		return writeJSON(cmd.OutOrStdout(), map[string]any{"status": status, "path": path, "config": object})
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", status, path)
	return err
}

func invalidConfigMutation(path string, err error) commandError {
	return commandError{Code: "invalid_config", Message: "hao configuration mutation is invalid", Remediation: "use a supported key and a value allowed by the version 1 schema", Details: map[string]any{"path": path, "diagnostic": safeDiagnostic(err)}, ExitStatus: 2, Cause: err}
}

func configWriteError(path string, err error) commandError {
	remediation := "check the configuration path and permissions, then retry"
	if errors.Is(err, errConfigStale) || errors.Is(err, errConfigBusy) {
		remediation = "retry after the other configuration mutation completes"
	}
	return commandError{Code: "operation_failed", Message: "could not write hao configuration", Remediation: remediation, Details: map[string]any{"path": path, "diagnostic": safeDiagnostic(err)}, ExitStatus: 1, Cause: err}
}
