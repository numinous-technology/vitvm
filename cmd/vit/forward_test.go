package main

import "testing"

func TestParseGmuxRemotes(t *testing.T) {
	got, err := parseGmuxRemotes("gpu1=tok1@10.0.0.5:7070#aa, gpu2=tok2@gpu2.example:7071#bb")
	if err != nil || len(got) != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	if got[0].Name != "gpu1" || got[0].Token != "tok1" || got[0].Target != "10.0.0.5:7070" || got[0].Fingerprint != "aa" {
		t.Fatalf("first: %+v", got[0])
	}
	if got[1].Target != "gpu2.example:7071" {
		t.Fatalf("second: %+v", got[1])
	}
	for _, bad := range []string{"gpu1", "gpu1=10.0.0.5:7070#aa", "gpu1=tok@10.0.0.5:7070"} {
		if _, err := parseGmuxRemotes(bad); err == nil {
			t.Fatalf("%q should be refused", bad)
		}
	}
	if got, _ := parseGmuxRemotes(""); len(got) != 0 {
		t.Fatal("empty means no hosts")
	}
}
