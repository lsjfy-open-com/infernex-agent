package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/domain"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/domainstore"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/privateinventory"
)

const maxPrivateInputBytes = int64(domain.MaxSnapshotBytes)

var (
	privateUUIDPattern        = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	privatePositiveIntPattern = regexp.MustCompile(`^[1-9][0-9]*$`)
)

type privateDiscoverOutput struct {
	Preview  privateinventory.Preview  `json:"preview"`
	Snapshot *domain.InventorySnapshot `json:"snapshot"`
	SavedRef *domain.RecordRef         `json:"savedRef,omitempty"`
}

func runPrivateInventory(args []string) error {
	if len(args) == 0 {
		return errors.New("private-inventory requires init, environment, discover, record, list, show, or verify")
	}
	switch args[0] {
	case "init":
		return runPrivateInventoryInit(args[1:])
	case "environment":
		return runPrivateInventoryEnvironment(args[1:])
	case "discover":
		return runPrivateInventoryDiscover(args[1:])
	case "record":
		return runPrivateInventoryRecord(args[1:])
	case "list":
		return runPrivateInventoryList(args[1:])
	case "show":
		return runPrivateInventoryShow(args[1:], false)
	case "verify":
		return runPrivateInventoryShow(args[1:], true)
	default:
		return fmt.Errorf("unknown private-inventory command %q", args[0])
	}
}

func runPrivateInventoryInit(args []string) error {
	flags := privateFlagSet("private-inventory init")
	stateDir := flags.String("state-dir", "", "absolute private inventory state directory")
	scope := flags.String("scope", "", "fixed local management scope")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *stateDir == "" || *scope == "" {
		return errors.New("init requires --state-dir ABS --scope ID")
	}
	store, err := domainstore.Init(*stateDir, *scope, os.Geteuid())
	if err != nil {
		return err
	}
	return writePrivateJSON(struct {
		Scope string `json:"scope"`
	}{Scope: store.Scope()})
}

func runPrivateInventoryEnvironment(args []string) error {
	if len(args) == 0 {
		return errors.New("environment requires register, update, or show")
	}
	switch args[0] {
	case "register":
		flags := privateFlagSet("private-inventory environment register")
		stateDir := flags.String("state-dir", "", "absolute private inventory state directory")
		input := flags.String("input", "", "protected local registration JSON file")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || *stateDir == "" || *input == "" {
			return errors.New("environment register requires --state-dir ABS --input FILE")
		}
		store, err := domainstore.Open(*stateDir, os.Geteuid())
		if err != nil {
			return err
		}
		connection, err := readPrivateConnection(*input)
		if err != nil {
			return err
		}
		environment, err := store.Register(context.Background(), connection)
		if err != nil {
			return err
		}
		return writePrivateJSON(environment)
	case "update":
		flags := privateFlagSet("private-inventory environment update")
		stateDir := flags.String("state-dir", "", "absolute private inventory state directory")
		id := flags.String("id", "", "registered Environment UUID")
		expectedRevision := flags.Int64("expected-revision", 0, "current Environment revision")
		input := flags.String("input", "", "protected local registration JSON file")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || *stateDir == "" || *id == "" || *expectedRevision <= 0 || *input == "" {
			return errors.New("environment update requires --state-dir ABS --id UUID --expected-revision N --input FILE")
		}
		store, err := domainstore.Open(*stateDir, os.Geteuid())
		if err != nil {
			return err
		}
		connection, err := readPrivateConnection(*input)
		if err != nil {
			return err
		}
		environment, err := store.Update(context.Background(), *id, *expectedRevision, connection)
		if err != nil {
			return err
		}
		return writePrivateJSON(environment)
	case "show":
		flags := privateFlagSet("private-inventory environment show")
		stateDir := flags.String("state-dir", "", "absolute private inventory state directory")
		id := flags.String("id", "", "registered Environment UUID")
		revision := flags.Int64("revision", 0, "Environment revision; zero selects current")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || *stateDir == "" || *id == "" || *revision < 0 {
			return errors.New("environment show requires --state-dir ABS --id UUID [--revision N]")
		}
		store, err := domainstore.Open(*stateDir, os.Geteuid())
		if err != nil {
			return err
		}
		var environment *domain.Environment
		if *revision == 0 {
			environment, err = store.CurrentEnvironment(context.Background(), store.Scope(), *id)
		} else {
			var record domain.Record
			record, err = store.Get(context.Background(), store.Scope(), domain.RecordRef{TenantScope: store.Scope(), Kind: domain.KindEnvironment, ID: *id, Revision: *revision})
			if err == nil {
				environment, _ = record.(*domain.Environment)
				if environment == nil {
					err = errors.New("record is not an Environment")
				}
			}
		}
		if err != nil {
			return err
		}
		return writePrivateJSON(environment)
	default:
		return fmt.Errorf("unknown environment command %q", args[0])
	}
}

