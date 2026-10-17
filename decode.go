package serper

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"
)

// decodeError is a decoding failure at a JSON location, such as "organic[3]".
// It becomes a CallError once the call's metadata is known.
type decodeError struct {
	kind  FailureKind
	cause error
	field string

	// validJSON reports that the body was valid UTF-8 and one valid JSON
	// value, so that a raw capture may keep it.
	validJSON bool
}

// malformed returns a malformed_response failure at field of a valid JSON
// body.
func malformed(cause error, field string) *decodeError {
	return &decodeError{kind: FailureMalformedResponse, cause: cause, field: field, validJSON: true}
}

// nullItem returns the failure of a null item at index i of the list field.
func nullItem(field string, i int) *decodeError {
	return malformed(errNullItem, field+"["+strconv.Itoa(i)+"]")
}

// callError returns the CallError for e on a body that came with meta.
func (e *decodeError) callError(meta Metadata) *CallError {
	ce := newCallError(e.kind, meta, e.cause)
	ce.Field = e.field
	return ce
}

// decodeObject checks that data is valid UTF-8 and one JSON object, with
// nothing but whitespace after it, and unmarshals it into the mirror m.
// Unknown members are ignored; members match as encoding/json matches them:
// the exact name first, else the name ignoring case. A known member of the
// wrong JSON type fails as malformed at its member path, such as
// "organic.position" (without array indexes, in the SDK's own member names:
// see canonicalField); the error's own text, which can quote the value, is
// never kept. The rest of the body is still decoded, so that credits can be
// read from a body that fails.
func decodeObject(data []byte, m any) *decodeError {
	if !utf8.Valid(data) {
		return &decodeError{kind: FailureMalformedResponse, cause: errInvalidUTF8}
	}
	if first := bytes.TrimLeft(data, " \t\r\n"); len(first) == 0 || first[0] != '{' {
		if !json.Valid(data) {
			return &decodeError{kind: FailureMalformedResponse, cause: errInvalidJSON}
		}
		return malformed(errNotObject, "")
	}
	err := json.Unmarshal(data, m)
	if err == nil {
		return nil
	}
	if te, ok := errors.AsType[*json.UnmarshalTypeError](err); ok {
		return malformed(errWrongType, canonicalField(reflect.TypeOf(m), te.Field))
	}
	// The input is an object, so the only other failure is a syntax error:
	// invalid JSON, or data after the object.
	return &decodeError{kind: FailureMalformedResponse, cause: errInvalidJSON}
}

// canonicalField rewrites the dotted member path of an *json.UnmarshalTypeError
// against the mirror type t into the SDK's own member names, without array
// indexes, such as "organic.sitelinks.link". encoding/json reports the path
// in the mirror's tag names on its classic implementation, but in the input's
// own spelling and with array indexes ("ORGANIC.0.SiteLinks.1.link") when it
// runs on its v2 implementation (the default from Go 1.27, or
// GOEXPERIMENT=jsonv2). Rewriting makes Field the same on every toolchain and
// guarantees it holds only the SDK's names: a segment that names no member of
// the mirror makes the whole path "".
func canonicalField(t reflect.Type, path string) string {
	if path == "" {
		return ""
	}
	var names []string
	for seg := range strings.SplitSeq(path, ".") {
		if seg != "" && asciiRange(seg, '0', '9') {
			continue // an array index
		}
		for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice {
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct {
			return ""
		}
		f, name, ok := memberNamed(t, seg)
		if !ok {
			return ""
		}
		names = append(names, name)
		t = f.Type
	}
	return strings.Join(names, ".")
}

// memberNamed returns the field of the struct type t whose JSON member name is
// seg, matched as encoding/json matches it (exactly first, then ignoring
// case), and that member name.
func memberNamed(t reflect.Type, seg string) (reflect.StructField, string, bool) {
	var folded reflect.StructField
	var foldedName string
	for f := range t.Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		switch {
		case name == "" || name == "-":
		case name == seg:
			return f, name, true
		case foldedName == "" && strings.EqualFold(name, seg):
			folded, foldedName = f, name
		}
	}
	return folded, foldedName, foldedName != ""
}

// creditsOf reads the top-level "credits" member, raw as sent: absent or null
// is not reported; plain decimal digits that fit an int64 are reported; any
// other value, such as 2.0, 2e0, -1, a number too large, a string, or a
// value of another type, is invalid and dropped. It never fails.
func creditsOf(raw json.RawMessage) Credits {
	if raw == nil || string(raw) == "null" {
		return Credits{}
	}
	if !asciiRange(string(raw), '0', '9') {
		return Credits{Status: CreditsInvalid}
	}
	n, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil {
		return Credits{Status: CreditsInvalid}
	}
	return Credits{Status: CreditsReported, Used: &n}
}

// Attribute is one member of a provider attributes object, such as
// {"Founded": "1976"}.
type Attribute struct {
	Name  string
	Value string
}

