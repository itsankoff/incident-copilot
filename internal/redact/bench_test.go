package redact

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func BenchmarkString(b *testing.B) {
	for _, n := range []int{1 << 10, 64 << 10, 1 << 20} {
		s := textPayload(n)
		b.Run(size(n), func(b *testing.B) {
			b.SetBytes(int64(len(s)))
			for b.Loop() {
				String(s)
			}
		})
	}
}

func BenchmarkJSON(b *testing.B) {
	for _, n := range []int{1 << 10, 64 << 10, 1 << 20} {
		raw := json.RawMessage(jsonPayload(n))
		b.Run(size(n), func(b *testing.B) {
			b.SetBytes(int64(len(raw)))
			for b.Loop() {
				JSON(raw)
			}
		})
	}
}

func size(n int) string {
	if n >= 1<<20 {
		return fmt.Sprintf("%dMiB", n>>20)
	}
	return fmt.Sprintf("%dKiB", n>>10)
}

// textPayload is n bytes of log lines, one line in 50 holding a secret.
func textPayload(n int) string {
	var b strings.Builder
	for i := 0; b.Len() < n; i++ {
		switch i % 50 {
		case 7:
			fmt.Fprintf(&b, "ts=2026-11-11T10:00:%02d.123Z level=error msg=\"upstream auth failed\" header=\"Authorization: Bearer %s\"\n", i%60, jwt)
		case 31:
			fmt.Fprintf(&b, "ts=2026-11-11T10:00:%02d.123Z level=warn msg=\"retry\" dsn=postgres://orders:s3cr3tPw@db.demo:5432/orders\n", i%60)
		default:
			fmt.Fprintf(&b, "ts=2026-11-11T10:00:%02d.123Z level=info trace_id=4bf92f3577b34da6a3ce929d0e0e4736 span_id=00f067aa0ba902b7 msg=\"GET /orders/%d 200\" duration_ms=12.7 pod=orders-7d4b9c8f6d-x2kq9\n", i%60, i)
		}
	}
	return b.String()[:n]
}

// jsonPayload is a PodList of at least n bytes, as the Sensor returns one.
func jsonPayload(n int) string {
	var b strings.Builder
	b.WriteString(`{"kind":"PodList","items":[`)
	for i := 0; b.Len() < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"metadata":{"name":"orders-7d4b9c8f6d-%05d","namespace":"demo","uid":"3f2a8c1e-9b7d-4e6f-a5c3-%012d","labels":{"app":"orders","pod-template-hash":"7d4b9c8f6d"}},"spec":{"containers":[{"name":"orders","image":"ghcr.io/itsankoff/orders:v1.4.2","env":[{"name":"LOG_LEVEL","value":"info"},{"name":"DB_PASSWORD","value":"hunter2"},{"name":"STRIPE_KEY","value":"%s"}]}]},"status":{"phase":"Running","podIP":"10.244.0.%d","containerStatuses":[{"containerID":"containerd://4f5de0123456789abcdef0123456789abcdef0123456789abcdef0123456789ab","restartCount":%d}]}}`, i, i, stripeLive, i%250, i%9)
	}
	b.WriteString(`]}`)
	return b.String()
}