func runPrivateInventoryDiscover(args []string) error {
	flags := privateFlagSet("private-inventory discover")
	stateDir := flags.String("state-dir", "", "absolute private inventory state directory")
	environmentID := flags.String("environment-id", "", "registered Environment UUID")
	revision := flags.Int64("revision", 0, "exact Environment revision")
	resourceKinds := flags.String("resource-kinds", "", "comma-separated runtime resource subset")
	namespaces := flags.String("namespaces", "", "comma-separated registered Kubernetes namespace subset")
	includeNodes := flags.Bool("include-nodes", false, "include authorized Kubernetes Node summaries")
	save := flags.Bool("save", false, "record this same preview after discovery")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *stateDir == "" || *environmentID == "" || *revision <= 0 {
		return errors.New("discover requires --state-dir ABS --environment-id UUID --revision N")
	}
	supplied := map[string]bool{}
	flags.Visit(func(value *flag.Flag) { supplied[value.Name] = true })
	parsedResourceKinds, err := splitPrivateList(*resourceKinds, supplied["resource-kinds"])
	if err != nil {
		return fmt.Errorf("invalid --resource-kinds: %w", err)
	}
	parsedNamespaces, err := splitPrivateList(*namespaces, supplied["namespaces"])
	if err != nil {
		return fmt.Errorf("invalid --namespaces: %w", err)
	}
	store, err := domainstore.Open(*stateDir, os.Geteuid())
	if err != nil {
		return err
	}
	service, err := newPrivateInventoryService(store)
	if err != nil {
		return err
	}
	principal := localPrivatePrincipal()
	preview, err := service.Discover(context.Background(), privateinventory.DiscoverRequest{
		Principal: principal, EnvironmentID: *environmentID, Revision: *revision,
		ResourceKinds: parsedResourceKinds, Namespaces: parsedNamespaces,
		IncludeNodes: *includeNodes,
	})
	if err != nil {
		return err
	}
	if preview.Preview.CursorHandle != "" {
		preview.Preview.CursorHandle = ""
		fmt.Fprintln(os.Stderr, "discovery has more Kubernetes pages; use the persistent stdio MCP service to run and continue paged discovery")
	}
	output := privateDiscoverOutput{Preview: preview.Preview, Snapshot: preview.Snapshot}
	if *save {
		ref, saveErr := service.Record(context.Background(), principal, preview.Preview.Handle, preview.Preview.Digest)
		if saveErr != nil {
			return saveErr
		}
		output.SavedRef = &ref
	}
	return writePrivateJSON(output)
}

func runPrivateInventoryRecord(args []string) error {
	flags := privateFlagSet("private-inventory record")
	stateDir := flags.String("state-dir", "", "absolute private inventory state directory")
	input := flags.String("input", "", "strict, redacted InventorySnapshot JSON file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *stateDir == "" || *input == "" {
		return errors.New("record requires --state-dir ABS --input FILE")
	}
	raw, err := readProtectedPrivateFile(*input, maxPrivateInputBytes)
	if err != nil {
		return err
	}
	snapshot, err := domain.DecodeInventorySnapshot(raw)
	if err != nil {
		return errors.New("input is not a valid redacted private inventory snapshot")
	}
	store, err := domainstore.Open(*stateDir, os.Geteuid())
	if err != nil {
		return err
	}
	environment, err := store.CurrentEnvironment(context.Background(), store.Scope(), snapshot.Spec.EnvironmentRef.ID)
	if err != nil {
		return err
	}
	if environment.Reference() != snapshot.Spec.EnvironmentRef || !environment.Spec.Enabled {
		return errors.New("snapshot does not reference the current enabled Environment revision")
	}
	ref, err := store.SaveSnapshot(context.Background(), store.Scope(), snapshot)
	if err != nil {
		return err
	}
	return writePrivateJSON(ref)
}

func runPrivateInventoryList(args []string) error {
	flags := privateFlagSet("private-inventory list")
	stateDir := flags.String("state-dir", "", "absolute private inventory state directory")
	kindText := flags.String("kind", "", "optional Environment or InventorySnapshot")
	limit := flags.Int("limit", domain.DefaultPageSize, "maximum records, at most 100")
	afterText := flags.String("after", "", "trusted CLI continuation Kind/UUID/revision from a prior page")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *stateDir == "" {
		return errors.New("list requires --state-dir ABS")
	}
	kind, err := privateKind(*kindText, true)
	if err != nil {
		return err
	}
	store, err := domainstore.Open(*stateDir, os.Geteuid())
	if err != nil {
		return err
	}
	var after *domain.RecordRef
	if strings.TrimSpace(*afterText) != "" {
		parsed, parseErr := parsePrivateAfter(store.Scope(), *afterText)
		if parseErr != nil {
			return parseErr
		}
		if kind != "" && parsed.Kind != kind {
			return errors.New("--after kind must match --kind")
		}
		after = &parsed
	}
	page, err := store.ListPage(context.Background(), store.Scope(), domainstore.ListOptions{Kind: kind, Limit: *limit, After: after})
	if err != nil {
		return err
	}
	return writePrivateJSON(struct {
		Records []domainstore.ListEntry `json:"records"`
		HasMore bool                    `json:"hasMore"`
		NextRef *domain.RecordRef       `json:"nextRef,omitempty"`
	}{Records: page.Entries, HasMore: page.HasMore, NextRef: page.NextRef})
}

