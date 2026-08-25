package api

import (
	"strings"
	"testing"
)

const (
	testEVM = "0x1111111111111111111111111111111111111111"
	testSOL = "11111111111111111111111111111111"
)

func TestCursorRoundTrip(t *testing.T) {
	rel := "/v1/wallets/" + testEVM + "/transactions/?page[lastId]=abc&page[size]=20"
	token := encodeCursor(rel)
	got, err := decodeCursor(token, testEVM)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "page[lastId]=abc") && !strings.Contains(got, "page%5BlastId%5D=abc") {
		t.Fatalf("got=%q", got)
	}
}

func TestCursorRejectsSchemeAndHost(t *testing.T) {
	token := encodeCursor("https://api.zerion.io/v1/wallets/" + testEVM + "/transactions/")
	if _, err := decodeCursor(token, testEVM); err == nil {
		t.Fatal("expected error")
	}
}

func TestCursorRejectsOtherAddress(t *testing.T) {
	other := "0x2222222222222222222222222222222222222222"
	token := encodeCursor("/v1/wallets/" + other + "/transactions/?page[after]=x")
	if _, err := decodeCursor(token, testEVM); err == nil {
		t.Fatal("expected error")
	}
}

func TestCursorRejectsWrongPath(t *testing.T) {
	token := encodeCursor("/v1/wallets/" + testEVM + "/portfolio")
	if _, err := decodeCursor(token, testEVM); err == nil {
		t.Fatal("expected error")
	}
}

func TestCursorRejectsUserinfoAndFragment(t *testing.T) {
	token := encodeCursor("/v1/wallets/" + testEVM + "/transactions/#frag")
	if _, err := decodeCursor(token, testEVM); err == nil {
		t.Fatal("expected error")
	}
}

func TestCursorRejectsGarbage(t *testing.T) {
	if _, err := decodeCursor("%%%not-base64%%%", testEVM); err == nil {
		t.Fatal("expected error")
	}
}

func TestCursorRejectsTooLong(t *testing.T) {
	if _, err := decodeCursor(strings.Repeat("A", maxCursorEncoded+1), testEVM); err == nil {
		t.Fatal("expected error")
	}
}

func TestCursorRejectsLongQuery(t *testing.T) {
	rel := "/v1/wallets/" + testEVM + "/transactions/?" + strings.Repeat("a=1&", 400)
	token := encodeCursor(rel)
	if _, err := decodeCursor(token, testEVM); err == nil {
		t.Fatal("expected error")
	}
}

func TestCursorSolanaCaseSensitive(t *testing.T) {
	mixed := "So11111111111111111111111111111111111111112"
	token := encodeCursor("/v1/wallets/" + mixed + "/transactions/?page[after]=x")
	if _, err := decodeCursor(token, mixed); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeCursor(token, strings.ToLower(mixed)); err == nil {
		t.Fatal("solana address case should be preserved")
	}
}

func TestCursorEVMCaseInsensitive(t *testing.T) {
	upper := "0x1111111111111111111111111111111111111111"
	token := encodeCursor("/v1/wallets/" + strings.ToUpper(upper) + "/transactions/?page[after]=x")
	if _, err := decodeCursor(token, upper); err != nil {
		t.Fatal(err)
	}
}
