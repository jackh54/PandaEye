package codec

import (
	"strings"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const maxNesting = 32

// sanitizeUTF8 rewrites a serialised message, replacing invalid UTF-8 in every string field (at any
// depth, including map keys and values) with U+FFFD. proto3 unmarshalling rejects invalid UTF-8,
// while the protocol replaces it. changed is false when nothing needed replacing.
func sanitizeUTF8(md protoreflect.MessageDescriptor, b []byte, max int) (out []byte, changed bool, err error) {
	out, changed, err = sanitizeMessage(md, b, 0)
	if err != nil {
		return nil, false, err
	}
	if len(out) > max {
		return nil, false, tooLarge("message exceeds %d bytes after replacing invalid UTF-8", max)
	}
	return out, changed, nil
}

func sanitizeMessage(md protoreflect.MessageDescriptor, b []byte, depth int) ([]byte, bool, error) {
	if depth > maxNesting {
		return nil, false, malformed("message nested too deeply")
	}
	out := make([]byte, 0, len(b))
	changed := false
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return nil, false, malformed("%v", protowire.ParseError(n))
		}
		tag := b[:n]
		m := protowire.ConsumeFieldValue(num, typ, b[n:])
		if m < 0 {
			return nil, false, malformed("%v", protowire.ParseError(m))
		}
		field := b[:n+m]
		b = b[n+m:]
		fd := md.Fields().ByNumber(num)
		if typ != protowire.BytesType || fd == nil {
			out = append(out, field...)
			continue
		}
		v, _ := protowire.ConsumeBytes(field[len(tag):])
		switch fd.Kind() {
		case protoreflect.StringKind:
			if !utf8.Valid(v) {
				v = []byte(strings.ToValidUTF8(string(v), "�"))
				changed = true
			}
		case protoreflect.MessageKind:
			sub, c, err := sanitizeMessage(fd.Message(), v, depth+1)
			if err != nil {
				return nil, false, err
			}
			v, changed = sub, changed || c
		default:
			out = append(out, field...)
			continue
		}
		out = append(out, tag...)
		out = protowire.AppendBytes(out, v)
	}
	return out, changed, nil
}
