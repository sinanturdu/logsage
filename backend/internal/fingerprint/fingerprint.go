// Package fingerprint groups log messages that share a cause (ADR 0022).
//
// Normalize turns the first line of a message into a template by replacing
// variable parts (numbers, ids, addresses, times) with placeholders. Compute
// hashes the service, the template (in its key form) and the error type into
// a 64-bit fingerprint. Every rule is a Go regexp (RE2, linear time) applied
// in a fixed order to at most MaxInput bytes, so normalizing is
// deterministic, idempotent and bounded on any input.
package fingerprint

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Version is part of every fingerprint. Changing any rule must bump it, so
// that old and new groups split on purpose; the golden tests enforce this.
const Version = "1"

// MaxInput is how much of the first line is normalized, in bytes.
const MaxInput = 512

// MaxErrorType bounds the error.type / exception.type value in the key.
const MaxErrorType = 128

var (
	reStatus    = regexp.MustCompile(`(?i)\b(returned|status|code|http/\d(?:\.\d)?)(\s*[:=]?\s*)([45]\d\d)\b`)
	reLeadStat  = regexp.MustCompile(`^([45]\d\d)(\s+[A-Z])`)
	reReqLine   = regexp.MustCompile(`^((?:GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)\s+\S+\s+)([1-5]\d\d)\b`)
	reQuoted    = regexp.MustCompile(`"([^"\n]{0,200})"|'([^'\n]{0,200})'`)
	reIdentLike = regexp.MustCompile(`^[A-Za-z_<][\w.$<>()\[\]:/-]{0,63}$`)
	reMethod    = regexp.MustCompile(`^(?:GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS) `)
	reTS        = regexp.MustCompile(`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}(?::\d{2}(?:[.,]\d+)?)?(?:Z|[+-]\d{2}:?\d{2})?`)
	reDate      = regexp.MustCompile(`\b\d{4}[-/]\d{2}[-/]\d{2}\b`)
	reTime      = regexp.MustCompile(`\b\d{1,2}:\d{2}:\d{2}(?:[.,]\d+)?\b`)
	reUUID      = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	reEmail     = regexp.MustCompile(`\b[\w.+-]+@[\w-]+(?:\.[\w-]+)+\b`)
	reURL       = regexp.MustCompile(`\b([a-zA-Z][a-zA-Z0-9+.-]*://)([^\s/?#'"<>]+)([^\s?#'"<>]*)(?:\?[^\s#'"<>]*)?(?:#[^\s'"<>]*)?`)
	reIPv4      = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}(?::\d{1,5})?\b`)
	rePod       = regexp.MustCompile(`\b([a-z][a-z0-9-]*?)-[a-z0-9]{8,10}-[a-z0-9]{5}\b`)
	reIPv6      = regexp.MustCompile(`(?i)(?:[0-9a-f]{1,4})?(?::[0-9a-f]{0,4}){2,7}`)
	rePathQuery = regexp.MustCompile(`((?:/[^\s/?'"(),;:]+)+/?)\?[^\s'"()]*`)
	rePath      = regexp.MustCompile(`(?:/[^\s/'"(),;:?]+)+/?`)
	// "HTTP/1.1" is a protocol version, not a path.
	reHTTPVersion = regexp.MustCompile(`(?i)HTTP$`)
	reExt         = regexp.MustCompile(`^(.*?)(\.[A-Za-z][A-Za-z0-9]{0,5})$`)
	reVersion     = regexp.MustCompile(`^v\d+$`)
	reB64         = regexp.MustCompile(`[A-Za-z0-9+/_-]{24,}={0,2}`)
	reHex         = regexp.MustCompile(`(?i)\b(?:0x[0-9a-f]+|[0-9a-f]{8,})\b`)
	reHexShort    = regexp.MustCompile(`(?i)\b[0-9a-f]{6,7}\b`)
	reUnits       = regexp.MustCompile(`(?i)\b\d+(?:\.\d+)?(\s?)(ns|us|µs|ms|s|sec|secs|seconds|min|mins|minutes|h|hr|hrs|hours|kb|mb|gb|tb|kib|mib|gib|tib)\b`)
	rePercent     = regexp.MustCompile(`\b\d+(?:\.\d+)?%`)
	rePrefixID    = regexp.MustCompile(`([A-Za-z]+[_-])([A-Za-z0-9]*\d[A-Za-z0-9]*)\b`)
	reMixed       = regexp.MustCompile(`\b[A-Za-z0-9]*\d[A-Za-z0-9]*\b`)
	reDigitLet    = regexp.MustCompile(`\d[A-Za-z]`)
	reNumber      = regexp.MustCompile(`\b\d+(?:[.,]\d+)*\b`)
	reSpace       = regexp.MustCompile(`\s+`)
	reSentinel    = regexp.MustCompile("\x01([a-j.]{1,5})\x01")
	reHTTPVer     = regexp.MustCompile(`\b(HTTP|http)/(\d(?:\.\d)?)\b`)
	reKeyForm     = regexp.MustCompile(`<(?:n|id|uuid)>`)

	// Words after "returned 4xx" that still mean a status code; any other
	// lower-case word means a count ("returned 503 results").
	statusFollowers = map[string]bool{"for": true, "from": true, "in": true, "on": true, "at": true, "after": true,
		"with": true, "while": true, "when": true, "to": true, "by": true, "error": true, "errors": true}
	// Words with digits that are names, not values.
	keepWords = map[string]bool{"sha1": true, "sha256": true, "sha512": true, "md5": true, "utf8": true, "ipv4": true,
		"ipv6": true, "x86": true, "x86_64": true, "http2": true, "oauth2": true, "base64": true, "int32": true,
		"int64": true, "uint32": true, "uint64": true, "float32": true, "float64": true, "s3": true, "ec2": true,
		"k8s": true, "h2": true, "e2e": true, "i18n": true, "l10n": true}
)

const digits = "0123456789"

// protect hides a status code or protocol version from the number rules
// ("503" -> "\x01fad\x01", "1.1" -> "\x01b.b\x01").
func protect(code string) string {
	b := []byte(code)
	for i := range b {
		if b[i] >= '0' && b[i] <= '9' {
			b[i] = 'a' + (b[i] - '0')
		}
	}
	return "\x01" + string(b) + "\x01"
}

func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return s[:i]
	}
	return s
}

