package rqtx

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type oracleFixture struct {
	DataSHA256 string `json:"data_sha256"`
	Vectors    []struct {
		Input  string `json:"input"`
		Output uint32 `json:"output"`
	} `json:"vectors"`
}

func TestOracleVectors(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "oracle-vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture oracleFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.DataSHA256 != programSHA256 {
		t.Fatalf("fixture program hash %q, want %q", fixture.DataSHA256, programSHA256)
	}
	if len(fixture.Vectors) == 0 {
		t.Fatal("fixture has no vectors")
	}
	for _, vector := range fixture.Vectors {
		vector := vector
		t.Run(vector.Input, func(t *testing.T) {
			t.Parallel()
			got, err := SumHex(vector.Input)
			if err != nil {
				t.Fatal(err)
			}
			if got != vector.Output {
				t.Fatalf("SumHex(%q) = %d, want %d", vector.Input, got, vector.Output)
			}
		})
	}
}

func TestSignBody(t *testing.T) {
	body := []byte("rqtx body test")
	digest := md5.Sum(body)
	want, err := SumHex(hex.EncodeToString(digest[:]))
	if err != nil {
		t.Fatal(err)
	}
	got, err := SignBody(body)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("SignBody() = %d, want %d", got, want)
	}
}

func TestRejectsInvalidHex(t *testing.T) {
	for _, input := range []string{"", "0000000000000000000000000000000", "000000000000000000000000000000000", "D41D8CD98F00B204E9800998ECF8427E", "g41d8cd98f00b204e9800998ecf8427e"} {
		if _, err := SumHex(input); err == nil {
			t.Errorf("SumHex(%q) succeeded", input)
		}
	}
}
