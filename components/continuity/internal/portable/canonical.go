package portable

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const MaxCanonicalBytes = 4 << 20

type canonicalObjectMember struct {
	name  string
	value any
}

// CanonicalizeJSON applies the RFC 8785 object-key and string rules to the
// portable profile. The profile deliberately rejects floats, integers outside
// the interoperable IEEE-754 range, duplicate keys, invalid UTF-8, and trailing
// JSON values.
func CanonicalizeJSON(input []byte) ([]byte, error) {
	if len(input) == 0 {
		return nil, fmt.Errorf("canonical JSON is required")
	}
	if len(input) > MaxCanonicalBytes {
		return nil, fmt.Errorf("canonical JSON exceeds %d bytes", MaxCanonicalBytes)
	}
	if !utf8.Valid(input) {
		return nil, fmt.Errorf("canonical JSON must be valid UTF-8")
	}
	if err := validateJSONStringEscapes(input); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.UseNumber()
	value, err := parseCanonicalValue(decoder)
	if err != nil {
		return nil, err
	}
	if token, err := decoder.Token(); err != io.EOF {
		if err != nil {
			return nil, fmt.Errorf("decode trailing JSON: %w", err)
		}
		return nil, fmt.Errorf("canonical JSON has trailing value %v", token)
	}
	var output bytes.Buffer
	if err := writeCanonicalValue(&output, value); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func parseCanonicalValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("decode JSON: %w", err)
	}
	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			seen := make(map[string]struct{})
			members := make([]canonicalObjectMember, 0)
			for decoder.More() {
				nameToken, err := decoder.Token()
				if err != nil {
					return nil, fmt.Errorf("decode object key: %w", err)
				}
				name, ok := nameToken.(string)
				if !ok {
					return nil, fmt.Errorf("object key must be a string")
				}
				if _, exists := seen[name]; exists {
					return nil, fmt.Errorf("duplicate object key %q", name)
				}
				seen[name] = struct{}{}
				member, err := parseCanonicalValue(decoder)
				if err != nil {
					return nil, err
				}
				members = append(members, canonicalObjectMember{name: name, value: member})
			}
			if _, err := decoder.Token(); err != nil {
				return nil, fmt.Errorf("decode object close: %w", err)
			}
			sort.Slice(members, func(i, j int) bool { return lessUTF16(members[i].name, members[j].name) })
			return members, nil
		case '[':
			values := make([]any, 0)
			for decoder.More() {
				item, err := parseCanonicalValue(decoder)
				if err != nil {
					return nil, err
				}
				values = append(values, item)
			}
			if _, err := decoder.Token(); err != nil {
				return nil, fmt.Errorf("decode array close: %w", err)
			}
			return values, nil
		default:
			return nil, fmt.Errorf("unexpected JSON delimiter %q", value)
		}
	case nil, bool, string:
		return value, nil
	case json.Number:
		text := string(value)
		if strings.ContainsAny(text, ".eE") {
			return nil, fmt.Errorf("portable canonical JSON does not support floats: %s", text)
		}
		integer, err := strconv.ParseInt(text, 10, 64)
		canonical := strconv.FormatInt(integer, 10)
		if err != nil || (canonical != text && text != "-0") {
			return nil, fmt.Errorf("invalid canonical integer %q", text)
		}
		const maxSafeInteger = int64(1<<53 - 1)
		if integer < -maxSafeInteger || integer > maxSafeInteger {
			return nil, fmt.Errorf("integer %s exceeds interoperable range", text)
		}
		return integer, nil
	default:
		return nil, fmt.Errorf("unsupported JSON token %T", token)
	}
}

