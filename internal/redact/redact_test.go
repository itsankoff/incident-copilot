package redact

import (
	"encoding/json"
	"strings"
	"testing"
)

// Fake secrets, built from parts so no scanner mistakes this file for a leak.
var (
	awsKeyID  = "AKIA" + "IOSFODNN7EXAMPLE"
	awsTemp   = "ASIA" + "Y34FZKBOKMUTVV7A"
	googleKey = "AIza" + "SyD-9tSrke72PouQMnMX-a7eZSW0jkFMBWY"
	ghp       = "ghp_" + strings.Repeat("a1B2c3D4e5", 3) + "F6g7h8"
	ghs       = "ghs_" + strings.Repeat("Z9y8X7w6V5", 3) + "u4T3s2"
	ghPAT     = "github_pat_" + "11ABCDEFG0123456789abc_" + strings.Repeat("xY12", 15)
	jwt       = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9" + ".eyJzdWIiOiIxMjM0NTY3ODkwIn0" + ".dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U"
	pemKey    = "-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA0Z3VS5JJcds3xfn\n/ygWyF8PbnGy0AHB7MhgHcTz6sE2I2yPB\n-----END RSA PRIVATE KEY-----"
)

func TestString(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
	}{
		{"bearer token", "Authorization: Bearer abcDEF0123456789xyz.-_~+/=", "Authorization: Bearer [REDACTED]"},
		{"lowercase bearer", "auth=bearer 0123456789abcdefghijklmnop", "auth=bearer [REDACTED]"},
		{"AWS access key id", "key " + awsKeyID + " in use", "key [REDACTED] in use"},
		{"AWS temporary key id", awsTemp, "[REDACTED]"},
		{"Google API key", "maps key " + googleKey, "maps key [REDACTED]"},
		{"GitHub classic token", "token " + ghp + " leaked", "token [REDACTED] leaked"},
		{"GitHub app token", ghs, "[REDACTED]"},
		{"GitHub fine-grained token", "pat " + ghPAT, "pat [REDACTED]"},
		{"JWT", "sa token " + jwt + " expired", "sa token [REDACTED] expired"},
		{"private key", "loaded " + pemKey + " ok", "loaded [REDACTED] ok"},
		{"private key cut off", "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBg", "[REDACTED]"},
		{"password pair", "connect password=hunter2 host=db", "connect password=[REDACTED] host=db"},
		{"secret pair with colon", `client_secret: "s3cr3t value"`, `client_secret: [REDACTED]`},
		{"token pair", "DB_TOKEN=abc123,next", "DB_TOKEN=[REDACTED],next"},
		{"api key pair", "x-api-key=zzz999", "x-api-key=[REDACTED]"},
		{"azure account key", "DefaultEndpointsProtocol=https;AccountName=demo;AccountKey=bXlrZXk=;EndpointSuffix=core.windows.net",
			"DefaultEndpointsProtocol=https;AccountName=demo;AccountKey=[REDACTED];EndpointSuffix=core.windows.net"},
		{"S3 pre-signed URL",
			"GET https://b.s3.amazonaws.com/o?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=" + awsKeyID + "%2F20261111%2Fus-east-1&X-Amz-Signature=fe5f80f77d5fa3beca038a248ff027d0445342fe2855ddc963176630326f1024&X-Amz-Expires=300",
			"GET https://b.s3.amazonaws.com/o?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=[REDACTED]&X-Amz-Signature=[REDACTED]&X-Amz-Expires=300"},
		{"Azure SAS URL (GitHub job logs)",
			"https://productionresultssa1.blob.core.windows.net/actions-results/x/logs.txt?rsct=text%2Fplain&se=2026-11-11T10%3A00%3A00Z&sig=AbC%2BdEf%3D&sp=r&sv=2021-12-02",
			"https://productionresultssa1.blob.core.windows.net/actions-results/x/logs.txt?rsct=text%2Fplain&se=2026-11-11T10%3A00%3A00Z&sig=[REDACTED]&sp=r&sv=2021-12-02"},
		{"GCS signed URL", "https://storage.googleapis.com/b/o?X-Goog-Signature=0a1b2c#frag", "https://storage.googleapis.com/b/o?X-Goog-Signature=[REDACTED]#frag"},
		{"already redacted stays the same", "password=[REDACTED] Bearer [REDACTED]", "password=[REDACTED] Bearer [REDACTED]"},

		// Near-misses: text that looks close but holds no secret.
		{"prose about bearer tokens", "Bearer tokens are sent in a header", "Bearer tokens are sent in a header"},
		{"short bearer realm", `WWW-Authenticate: Bearer realm="api"`, `WWW-Authenticate: Bearer realm="api"`},
		{"AKIA too short", "AKIA1234", "AKIA1234"},
		{"AKIA inside a longer word", "AKIAIOSFODNN7EXAMPLEXYZ", "AKIAIOSFODNN7EXAMPLEXYZ"},
		{"ghp_ too short", "ghp_short", "ghp_short"},
		{"one JWT-like segment", "eyJhbGciOiJIUzI1NiJ9 alone", "eyJhbGciOiJIUzI1NiJ9 alone"},
		{"public certificate", "-----BEGIN CERTIFICATE-----\nMIIB", "-----BEGIN CERTIFICATE-----\nMIIB"},
		{"password without a value", "password reset failed for user", "password reset failed for user"},
		{"k8s missing secret event", `MountVolume.SetUp failed for volume "creds" : secret "db-creds" not found`,
			`MountVolume.SetUp failed for volume "creds" : secret "db-creds" not found`},
		{"sha256 digest", "sha256:fe5f80f77d5fa3beca038a248ff027d0445342fe2855ddc963176630326f1024", "sha256:fe5f80f77d5fa3beca038a248ff027d0445342fe2855ddc963176630326f1024"},
		{"URL without signature", "http://prometheus/graph?g0.expr=up&g0.tab=1", "http://prometheus/graph?g0.expr=up&g0.tab=1"},
		{"query param that only ends in sig", "https://x/y?design=blue&signature_version=4", "https://x/y?design=blue&signature_version=4"},
		{"container image digest", "orders@sha256:4f5de0123456789abcdef0123456789abcdef0123456789abcdef0123456789ab", "orders@sha256:4f5de0123456789abcdef0123456789abcdef0123456789abcdef0123456789ab"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := String(tc.in); got != tc.want {
				t.Errorf("String(%q)\n got %q\nwant %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestJSONRedactsInsideStringValues(t *testing.T) {
	// json.Marshal writes & as & and newlines as \n, so a pattern run over
	// the encoded text would miss the second query parameter and the key.
	in, _ := json.Marshal(map[string]any{
		"message": "fetch https://b/o?X-Amz-Algorithm=A&X-Amz-Signature=deadbeef failed",
		"key":     pemKey,
		"count":   12345678901234567,
		"nested":  []any{map[string]string{"t": "Bearer abcdefghijklmnopqrstuvwxyz"}},
	})
	out := JSON(in)
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output is not JSON: %v: %s", err, out)
	}
	if s := got["message"]; s != "fetch https://b/o?X-Amz-Algorithm=A&X-Amz-Signature=[REDACTED] failed" {
		t.Errorf("message = %q", s)
	}
	if s := got["key"]; s != "[REDACTED]" {
		t.Errorf("key = %q", s)
	}
	if !strings.Contains(string(out), `"count":12345678901234567`) {
		t.Errorf("number changed: %s", out)
	}
	if strings.Contains(string(out), "abcdefghijklmnopqrstuvwxyz") {
		t.Errorf("nested token kept: %s", out)
	}
}

func TestJSONWithoutSecretsIsUnchanged(t *testing.T) {
	in := json.RawMessage(`{"z":1,"a":"plain","list":[1.50,"x"]}`)
	if out := JSON(in); string(out) != string(in) {
		t.Errorf("JSON(%s) = %s, want it unchanged", in, out)
	}
}

func TestJSONReplacesValuesUnderSensitiveNames(t *testing.T) {
	in := json.RawMessage(`{"password":"hunter2","tokens":3,"env":[{"name":"DB_PASSWORD","value":"hunter2"},{"name":"LOG_LEVEL","value":"debug"}],"name":"api_token","value":"v"}`)
	out := string(JSON(in))
	if strings.Contains(out, "hunter2") || strings.Contains(out, `"value":"v"`) {
		t.Errorf("a value under a sensitive name was kept: %s", out)
	}
	for _, want := range []string{`"tokens":3`, `"value":"debug"`, `"name":"DB_PASSWORD"`} {
		if !strings.Contains(out, want) {
			t.Errorf("lacks %s: %s", want, out)
		}
	}
	if again := string(JSON(json.RawMessage(out))); again != out {
		t.Errorf("a second pass changed it:\n%s\n%s", out, again)
	}
}

func TestJSONRedactsTextThatIsNotJSON(t *testing.T) {
	if out := string(JSON(json.RawMessage("not json " + ghp))); out != "not json [REDACTED]" {
		t.Errorf("got %q", out)
	}
}