// cut shortens s to at most n bytes without splitting a valid rune.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for i := 0; i < utf8.UTFMax && len(s) > 0; i++ {
		r, size := utf8.DecodeLastRuneInString(s)
		if r != utf8.RuneError || size != 1 {
			break
		}
		s = s[:len(s)-1]
	}
	return s
}

// maxPasses bounds the fixed-point loop in Normalize.
const maxPasses = 4

// Normalize returns the template of the message's first line. Some rules
// depend on context (a status code after "returned", a quoted string) that
// a later rule can rewrite, so one pass is applied until the result stops
// changing: that makes Normalize idempotent. Real messages settle after the
// second pass; the loop is capped at maxPasses.
func Normalize(message string) string {
	s := strings.ToValidUTF8(cut(firstLine(message), MaxInput), "�")
	for i := 0; i < maxPasses; i++ {
		next := pass(s)
		if next == s {
			break
		}
		s = next
	}
	return s
}

// pass applies every rule once, in order, to a valid UTF-8 line.
func pass(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' ' // also keeps the \x01 sentinel unique
		}
		return r
	}, s)

	// 1. Status codes stay: "429 Too Many Requests", "returned 503 for", "POST /x 502 in".
	s = reLeadStat.ReplaceAllStringFunc(s, func(m string) string {
		p := reLeadStat.FindStringSubmatch(m)
		return protect(p[1]) + p[2]
	})
	s = protectStatus(s)
	s = reReqLine.ReplaceAllStringFunc(s, func(m string) string {
		p := reReqLine.FindStringSubmatch(m)
		return p[1] + protect(p[2])
	})
	// "HTTP/1.1" is a protocol version: neither a path nor a number.
	s = reHTTPVer.ReplaceAllStringFunc(s, func(m string) string {
		p := reHTTPVer.FindStringSubmatch(m)
		return p[1] + "/" + protect(p[2])
	})
	// 2. Quoted strings: identifiers stay, values become <str>.
	s = reQuoted.ReplaceAllStringFunc(s, func(m string) string {
		inner := m[1 : len(m)-1]
		if reMethod.MatchString(inner) {
			return m // a quoted request line: its path is normalized below
		}
		if reIdentLike.MatchString(inner) && !reUUID.MatchString(inner) && !reEmail.MatchString(inner) {
			return m
		}
		return m[:1] + "<str>" + m[:1]
	})
	// 3-7. Times, ids, addresses.
	s = reTS.ReplaceAllString(s, "<ts>")
	s = reDate.ReplaceAllString(s, "<date>")
	s = reTime.ReplaceAllString(s, "<time>")
	s = reUUID.ReplaceAllString(s, "<uuid>")
	s = reEmail.ReplaceAllString(s, "<email>")
	s = reURL.ReplaceAllString(s, "$1$2$3")
	s = reIPv4.ReplaceAllString(s, "<ip>")
	s = rePod.ReplaceAllString(s, "$1-<id>")
	s = reIPv6.ReplaceAllStringFunc(s, func(m string) string {
		colons := strings.Count(m, ":")
		if colons < 2 || !strings.ContainsAny(m, "0123456789abcdefABCDEF") || (colons < 3 && !strings.Contains(m, "::")) {
			return m
		}
		return "<ip>"
	})
	// 8. Paths: drop a query, then normalize segments with digits.
	s = rePathQuery.ReplaceAllString(s, "$1")
	s = replaceOutside(s, rePath, reHTTPVersion, normalizePath)
	// 9-14. Tokens and numbers.
	s = reB64.ReplaceAllStringFunc(s, func(m string) string {
		if strings.ContainsAny(m, digits) && strings.ContainsAny(strings.ToLower(m), "abcdefghijklmnopqrstuvwxyz") {
			return "<token>"
		}
		return m
	})
	s = reHex.ReplaceAllString(s, "<id>")
	s = reHexShort.ReplaceAllStringFunc(s, func(m string) string {
		if strings.ContainsAny(m, digits) && strings.ContainsAny(strings.ToLower(m), "abcdef") {
			return "<id>"
		}
		return m
	})
	s = reUnits.ReplaceAllStringFunc(s, func(m string) string {
		p := reUnits.FindStringSubmatch(m)
		return "<n>" + p[1] + strings.ToLower(p[2])
	})
	s = rePercent.ReplaceAllString(s, "<n>%")
	s = rePrefixID.ReplaceAllStringFunc(s, func(m string) string {
		if keepWords[strings.ToLower(m)] {
			return m
		}
		p := rePrefixID.FindStringSubmatch(m)
		if strings.Trim(p[2], digits) == "" {
			return p[1] + "<n>"
		}
		return p[1] + "<id>"
	})
	s = reMixed.ReplaceAllStringFunc(s, func(m string) string {
		switch {
		case keepWords[strings.ToLower(m)], reVersion.MatchString(m), strings.Trim(m, digits) == "":
			return m // a name, a version, or a plain number for the next rule
		case len(m) < 5 || !reDigitLet.MatchString(m):
			return m // a word with a trailing number (psycopg2, sha256, local1)
		}
		return "<id>"
	})
	s = reNumber.ReplaceAllString(s, "<n>")
	// 15. Restore the status codes, collapse whitespace.
	s = reSentinel.ReplaceAllStringFunc(s, func(m string) string {
		b := []byte(m[1 : len(m)-1])
		for i := range b {
			if b[i] != '.' {
				b[i] = '0' + (b[i] - 'a')
			}
		}
		return string(b)
	})
	return strings.TrimSpace(reSpace.ReplaceAllString(s, " "))
}

