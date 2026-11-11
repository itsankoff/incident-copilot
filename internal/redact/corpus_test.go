package redact

import (
	"encoding/json"
	"strings"
	"testing"
)

// More fake secrets, built from parts so no scanner mistakes this file for a leak.
var (
	slackUser    = "xoxp-" + "123456789012-1234567890123-1234567890123-" + "0123456789abcdef0123456789abcdef"
	slackWebhook = "https://hooks.slack.com/services/" + "T0123ABCD/B0123ABCD/" + "AbCdEfGhIjKlMnOpQrStUvWx"
	stripeLive   = "sk_" + "live_" + "51Hx" + strings.Repeat("Ab3dEf9h", 11) + "Zq7Wm2K"
	stripeRk     = "rk_" + "live_" + "51Hx" + strings.Repeat("Zy8xWv7u", 8)
	azureADSec   = "Xy1" + "8Q~" + "aB3cD4eF5gH6iJ7kL8mN9oP0qR1sT2uV3w"
	openAI       = "sk-" + "proj-" + strings.Repeat("Ab3_dE9-", 7) + "Ab" + "T3Blbk" + "FJ" + strings.Repeat("Zx7_yW6-", 7) + "Zx"
	anthropic    = "sk-ant-" + "api03-" + strings.Repeat("aB3dE9fG_h", 9) + "aBc" + "AA"
	gitlabPAT    = "glpat-" + "x1Y2z3A4b5C6d7E8f9G0"
	npmToken     = "npm_" + strings.Repeat("a1B2c3D4e5F6", 3)
	sendgrid     = "SG." + "aB3dE9fG_h1J2k3L4m5N6o" + "." + "pQ7rS8tU9vW0xY1zA2bC3dE4fG5hI6jK7lM8nO9pQ0r"
	twilio       = "SK" + strings.Repeat("0a1b2c3d", 4)
	vault        = "hvs." + "CAESI" + strings.Repeat("Kx9mQ2vL7pR4tZ8w", 6)
	basicAuth    = "dXNlcjpodW50" + "ZXIyMDI2IQ=="         // user:hunter2026!
	dockerAuth   = "b3JkZXJzOmdo" + "cF9GYWtlRmFrZUZha2U=" // orders:ghp_FakeFakeFake
	redisPass    = "Rk9" + "pX2s3cretPa55"
	opaque       = "8f3kLm2Q" + "p9Zx7Vb4Nc6Rt1Yw5Hs0Jd"
	barePass     = "Tr0ub4dor" + "&3horse"
	awsSecret    = "wJalrXUtnFEMI/K7MDENG/" + "bPxRfiCYEXAMPLEKEY"
	ddKey        = "0a1b2c3d" + "4e5f6a7b8c9d0e1f2a3b4c5d"
	gcpSAKeyPEM  = `-----BEGIN PRIVATE KEY-----\\nMIIEvgIBADANBgkqhkiG9w0BAQEFAASC\\nBKgwggSkAgEAAoIBAQC7\\n-----END PRIVATE KEY-----\\n`
)