func writeCanonicalValue(output *bytes.Buffer, value any) error {
	switch value := value.(type) {
	case []canonicalObjectMember:
		output.WriteByte('{')
		for i, member := range value {
			if i > 0 {
				output.WriteByte(',')
			}
			writeCanonicalString(output, member.name)
			output.WriteByte(':')
			if err := writeCanonicalValue(output, member.value); err != nil {
				return err
			}
		}
		output.WriteByte('}')
	case []any:
		output.WriteByte('[')
		for i, item := range value {
			if i > 0 {
				output.WriteByte(',')
			}
			if err := writeCanonicalValue(output, item); err != nil {
				return err
			}
		}
		output.WriteByte(']')
	case nil:
		output.WriteString("null")
	case bool:
		output.WriteString(strconv.FormatBool(value))
	case string:
		writeCanonicalString(output, value)
	case int64:
		output.WriteString(strconv.FormatInt(value, 10))
	default:
		return fmt.Errorf("cannot encode canonical value %T", value)
	}
	return nil
}

func writeCanonicalString(output *bytes.Buffer, value string) {
	output.WriteByte('"')
	for _, current := range value {
		switch current {
		case '"', '\\':
			output.WriteByte('\\')
			output.WriteRune(current)
		case '\b':
			output.WriteString(`\b`)
		case '\t':
			output.WriteString(`\t`)
		case '\n':
			output.WriteString(`\n`)
		case '\f':
			output.WriteString(`\f`)
		case '\r':
			output.WriteString(`\r`)
		default:
			if current < 0x20 {
				fmt.Fprintf(output, `\u%04x`, current)
			} else {
				output.WriteRune(current)
			}
		}
	}
	output.WriteByte('"')
}

func lessUTF16(left, right string) bool {
	for len(left) > 0 && len(right) > 0 {
		a, aSize := utf8.DecodeRuneInString(left)
		b, bSize := utf8.DecodeRuneInString(right)
		// Supplementary runes sort by their leading surrogate before BMP code
		// points above the surrogate range. Within one leading surrogate, rune
		// order is the same as trailing-surrogate order.
		aFirst, bFirst := a, b
		if a >= 0x10000 {
			aFirst = 0xd800 + (a-0x10000)>>10
		}
		if b >= 0x10000 {
			bFirst = 0xd800 + (b-0x10000)>>10
		}
		if aFirst != bFirst {
			return aFirst < bFirst
		}
		if a != b {
			return a < b
		}
		left, right = left[aSize:], right[bSize:]
	}
	return len(left) < len(right)
}

func validateJSONStringEscapes(input []byte) error {
	for index := 0; index < len(input); index++ {
		if input[index] != '"' {
			continue
		}
		for index++; index < len(input); index++ {
			switch input[index] {
			case '"':
				goto nextString
			case '\\':
				if index+1 >= len(input) {
					return fmt.Errorf("unterminated JSON escape")
				}
				index++
				if input[index] != 'u' {
					continue
				}
				value, err := parseHexEscape(input, index+1)
				if err != nil {
					return err
				}
				index += 4
				if value >= 0xd800 && value <= 0xdbff {
					if index+6 >= len(input) || input[index+1] != '\\' || input[index+2] != 'u' {
						return fmt.Errorf("unpaired high surrogate in JSON string")
					}
					low, err := parseHexEscape(input, index+3)
					if err != nil || low < 0xdc00 || low > 0xdfff {
						return fmt.Errorf("unpaired high surrogate in JSON string")
					}
					index += 6
				} else if value >= 0xdc00 && value <= 0xdfff {
					return fmt.Errorf("unpaired low surrogate in JSON string")
				}
			}
		}
		return fmt.Errorf("unterminated JSON string")
	nextString:
	}
	return nil
}

func parseHexEscape(input []byte, start int) (uint16, error) {
	if start+4 > len(input) {
		return 0, fmt.Errorf("short Unicode escape in JSON string")
	}
	value, err := strconv.ParseUint(string(input[start:start+4]), 16, 16)
	if err != nil {
		return 0, fmt.Errorf("invalid Unicode escape in JSON string")
	}
	return uint16(value), nil
}