// protectStatus protects a 4xx/5xx after returned/status/code/HTTP unless a
// count noun follows it.
func protectStatus(s string) string {
	locs := reStatus.FindAllStringSubmatchIndex(s, -1)
	if locs == nil {
		return s
	}
	var b strings.Builder
	last := 0
	for _, l := range locs {
		after := s[l[1]:]
		rest := strings.TrimLeft(after, " ")
		word := rest
		if i := strings.IndexAny(rest, " ,.;:)"); i >= 0 {
			word = rest[:i]
		}
		count := len(rest) < len(after) && word != "" && word[0] >= 'a' && word[0] <= 'z' && !statusFollowers[word]
		b.WriteString(s[last:l[6]])
		if count {
			b.WriteString(s[l[6]:l[7]])
		} else {
			b.WriteString(protect(s[l[6]:l[7]]))
		}
		last = l[7]
	}
	b.WriteString(s[last:])
	return b.String()
}

// replaceOutside applies fn to each match of re, except where the text just
// before the match matches skipBefore (RE2 has no look-behind).
func replaceOutside(s string, re, skipBefore *regexp.Regexp, fn func(string) string) string {
	locs := re.FindAllStringIndex(s, -1)
	if locs == nil {
		return s
	}
	var b strings.Builder
	last := 0
	for _, l := range locs {
		b.WriteString(s[last:l[0]])
		if skipBefore.MatchString(s[:l[0]]) {
			b.WriteString(s[l[0]:l[1]])
		} else {
			b.WriteString(fn(s[l[0]:l[1]]))
		}
		last = l[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

func normalizePath(p string) string {
	segs := strings.Split(p, "/")
	for i, seg := range segs {
		if seg == "" || !strings.ContainsAny(seg, digits) || reVersion.MatchString(seg) || keepWords[strings.ToLower(seg)] {
			continue
		}
		stem, ext := seg, ""
		if m := reExt.FindStringSubmatch(seg); m != nil && m[1] != "" {
			stem, ext = m[1], m[2]
		}
		if strings.Trim(stem, digits) == "" {
			segs[i] = "<n>" + ext
		} else if strings.ContainsAny(stem, digits) {
			segs[i] = "<id>" + ext
		}
	}
	return strings.Join(segs, "/")
}

// KeyForm collapses <n>, <id> and <uuid> to <*>, so the key does not depend
// on how one id happened to look (all digits or mixed hex).
func KeyForm(template string) string { return reKeyForm.ReplaceAllString(template, "<*>") }

// ErrorType returns error.type, else exception.type, trimmed and bounded.
func ErrorType(attrs map[string]string) string {
	v := attrs["error.type"]
	if strings.TrimSpace(v) == "" {
		v = attrs["exception.type"]
	}
	return cut(strings.TrimSpace(strings.ToValidUTF8(v, "�")), MaxErrorType)
}

// Of returns the fingerprint for a template. 0 never comes out: it means
// "not grouped".
func Of(service, template, errorType string) uint64 {
	h := sha256.New()
	for i, part := range []string{Version, service, KeyForm(template), errorType} {
		if i > 0 {
			h.Write([]byte{0x1f})
		}
		h.Write([]byte(part))
	}
	v := binary.BigEndian.Uint64(h.Sum(nil)[:8])
	if v == 0 {
		v = 1
	}
	return v
}

// Groupable reports whether rows of this level get a fingerprint: warn,
// error and fatal. debug and info keep 0 ("not grouped").
func Groupable(level string) bool { return level == "warn" || level == "error" || level == "fatal" }

// Compute returns the fingerprint and template of one log line, or 0 and ""
// when its level is not grouped.
func Compute(level, service, message string, attrs map[string]string) (uint64, string) {
	if !Groupable(level) {
		return 0, ""
	}
	tpl := Normalize(message)
	return Of(service, tpl, ErrorType(attrs)), tpl
}

// Hex formats a fingerprint as 16 lower-case hex digits (empty for 0).
func Hex(fp uint64) string {
	if fp == 0 {
		return ""
	}
	return fmt.Sprintf("%016x", fp)
}

// ParseHex reads exactly 16 hex digits (either case); 0 is not a fingerprint.
func ParseHex(s string) (uint64, error) {
	if len(s) != 16 {
		return 0, fmt.Errorf("fingerprint must be 16 hexadecimal characters")
	}
	v, err := strconv.ParseUint(s, 16, 64)
	if err != nil || v == 0 {
		return 0, fmt.Errorf("fingerprint must be 16 hexadecimal characters")
	}
	return v, nil
}
