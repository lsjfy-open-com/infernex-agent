// Package inventoryview exposes a deliberately small, read-only HTTP view of
// the private deployment inventory. The caller supplies both the trusted
// tenant scope and the single-operator bearer token; neither can be selected
// by an HTTP client.
package inventoryview

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/domain"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/domainstore"
)

const (
	apiPrefix      = "/api/v1/inventory/"
	maxBearerBytes = 4096
	maxCursorBytes = 2048
	maxResponse    = domain.MaxToolResponseBytes
)

var canonicalUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// Reader is the fixed-scope, store-backed read surface used by the dashboard.
// *domainstore.Store satisfies it. Connection and registration write methods
// are intentionally absent.
type Reader interface {
	ListPage(context.Context, string, domainstore.ListOptions) (domainstore.ListPage, error)
	Get(context.Context, string, domain.RecordRef) (domain.Record, error)
}

type server struct {
	store     Reader
	scope     string
	token     string
	cursorKey [32]byte
	badCfg    bool
}

// New returns the private inventory API handler. Configuration failure is
// represented by a handler that returns a safe internal error, which keeps the
// constructor convenient for direct http.ServeMux composition without ever
// weakening authentication.
func New(store Reader, scope, token string) http.Handler {
	return newWithEntropy(store, scope, token, rand.Reader)
}