// Attributes holds a provider attributes object in the provider's member
// order, duplicates included. It marshals back to an ordered JSON object, so a
// stored result round-trips unchanged. A nil Attributes means the member was
// absent or null.
type Attributes []Attribute

// Get returns the value of the first attribute called name, matched exactly,
// and whether there is one.
func (a Attributes) Get(name string) (string, bool) {
	for _, at := range a {
		if at.Name == name {
			return at.Value, true
		}
	}
	return "", false
}

// MarshalJSON writes the attributes as a JSON object, in order, duplicates
// included; nil is null.
func (a Attributes) MarshalJSON() ([]byte, error) {
	if a == nil {
		return []byte("null"), nil
	}
	b := []byte{'{'}
	for i, at := range a {
		if i > 0 {
			b = append(b, ',')
		}
		b = appendJSONString(b, at.Name)
		b = append(b, ':')
		b = appendJSONString(b, at.Value)
	}
	return append(b, '}'), nil
}

// UnmarshalJSON reads a JSON object whose values are all strings, keeping
// member order and duplicates; null leaves a nil Attributes. Anything else
// fails with an error that never quotes the input.
func (a *Attributes) UnmarshalJSON(data []byte) error {
	list, ok := parseAttributes(data)
	if !ok {
		return errAttributes
	}
	*a = list
	return nil
}

// errAttributes is the error of Attributes.UnmarshalJSON.
var errAttributes = errors.New("serper: attributes must be a JSON object of strings")

// attributesMirror decodes an attributes member without ever failing the
// surrounding json.Unmarshal: it records whether the value had the right
// shape, so that the decoder can report the failure with its exact location.
type attributesMirror struct {
	list Attributes
	bad  bool
}

// UnmarshalJSON records the attributes, or that they were not an object of
// strings. A body that repeats the member keeps the last copy, as encoding/json
// does, but a bad copy stays bad, as encoding/json keeps the first type error
// of a repeated member of any other type.
func (m *attributesMirror) UnmarshalJSON(data []byte) error {
	list, ok := parseAttributes(data)
	m.list, m.bad = list, m.bad || !ok
	return nil
}

// parseAttributes reads data as one JSON object of strings in order, or null
// as nil, with nothing after it but whitespace. ok is false for anything else.
func parseAttributes(data []byte) (Attributes, bool) {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, false
	}
	if tok == nil {
		return nil, atEOF(dec)
	}
	if d, isDelim := tok.(json.Delim); !isDelim || d != '{' {
		return nil, false
	}
	list := Attributes{}
	for dec.More() {
		nameTok, err := dec.Token()
		if err != nil {
			return nil, false
		}
		valueTok, err := dec.Token()
		if err != nil {
			return nil, false
		}
		value, isString := valueTok.(string)
		if !isString {
			return nil, false
		}
		name, _ := nameTok.(string) // inside an object, a key is always a string
		list = append(list, Attribute{Name: name, Value: value})
	}
	if _, err := dec.Token(); err != nil || !atEOF(dec) {
		return nil, false
	}
	return list, true
}

// atEOF reports whether dec has nothing left but whitespace.
func atEOF(dec *json.Decoder) bool {
	_, err := dec.Token()
	return errors.Is(err, io.EOF)
}

// appendJSONString appends s to b as a JSON string literal, escaped as
// encoding/json escapes it but without HTML escaping: encoding/json escapes
// the output of a MarshalJSON method itself, as its encoder is set to, so an
// encoder with SetEscapeHTML(false) leaves <, > and & bare here too.
func appendJSONString(b []byte, s string) []byte {
	return append(b, marshalNoEscape(s)...)
}

// marshalNoEscape encodes v as JSON without HTML escaping and without a
// trailing newline; encoding/json still escapes U+2028 and U+2029. v must be
// a value that always encodes: a string, or a wire struct of strings already
// checked to be valid UTF-8, integers and booleans.
func marshalNoEscape(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v) //nolint:errchkjson // v always encodes (see above), and a bytes.Buffer never fails a write
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

// rawBody holds a captured body. It is a function value, so that fmt and
// reflection-based dumpers print it only as an address, and it is nil when
// capture was off.
type rawBody func() []byte

// newRawBody returns the holder of data, at its exact size (see exactSize).
func newRawBody(data []byte) rawBody {
	data = exactSize(data)
	return func() []byte { return data }
}

// exactSize returns data with its capacity cut to its length: data itself
// when it has at most one spare byte of capacity, as a body read to its
// declared length has, else a copy, so that a body that grew while it was
// read does not pin its spare capacity.
func exactSize(data []byte) []byte {
	if cap(data)-len(data) > 1 {
		return cloneBytes(data)
	}
	return data[:len(data):len(data)]
}

// copyOut returns a new copy of the captured body and true, or (nil, false)
// when nothing was captured.
func (r rawBody) copyOut() ([]byte, bool) {
	if r == nil {
		return nil, false
	}
	return cloneBytes(r()), true
}

// cloneBytes returns a copy of b at its exact size.
func cloneBytes(b []byte) []byte {
	out := make([]byte, len(b))
	copy(out, b)
	return out
}
