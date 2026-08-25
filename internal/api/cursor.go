package api

import (
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
)

const (
	maxCursorEncoded = 4096
	maxCursorQuery   = 1500
)

var errInvalidCursor = errors.New("invalid cursor")

func encodeCursor(rel string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(rel))
}

func decodeCursor(token, address string) (string, error) {
	if token == "" || len(token) > maxCursorEncoded {
		return "", errInvalidCursor
	}
	raw, err := decodeCursorBytes(token)
	if err != nil {
		return "", errInvalidCursor
	}
	u, err := url.Parse(string(raw))
	if err != nil {
		return "", errInvalidCursor
	}
	if u.Scheme != "" || u.Host != "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return "", errInvalidCursor
	}
	if !sameWalletTxPath(u.Path, address) {
		return "", errInvalidCursor
	}
	if len(u.RawQuery) > maxCursorQuery {
		return "", errInvalidCursor
	}
	return (&url.URL{Path: u.Path, RawQuery: u.RawQuery}).RequestURI(), nil
}

func decodeCursorBytes(token string) ([]byte, error) {
	if raw, err := base64.RawURLEncoding.DecodeString(token); err == nil {
		return raw, nil
	}
	return base64.URLEncoding.DecodeString(token)
}

func sameWalletTxPath(path, address string) bool {
	const prefix = "/v1/wallets/"
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	rest := strings.TrimPrefix(path, prefix)
	var got string
	switch {
	case strings.HasSuffix(rest, "/transactions/"):
		got = strings.TrimSuffix(rest, "/transactions/")
	case strings.HasSuffix(rest, "/transactions"):
		got = strings.TrimSuffix(rest, "/transactions")
	default:
		return false
	}
	if got == "" || strings.Contains(got, "/") {
		return false
	}
	if looksEVM(got) || looksEVM(address) {
		return strings.EqualFold(got, address)
	}
	return got == address
}

func looksEVM(s string) bool {
	return len(s) == 42 && (strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X"))
}
