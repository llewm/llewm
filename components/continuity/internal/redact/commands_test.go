package redact

import (
	"strings"
	"testing"
)

func TestCommandCredentials(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{`export GITHUB_TOKEN=fixture-value`, `export GITHUB_TOKEN=[REDACTED]`},
		{`curl -H "Authorization: Bearer fixture-value"`, `curl -H "Authorization: Bearer [REDACTED]"`},
		{`curl -H "Authorization: token fixture-value"`, `curl -H "Authorization: [REDACTED]"`},
		{`curl -H "Authorization: Basic Zml4dHVyZTpwYXNz"`, `curl -H "Authorization: [REDACTED]"`},
		{`curl -H 'Authorization: Custom key=fixture, signature=other'`, `curl -H 'Authorization: [REDACTED]'`},
		{`mytool --token fixture-value --dry-run`, `mytool --token [REDACTED] --dry-run`},
		{`mytool --password "fixture with spaces"`, `mytool --password [REDACTED]`},
		{`mytool --secret 'fixture with spaces'`, `mytool --secret [REDACTED]`},
		{`mytool --api-key='fixture with spaces'`, `mytool --api-key=[REDACTED]`},
		{`mytool --api_key "fixture \"quoted\" value"`, `mytool --api_key [REDACTED]`},
		{`git clone https://user:fixture-value@example.test/path`, `git clone https://[REDACTED]@example.test/path`},
		{`git clone https://fixture-value@example.test/path`, `git clone https://[REDACTED]@example.test/path`},
		{`curl ftp://user:fixture-value@example.test/path`, `curl ftp://[REDACTED]@example.test/path`},
		{`curl -u admin:fixture-value https://example.test`, `curl -u [REDACTED] https://example.test`},
		{`curl --user 'admin:fixture value'`, `curl --user [REDACTED]`},
		{`aws configure set aws_secret_access_key fixture-value --profile test`, `aws configure set aws_secret_access_key [REDACTED] --profile test`},
		{`AWS_SESSION_TOKEN=fixture-value`, `AWS_SESSION_TOKEN=[REDACTED]`},
		{`AWS_ACCESS_KEY_ID='fixture-value'`, `AWS_ACCESS_KEY_ID=[REDACTED]`},
		{`echo sk-proj-abcdefghijklmnopqrstuv | some-cli login`, `echo [REDACTED] | some-cli login`},
		{`ghp_abcdefghijklmnopqrstuv github_pat_abcdefghijklmnopqrstuv`, `[REDACTED] [REDACTED]`},
		{`gho_abcdefghijklmnopqrstuv AKIAABCDEFGHIJKLMNOP`, `[REDACTED] [REDACTED]`},
		{`xoxb-1234567890-abcdefghijkl`, `[REDACTED]`},
		{`go test ./... --timeout 5m`, `go test ./... --timeout 5m`},
		{`token rotation: planned; --tokenizer sample; sk-short`, `token rotation: planned; --tokenizer sample; sk-short`},
		{`https://example.test/path user@example.test`, `https://example.test/path user@example.test`},
		{`task-sk-abcdefghijklmnopqrstuv`, `task-sk-abcdefghijklmnopqrstuv`},
		{`Authorization: Bearer [REDACTED]`, `Authorization: Bearer [REDACTED]`},
		{`token=[REDACTED] secret='[REDACTED]'`, `token=[REDACTED] secret='[REDACTED]'`},
		{`https://user:[REDACTED]example.test/path`, `https://user:[REDACTED]example.test/path`},
	} {
		t.Run(tc.input, func(t *testing.T) {
			got := String(tc.input)
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			if twice := String(got); twice != got {
				t.Fatalf("not idempotent: %q -> %q", got, twice)
			}
			if want := referenceString(tc.input); got != want {
				t.Fatalf("optimized %q differs from reference %q", got, want)
			}
			checkCandidate(t, tc.input)
		})
	}
}

func BenchmarkCommandRedaction(b *testing.B) {
	for _, size := range []int{1024, 64 * 1024} {
		text := strings.Repeat("ordinary text: source-owned project-guidance and task-status. ", size/60) + ` --token fixture`
		b.Run(strings.Repeat("x", size/1024), func(b *testing.B) {
			b.SetBytes(int64(len(text)))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = String(text)
			}
		})
	}
}
