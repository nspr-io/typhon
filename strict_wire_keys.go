package typhon

// nspr-io patch: wire-format strict key checker.
//
// This file is NOT part of upstream monzo/typhon. It adds a pre-decode
// validation pass used by the e2e test harness so that a client (or
// server) which sends a proto message with unexpected JSON keys fails
// the test instead of being silently tolerated.
//
// Background: `encoding/json` (used for request decoding in
// request.go) is case-insensitive when matching JSON keys to Go struct
// fields, and `protojson.Unmarshal` (used for response decoding in
// response.go) accepts BOTH the proto field name (snake_case) AND the
// json_name (camelCase) per the proto3 JSON spec. In both cases, the
// wire format of the TS monolith could drift (e.g. emit
// `icon_file_id` when Go emits `iconFileId`) without any test noticing
// because the decoders map both into the same struct field. This
// pre-pass enforces json_name exactness so wire-format divergence is
// a hard failure.
//
// Toggle: typhon.OptionStrictWireKeys (bool). Off by default. Flipped
// on inside testharness.New() so only e2e tests are affected.
//
// Keep this in sync with re-vendoring of monzo/typhon. If the upstream
// ever accepts a similar knob, rip this file out and use theirs.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// OptionStrictWireKeys, when true, causes typhon request.Decode and
// response.Decode (for proto.Message values) to reject any JSON object
// whose keys do not match the proto json_name (camelCase) exactly.
// Both directions must emit camelCase. Checks recurse into nested
// proto messages. See doc at top of file.
var OptionStrictWireKeys bool

// ValidateStrictWireKeys walks a proto JSON body and ensures every key
// matches the proto json_name (camelCase). Nested messages (including
// repeated and map value messages) are recursed into.
//
// On success returns nil. On mismatch returns a human-readable error
// describing the offending key and the closest expected name.
//
// Exported so other vendored packages (e.g. libraries/streams for
// Kafka body validation) can reuse the same walker as the typhon HTTP
// decoders.
func ValidateStrictWireKeys(body []byte, desc protoreflect.MessageDescriptor) error {
	if len(body) == 0 {
		return nil
	}
	var raw interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		// Leave the real decoder to produce a pretty JSON-syntax error.
		return nil
	}
	return walkStrict(raw, desc, "")
}

func walkStrict(v interface{}, desc protoreflect.MessageDescriptor, path string) error {
	obj, ok := v.(map[string]interface{})
	if !ok {
		// Only objects need key validation. Scalars, arrays-of-scalars,
		// and null pass through unchanged.
		return nil
	}
	fields := desc.Fields()
	for _, key := range sortedKeys(obj) {
		fd := fields.ByJSONName(key)
		if fd == nil {
			return fmt.Errorf(
				"wire-format: unexpected key %q at %s (allowed json_names: %s)",
				key, displayPath(path, key), allowedNames(fields),
			)
		}
		if key != fd.JSONName() {
			return fmt.Errorf(
				"wire-format: key %q at %s does not match proto json_name (expected %q)",
				key, displayPath(path, key), fd.JSONName(),
			)
		}
		// Recurse into nested message-typed fields.
		if err := recurseInto(obj[key], fd, displayPath(path, key)); err != nil {
			return err
		}
	}
	return nil
}

// recurseInto walks into message-typed fields so the check applies at
// any depth. Map<string, Message> is handled by treating each map
// value as an independent message of the map's value descriptor.
func recurseInto(v interface{}, fd protoreflect.FieldDescriptor, path string) error {
	switch {
	case fd.IsMap():
		// Only message-valued maps need recursion. Scalar value maps
		// are JSON objects keyed by arbitrary strings and don't have
		// their own json_name space.
		valFD := fd.MapValue()
		if valFD.Kind() != protoreflect.MessageKind {
			return nil
		}
		m, ok := v.(map[string]interface{})
		if !ok {
			return nil
		}
		for mk, mv := range m {
			if err := walkStrict(mv, valFD.Message(), path+"[\""+mk+"\"]"); err != nil {
				return err
			}
		}
	case fd.IsList():
		arr, ok := v.([]interface{})
		if !ok {
			return nil
		}
		if fd.Kind() != protoreflect.MessageKind {
			return nil
		}
		for i, el := range arr {
			if err := walkStrict(el, fd.Message(), fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	case fd.Kind() == protoreflect.MessageKind:
		if err := walkStrict(v, fd.Message(), path); err != nil {
			return err
		}
	}
	return nil
}

func displayPath(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

func sortedKeys(m map[string]interface{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// allowedNames returns a sorted, comma-separated list of the accepted
// json_names for the proto message (used in error messages).
func allowedNames(fields protoreflect.FieldDescriptors) string {
	out := make([]string, fields.Len())
	for i := 0; i < fields.Len(); i++ {
		out[i] = fields.Get(i).JSONName()
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}