func parsePrivateAfter(scope, value string) (domain.RecordRef, error) {
	parts := strings.Split(value, "/")
	if len(parts) != 3 || parts[0] == "" || !privateUUIDPattern.MatchString(parts[1]) {
		return domain.RecordRef{}, errors.New("--after must be Kind/UUID/revision")
	}
	kind, err := privateKind(parts[0], false)
	if err != nil {
		return domain.RecordRef{}, err
	}
	if !privatePositiveIntPattern.MatchString(parts[2]) {
		return domain.RecordRef{}, errors.New("--after revision must be a positive integer")
	}
	revision, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || revision > domain.MaxSafeInteger {
		return domain.RecordRef{}, errors.New("--after revision must be a positive integer")
	}
	return domain.RecordRef{TenantScope: scope, Kind: kind, ID: parts[1], Revision: revision}, nil
}

func runPrivateInventoryShow(args []string, verify bool) error {
	name := "private-inventory show"
	if verify {
		name = "private-inventory verify"
	}
	flags := privateFlagSet(name)
	stateDir := flags.String("state-dir", "", "absolute private inventory state directory")
	kindText := flags.String("kind", "", "Environment or InventorySnapshot")
	id := flags.String("id", "", "record UUID")
	revision := flags.Int64("revision", 0, "exact immutable revision")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *stateDir == "" || *kindText == "" || *id == "" || *revision <= 0 {
		return fmt.Errorf("%s requires --state-dir ABS --kind KIND --id UUID --revision N", name)
	}
	kind, err := privateKind(*kindText, false)
	if err != nil {
		return err
	}
	store, err := domainstore.Open(*stateDir, os.Geteuid())
	if err != nil {
		return err
	}
	ref := domain.RecordRef{TenantScope: store.Scope(), Kind: kind, ID: *id, Revision: *revision}
	if verify {
		record, verifyErr := store.Verify(context.Background(), store.Scope(), ref)
		if verifyErr != nil {
			return verifyErr
		}
		return writePrivateJSON(struct {
			Reference domain.RecordRef `json:"reference"`
			Valid     bool             `json:"valid"`
		}{record.Reference(), true})
	}
	record, err := store.Get(context.Background(), store.Scope(), ref)
	if err != nil {
		return err
	}
	return writePrivateJSON(record)
}

func privateFlagSet(name string) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	return flags
}

func readPrivateConnection(path string) (domainstore.Connection, error) {
	raw, err := readProtectedPrivateFile(path, 64<<10)
	if err != nil {
		return domainstore.Connection{}, err
	}
	connection, err := domainstore.DecodeConnection(raw)
	if err != nil {
		return domainstore.Connection{}, errors.New("registration input is invalid")
	}
	return connection, nil
}

func readProtectedPrivateFile(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, errors.New("inspect private inventory input")
	}
	if !info.Mode().IsRegular() || info.Mode()&0o077 != 0 {
		return nil, errors.New("private inventory input must be a protected regular file")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != os.Geteuid() {
		return nil, errors.New("private inventory input must be owned by the effective process UID")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("open private inventory input")
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || openedInfo.Mode()&0o077 != 0 || !os.SameFile(info, openedInfo) {
		return nil, errors.New("private inventory input changed while opening")
	}
	if stat, ok := openedInfo.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != os.Geteuid() {
		return nil, errors.New("private inventory input must be owned by the effective process UID")
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, errors.New("read private inventory input")
	}
	if int64(len(raw)) > limit {
		return nil, errors.New("private inventory input exceeds byte limit")
	}
	return raw, nil
}

func privateKind(value string, allowEmpty bool) (domain.Kind, error) {
	switch domain.Kind(value) {
	case "":
		if allowEmpty {
			return "", nil
		}
	case domain.KindEnvironment:
		return domain.KindEnvironment, nil
	case domain.KindInventorySnapshot:
		return domain.KindInventorySnapshot, nil
	}
	return "", errors.New("kind must be Environment or InventorySnapshot")
}

func splitPrivateList(value string, supplied bool) ([]string, error) {
	if !supplied {
		return nil, nil
	}
	if strings.TrimSpace(value) == "" {
		return nil, errors.New("explicit list cannot be empty")
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	seen := make(map[string]bool, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" || seen[trimmed] {
			return nil, errors.New("list contains an empty or duplicate item")
		}
		seen[trimmed] = true
		result = append(result, trimmed)
	}
	return result, nil
}

func localPrivatePrincipal() string {
	return "uid:" + strconv.Itoa(os.Geteuid())
}

func writePrivateJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}