func newWithEntropy(store Reader, scope, token string, entropy io.Reader) http.Handler {
	s := &server{store: store, scope: scope, token: token}
	s.badCfg = nilReader(store) || scope == "" || scope != strings.TrimSpace(scope) || len(scope) > domain.MaxStringBytes ||
		len(token) < 32 || len(token) > maxBearerBytes || strings.ContainsAny(token, " \t\r\n\x00")
	if !s.badCfg {
		if entropy == nil {
			s.badCfg = true
		} else if _, err := io.ReadFull(entropy, s.cursorKey[:]); err != nil {
			s.badCfg = true
		}
	}
	return s
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w.Header())
	if s.badCfg {
		writeError(w, http.StatusInternalServerError, "server_error", "inventory view is not configured")
		return
	}
	if len(r.Header.Values("Origin")) != 0 {
		writeError(w, http.StatusForbidden, "cross_origin_denied", "cross-origin requests are not allowed")
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only GET is allowed")
		return
	}
	if !s.authorized(r.Header.Values("Authorization")) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="infernex-private-inventory"`)
		writeError(w, http.StatusUnauthorized, "unauthorized", "a valid bearer token is required")
		return
	}
	if r.URL.Path == apiPrefix+"records" {
		s.serveRecords(w, r)
		return
	}
	if r.URL.Path == apiPrefix+"record" {
		s.serveRecord(w, r)
		return
	}
	if r.URL.Path == apiPrefix+"diff" {
		s.serveDiff(w, r)
		return
	}
	writeError(w, http.StatusNotFound, "not_found", "API route was not found")
}

func nilReader(reader Reader) bool {
	if reader == nil {
		return true
	}
	value := reflect.ValueOf(reader)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func (s *server) authorized(values []string) bool {
	if len(values) != 1 {
		return false
	}
	want := sha256.Sum256([]byte("Bearer " + s.token))
	got := sha256.Sum256([]byte(values[0]))
	return subtle.ConstantTimeCompare(got[:], want[:]) == 1
}

func (s *server) serveRecords(w http.ResponseWriter, r *http.Request) {
	query, err := strictQuery(r.URL, "kind", "limit", "after")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "records query is invalid")
		return
	}
	var options domainstore.ListOptions
	if after := query.Get("after"); after != "" {
		if len(query) != 1 {
			writeError(w, http.StatusBadRequest, "invalid_request", "after cannot be combined with list filters")
			return
		}
		cursor, cursorErr := s.decodeCursor(after)
		if cursorErr != nil {
			writeError(w, http.StatusBadRequest, "invalid_cursor", "after cursor is invalid")
			return
		}
		options.Kind, options.Limit = cursor.Kind, cursor.Limit
		options.After = &domain.RecordRef{TenantScope: s.scope, Kind: cursor.After.Kind, ID: cursor.After.ID, Revision: cursor.After.Revision}
	} else {
		if _, present := query["after"]; present {
			writeError(w, http.StatusBadRequest, "invalid_cursor", "after cursor is invalid")
			return
		}
		var kind domain.Kind
		if _, present := query["kind"]; present {
			kind, err = parseRequiredKind(query.Get("kind"))
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid_request", "kind must be Environment or InventorySnapshot")
				return
			}
		}
		limit := domain.DefaultPageSize
		if _, present := query["limit"]; present {
			limit, err = parseLimit(query.Get("limit"))
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid_request", "limit must be an integer between 1 and 100")
				return
			}
		}
		options.Kind, options.Limit = kind, limit
	}

	page, err := s.store.ListPage(r.Context(), s.scope, options)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if len(page.Entries) > options.Limit || len(page.Entries) > domain.MaxPageSize {
		writeError(w, http.StatusInternalServerError, "server_error", "inventory store exceeded the page limit")
		return
	}
	output := listResponse{Records: make([]listRecord, 0, len(page.Entries)), Returned: len(page.Entries), Truncated: page.HasMore}
	for _, entry := range page.Entries {
		if entry.Reference.TenantScope != s.scope || !validRef(entry.Reference, "") {
			writeError(w, http.StatusInternalServerError, "server_error", "inventory store returned an invalid record")
			return
		}
		if entry.Status != domainstore.StatusOK && entry.Status != domainstore.StatusCorrupt {
			writeError(w, http.StatusInternalServerError, "server_error", "inventory store returned an invalid record status")
			return
		}
		problem := ""
		if entry.Status == domainstore.StatusCorrupt {
			problem = "record integrity verification failed"
		}
		output.Records = append(output.Records, listRecord{
			Reference: publicRef(entry.Reference), Digest: entry.Digest, CreatedAt: entry.CreatedAt,
			Status: entry.Status, Problem: problem,
		})
	}
	if page.HasMore {
		if page.NextRef == nil || page.NextRef.TenantScope != s.scope || !validRef(*page.NextRef, options.Kind) {
			writeError(w, http.StatusInternalServerError, "server_error", "inventory store returned an invalid page")
			return
		}
		output.Next = s.encodeCursor(listCursor{Version: 1, Kind: options.Kind, Limit: options.Limit, After: publicRef(*page.NextRef)})
	}
	writeBoundedJSON(w, http.StatusOK, output)
}

func (s *server) serveRecord(w http.ResponseWriter, r *http.Request) {
	query, err := strictQuery(r.URL, "kind", "id", "revision")
	if err != nil || len(query) != 3 {
		writeError(w, http.StatusBadRequest, "invalid_request", "kind, id, and revision are required")
		return
	}
	ref, err := s.refFromQuery(query)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "record reference is invalid")
		return
	}
	record, err := s.store.Get(r.Context(), s.scope, ref)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if record == nil || record.Reference() != ref {
		writeError(w, http.StatusInternalServerError, "server_error", "inventory store returned an invalid record")
		return
	}
	view, err := safeRecord(record, s.scope)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server_error", "inventory record could not be rendered")
		return
	}
	writeBoundedJSON(w, http.StatusOK, recordResponse{Record: view})
}

func (s *server) refFromQuery(query url.Values) (domain.RecordRef, error) {
	kind, err := parseRequiredKind(query.Get("kind"))
	if err != nil || !canonicalUUID.MatchString(query.Get("id")) {
		return domain.RecordRef{}, errors.New("invalid reference")
	}
	revision, err := strconv.ParseInt(query.Get("revision"), 10, 64)
	if err != nil || revision < 1 || revision > domain.MaxSafeInteger || strconv.FormatInt(revision, 10) != query.Get("revision") {
		return domain.RecordRef{}, errors.New("invalid reference")
	}
	if kind == domain.KindInventorySnapshot && revision != 1 {
		return domain.RecordRef{}, errors.New("invalid snapshot revision")
	}
	return domain.RecordRef{TenantScope: s.scope, Kind: kind, ID: query.Get("id"), Revision: revision}, nil
}

type publicRecordRef struct {
	Kind     domain.Kind `json:"kind"`
	ID       string      `json:"id"`
	Revision int64       `json:"revision"`
}

func publicRef(ref domain.RecordRef) publicRecordRef {
	return publicRecordRef{Kind: ref.Kind, ID: ref.ID, Revision: ref.Revision}
}

type listRecord struct {
	Reference publicRecordRef          `json:"reference"`
	Digest    string                   `json:"digest,omitempty"`
	CreatedAt string                   `json:"createdAt,omitempty"`
	Status    domainstore.RecordStatus `json:"status"`
	Problem   string                   `json:"problem,omitempty"`
}

type listResponse struct {
	Records   []listRecord `json:"records"`
	Returned  int          `json:"returned"`
	Truncated bool         `json:"truncated"`
	Next      string       `json:"next,omitempty"`
}

type recordResponse struct {
	Record any `json:"record"`
}

type listCursor struct {
	Version int             `json:"v"`
	Kind    domain.Kind     `json:"kind,omitempty"`
	Limit   int             `json:"limit"`
	After   publicRecordRef `json:"after"`
}

func (s *server) encodeCursor(cursor listCursor) string {
	payload, _ := json.Marshal(cursor)
	mac := s.cursorMAC(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac)
}

func (s *server) decodeCursor(encoded string) (listCursor, error) {
	var cursor listCursor
	if encoded == "" || len(encoded) > maxCursorBytes || strings.Count(encoded, ".") != 1 {
		return cursor, errors.New("invalid cursor")
	}
	parts := strings.SplitN(encoded, ".", 2)
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return cursor, errors.New("invalid cursor")
	}
	provided, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return cursor, errors.New("invalid cursor")
	}
	want := s.cursorMAC(payload)
	if !hmac.Equal(provided, want) {
		return cursor, errors.New("invalid cursor")
	}
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&cursor); err != nil || cursor.Version != 1 || cursor.Limit < 1 || cursor.Limit > domain.MaxPageSize ||
		(cursor.Kind != "" && cursor.Kind != domain.KindEnvironment && cursor.Kind != domain.KindInventorySnapshot) ||
		!validPublicRef(cursor.After, cursor.Kind) {
		return listCursor{}, errors.New("invalid cursor")
	}
	var extra any
	if decoder.Decode(&extra) == nil {
		return listCursor{}, errors.New("invalid cursor")
	}
	return cursor, nil
}

func (s *server) cursorMAC(payload []byte) []byte {
	mac := hmac.New(sha256.New, s.cursorKey[:])
	_, _ = mac.Write([]byte("infernex-private-inventory-cursor-v1\x00"))
	_, _ = mac.Write([]byte(s.scope))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(payload)
	return mac.Sum(nil)
}

func strictQuery(u *url.URL, allowed ...string) (url.Values, error) {
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, err
	}
	wanted := make(map[string]bool, len(allowed))
	for _, key := range allowed {
		wanted[key] = true
	}
	for key, values := range query {
		if !wanted[key] || len(values) != 1 {
			return nil, errors.New("invalid query")
		}
	}
	return query, nil
}

func parseRequiredKind(value string) (domain.Kind, error) {
	kind := domain.Kind(value)
	if kind != domain.KindEnvironment && kind != domain.KindInventorySnapshot {
		return "", errors.New("invalid kind")
	}
	return kind, nil
}

func parseLimit(value string) (int, error) {
	if value == "" {
		return 0, errors.New("invalid limit")
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit < 1 || limit > domain.MaxPageSize || strconv.Itoa(limit) != value {
		return 0, errors.New("invalid limit")
	}
	return limit, nil
}

func validRef(ref domain.RecordRef, kind domain.Kind) bool {
	return ref.TenantScope != "" && validPublicRef(publicRef(ref), kind)
}

func validPublicRef(ref publicRecordRef, kind domain.Kind) bool {
	if (ref.Kind != domain.KindEnvironment && ref.Kind != domain.KindInventorySnapshot) || (kind != "" && ref.Kind != kind) ||
		!canonicalUUID.MatchString(ref.ID) || ref.Revision < 1 || ref.Revision > domain.MaxSafeInteger {
		return false
	}
	return ref.Kind != domain.KindInventorySnapshot || ref.Revision == 1
}

func setSecurityHeaders(header http.Header) {
	header.Set("Cache-Control", "no-store")
	header.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Content-Type-Options", "nosniff")
}

func writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case domainstore.IsCode(err, domainstore.CodeNotFound):
		writeError(w, http.StatusNotFound, "not_found", "inventory record was not found")
	case domainstore.IsCode(err, domainstore.CodeInvalidArgument):
		writeError(w, http.StatusBadRequest, "invalid_request", "inventory request is invalid")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded), domainstore.IsCode(err, domainstore.CodeCanceled):
		writeError(w, http.StatusServiceUnavailable, "unavailable", "inventory request was canceled")
	default:
		writeError(w, http.StatusInternalServerError, "server_error", "inventory store could not complete the request")
	}
}

func writeBoundedJSON(w http.ResponseWriter, status int, value any) {
	payload, err := json.Marshal(value)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server_error", "response could not be encoded")
		return
	}
	if len(payload) > maxResponse {
		writeError(w, http.StatusRequestEntityTooLarge, "response_too_large", fmt.Sprintf("response exceeds the %d byte limit", maxResponse))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(append(payload, '\n'))
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	type errorBody struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	var body errorBody
	body.Error.Code, body.Error.Message = code, message
	payload, _ := json.Marshal(body)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(append(payload, '\n'))
}