// corpus is secrets as they show up in what the Sensor fetches (pod env and
// args, events, log lines, ConfigMaps, error text), then near-misses: payloads
// from the same places that hold no secret.
var corpus = []struct {
	name    string
	json    bool // run through JSON, else String
	in      string
	secrets []string // each must be gone from the output
	// outOfScope marks a secret no rule catches yet: an opaque token with no
	// key name, or a bare password. Context rules for them come later.
	outOfScope bool
	want       string // the expected output of a near-miss our key-name rule already over-redacts; empty means unchanged
}{
	{name: "AWS key id and secret in env text", in: "AWS_ACCESS_KEY_ID=" + awsKeyID + " AWS_SECRET_ACCESS_KEY=" + awsSecret, secrets: []string{awsKeyID, awsSecret}},
	{name: "GitHub token in a CI log", in: "2026-11-11T10:00:00Z git clone https://x-access-token:" + ghp + "@github.com/org/repo.git", secrets: []string{ghp}},
	{name: "JWT in an event message", in: `Warning  Unauthorized  pod/orders-7d4b9c8f6d-x2kq9  upstream rejected token ` + jwt, secrets: []string{jwt}},
	{name: "postgres URL in pod env", json: true, in: `{"env":[{"name":"DATABASE_URL","value":"postgres://orders:` + "s3cr3tPw" + `@db.demo:5432/orders"}]}`, secrets: []string{"s3cr3tPw"}},
	{name: "GCP service account key in a ConfigMap", json: true, in: `{"data":{"sa.json":"{\"type\":\"service_account\",\"private_key\":\"` + gcpSAKeyPEM + `\"}"}}`, secrets: []string{"MIIEvgIBADANBgkqhkiG9w0BAQEFAASC"}},
	{name: "Datadog key in pod env", json: true, in: `{"env":[{"name":"DD_API_KEY","value":"` + ddKey + `"}]}`, secrets: []string{ddKey}},
	{name: "Slack bot token in container args", json: true, in: `{"spec":{"containers":[{"name":"notifier","args":["--channel=#alerts","--slack=` + slackBot + `"]}]}}`, secrets: []string{slackBot}},
	{name: "Slack user token in a log line", in: `level=error ts=2026-11-11T10:00:01Z msg="chat.postMessage failed" auth=` + slackUser + ` err="not_in_channel"`, secrets: []string{slackUser}},
	{name: "Slack webhook in pod env", json: true, in: `{"env":[{"name":"SLACK_WEBHOOK_URL","value":"` + slackWebhook + `"}]}`, secrets: []string{"AbCdEfGhIjKlMnOpQrStUvWx"}},
	{name: "Stripe live key in pod env", json: true, in: `{"env":[{"name":"STRIPE_KEY","value":"` + stripeLive + `"}]}`, secrets: []string{stripeLive}},
	{name: "Stripe restricted key in a log line", in: `msg="stripe: charge failed" client=` + stripeRk + ` status=402`, secrets: []string{stripeRk}},
	{name: "Azure AD client secret in pod env", json: true, in: `{"env":[{"name":"AZURE_CLIENT_CREDENTIAL","value":"` + azureADSec + `"}]}`, secrets: []string{azureADSec}},
	{name: "OpenAI key in error text", in: `openai: 401 Incorrect API key provided: ` + openAI + `.`, secrets: []string{openAI}},
	{name: "Anthropic key in a log line", in: `{"level":"warn","msg":"llm call failed","x":"` + anthropic + `"}`, secrets: []string{anthropic}},
	{name: "GitLab PAT in a deploy log", in: `fetching https://gitlab.com/api/v4/projects/42/repository/commits with PRIVATE-HEADER ` + gitlabPAT, secrets: []string{gitlabPAT}},
	{name: "npm token in an .npmrc ConfigMap", json: true, in: `{"data":{".npmrc":"//registry.npmjs.org/:_authToken=` + npmToken + `\n"}}`, secrets: []string{npmToken}},
	{name: "SendGrid key in pod env", json: true, in: `{"env":[{"name":"SENDGRID","value":"` + sendgrid + `"}]}`, secrets: []string{sendgrid}},
	{name: "Twilio key in a log line", in: `twilio: using ` + twilio + ` for account AC0123`, secrets: []string{twilio}},
	{name: "Vault token in a log line", in: `vault: renew lease with ` + vault + ` ok`, secrets: []string{vault}},
	{name: "basic auth header in an event", in: `Failed to pull image "registry.demo/orders:v2": unexpected status from HEAD request, Authorization: Basic ` + basicAuth, secrets: []string{basicAuth}},
	{name: "dockerconfigjson auth", json: true, in: `{"auths":{"registry.demo":{"auth":"` + dockerAuth + `"}}}`, secrets: []string{dockerAuth}},
	{name: "redis URL with an empty user", in: `dial tcp: redis://:` + redisPass + `@redis.demo:6379/0: connection refused`, secrets: []string{redisPass}},
	{name: "opaque token with no key name", outOfScope: true, in: `retrying upstream call with header X-Upstream-Auth ` + opaque, secrets: []string{opaque}},
	{name: "bare password in args", json: true, outOfScope: true, in: `{"args":["--db-user","orders","--db-pass","` + barePass + `"]}`, secrets: []string{barePass}},

	{name: "pod metadata and status", json: true, in: `{"metadata":{"name":"orders-7d4b9c8f6d-x2kq9","namespace":"demo","uid":"3f2a8c1e-9b7d-4e6f-a5c3-1d2e3f4a5b6c","resourceVersion":"918273","labels":{"app":"orders","pod-template-hash":"7d4b9c8f6d"},"ownerReferences":[{"kind":"ReplicaSet","name":"orders-7d4b9c8f6d","uid":"9a8b7c6d-5e4f-4a3b-8c2d-1e0f9a8b7c6d"}]},"status":{"podIP":"10.244.0.17","containerStatuses":[{"containerID":"containerd://4f5de0123456789abcdef0123456789abcdef0123456789abcdef0123456789ab","imageID":"ghcr.io/itsankoff/orders@sha256:fe5f80f77d5fa3beca038a248ff027d0445342fe2855ddc963176630326f1024","restartCount":7}]}}`},
	// Our key-name rule over-redacts "true" here, as before the vendored rules.
	{name: "env with secretKeyRef", json: true, in: `{"env":[{"name":"DB_PASSWORD","valueFrom":{"secretKeyRef":{"name":"db-creds","key":"password"}}},{"name":"API_TOKEN_FILE_PATH_ENABLED","value":"true"}],"automountServiceAccountToken":false,"serviceAccountName":"orders"}`,
		want: `{"automountServiceAccountToken":false,"env":[{"name":"DB_PASSWORD","valueFrom":{"secretKeyRef":{"key":"password","name":"db-creds"}}},{"name":"API_TOKEN_FILE_PATH_ENABLED","value":"[REDACTED]"}],"serviceAccountName":"orders"}`},
	{name: "projected service account token volume", json: true, in: `{"volumes":[{"name":"kube-api-access-x2kq9","projected":{"sources":[{"serviceAccountToken":{"expirationSeconds":3607,"path":"token"}},{"configMap":{"name":"kube-root-ca.crt","items":[{"key":"ca.crt","path":"ca.crt"}]}}]}}]}`},
	{name: "image pull event", in: `Normal  Pulled  pod/orders-7d4b9c8f6d-x2kq9  Successfully pulled image "ghcr.io/itsankoff/orders:v1.4.2" in 1.204s (1.204s including waiting). Image size: 48213411 bytes.`},
	{name: "OTel log line with trace ids", in: `ts=2026-11-11T10:00:02.123Z level=info trace_id=4bf92f3577b34da6a3ce929d0e0e4736 span_id=00f067aa0ba902b7 msg="GET /orders/42 200" duration_ms=12.7 request_id=3f2a8c1e-9b7d-4e6f-a5c3-1d2e3f4a5b6c`},
	{name: "Prometheus series", in: `container_memory_working_set_bytes{container="orders",id="/kubepods/burstable/pod3f2a8c1e-9b7d-4e6f-a5c3-1d2e3f4a5b6c/cri-containerd-4f5de0123456789abcdef0123456789abcdef0123456789abcdef0123456789ab",image="ghcr.io/itsankoff/orders@sha256:fe5f80f77d5fa3beca038a248ff027d0445342fe2855ddc963176630326f1024",namespace="demo",pod="orders-7d4b9c8f6d-x2kq9"} 1.34217728e+08`},
	{name: "deploy history commit", in: "commit 9fceb02d0ae598e95dc970b74767f19372d61af8\nAuthor: dev <dev@example.com>\nDate:   Tue Nov 10 10:00:00 2026 +0000\n\n    fix(orders): raise token bucket burst to 200 and rotate password hashing cost to 12"},
	{name: "prose with secret words", in: `token bucket refill rate=100/s; password reset email sent for user id 42; secret "db-creds" not found; using service account token from /var/run/secrets/kubernetes.io/serviceaccount/token`},
	{name: "Go panic stack", in: "panic: runtime error: invalid memory address or nil pointer dereference\n[signal SIGSEGV: segmentation violation code=0x1 addr=0x18 pc=0x4a2f1d]\n\ngoroutine 1 [running]:\nmain.(*Server).auth(0xc000012345, {0x7f3b2c, 0xc0001a2b3c})\n\t/app/auth.go:42 +0x1d"},
	{name: "last-applied-configuration annotation", json: true, in: `{"metadata":{"annotations":{"kubectl.kubernetes.io/last-applied-configuration":"{\"apiVersion\":\"apps/v1\",\"kind\":\"Deployment\",\"metadata\":{\"name\":\"orders\",\"namespace\":\"demo\"},\"spec\":{\"replicas\":2,\"template\":{\"spec\":{\"containers\":[{\"image\":\"ghcr.io/itsankoff/orders:v1.4.2\",\"name\":\"orders\"}]}}}}\n","deployment.kubernetes.io/revision":"7"}}}`},
	{name: "AWS ARN and request id", in: `AccessDenied: User: arn:aws:sts::123456789012:assumed-role/orders-irsa/1731319200 is not authorized to perform: s3:GetObject; x-amz-request-id: 4442587FB7D0A2F9; x-amz-id-2: vlR7PnpV2Ce81l0PRw6jlUpck7Jo5ZsQjryTjKlc5aLWGVHPZLj5NeC6qMa0emYBDXOo6QBU0Wo=`},
	{name: "Loki stream labels and base64 protobuf", in: `{app="orders", namespace="demo", pod="orders-7d4b9c8f6d-x2kq9"} payload=CiQzZjJhOGMxZS05YjdkLTRlNmYtYTVjMy0xZDJlM2Y0YTViNmMSBm9yZGVycxoEZGVtbyIJMTAuMjQ0LjAuMTc=`},
	{name: "kube-root-ca certificate", json: true, in: `{"data":{"ca.crt":"-----BEGIN CERTIFICATE-----\nMIIC/jCCAeagAwIBAgIBADANBgkqhkiG9w0BAQsFADAVMRMwEQYDVQQDEwprdWJl\ncm5ldGVzMB4XDTI2MTExMTEwMDAwMFoXDTM2MTEwOTEwMDAwMFowFTETMBEGA1UE\n-----END CERTIFICATE-----\n"}}`},
	{name: "Helm release and image digest", in: `helm: release "orders" revision 7 stored in secret sh.helm.release.v1.orders.v7; image orders@sha256:4f5de0123456789abcdef0123456789abcdef0123456789abcdef0123456789ab`},
	{name: "Grafana and Prometheus links", in: `http://grafana.monitoring:3000/d/abc123/orders?orgId=1&var-namespace=demo&from=now-1h&to=now&refresh=30s http://prometheus:9090/graph?g0.expr=rate(http_requests_total%7Bjob%3D%22orders%22%7D%5B5m%5D)&g0.tab=0`},
	{name: "node status", json: true, in: `{"status":{"nodeInfo":{"machineID":"a1b2c3d4e5f60718293a4b5c6d7e8f90","systemUUID":"3f2a8c1e-9b7d-4e6f-a5c3-1d2e3f4a5b6c","bootID":"9a8b7c6d-5e4f-4a3b-8c2d-1e0f9a8b7c6d","kernelVersion":"6.8.0-1015-aws","containerRuntimeVersion":"containerd://1.7.22","kubeletVersion":"v1.35.0"},"images":[{"names":["registry.k8s.io/pause@sha256:7031c1b283388d2c2e09b57badb803c05ebed362dc88d84b480cc47f72a21097","registry.k8s.io/pause:3.10"],"sizeBytes":320368}]}}`},
	// Our key-name rule over-redacts "24h" here, as before the vendored rules.
	{name: "Argo CD sync and git sha", in: `argocd: app orders synced to 4f5de01234567890abcdef0123456789abcdef01 (HEAD) by automation, health=Progressing, keyId=2, tokenTTL=24h`,
		want: `argocd: app orders synced to 4f5de01234567890abcdef0123456789abcdef01 (HEAD) by automation, health=Progressing, keyId=2, tokenTTL=[REDACTED]`},
}

func TestCorpus(t *testing.T) {
	caught, positives := 0, 0
	for _, tc := range corpus {
		t.Run(tc.name, func(t *testing.T) {
			var out string
			if tc.json {
				out = string(JSON(json.RawMessage(tc.in)))
			} else {
				out = String(tc.in)
			}
			if len(tc.secrets) == 0 {
				want := tc.want
				if want == "" {
					want = tc.in
				}
				if out != want {
					t.Errorf("near-miss changed:\n got %s\nwant %s", out, want)
				}
				return
			}
			positives++
			var kept []string
			for _, s := range tc.secrets {
				if strings.Contains(out, s) {
					kept = append(kept, s)
				}
			}
			if len(kept) == 0 {
				caught++
			}
			if len(kept) > 0 && !tc.outOfScope {
				t.Errorf("kept %q: %s", kept, out)
			}
		})
	}
	t.Logf("caught %d of %d secrets", caught, positives)
}
