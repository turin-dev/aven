package catalog

import (
	"strings"
	"testing"
)

func TestEntropy(t *testing.T) {
	if Entropy("") != 0 || Entropy("aaaa") != 0 {
		t.Error("zero entropy")
	}
	if e := Entropy("abcd"); e != 2 {
		t.Errorf("abcd: %v", e)
	}
}

func TestSecretLiterals(t *testing.T) {
	pem := "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAA\n-----END OPENSSH PRIVATE KEY-----"
	for _, tc := range []struct {
		in   string
		want []string // 찾아야 하는 글자들
		why  string
	}{
		{"node server.js --stripe-key=sk_live_51HxYzAbCdEf", []string{"sk_live_51HxYzAbCdEf"}, "sk_"},
		{"git clone https://x:ghp_aB3dE5gH7jK9mN1pQ3sT5vW7yZ@github.com/a/b", []string{"ghp_aB3dE5gH7jK9mN1pQ3sT5vW7yZ"}, "ghp_"},
		{"TOKEN=github_pat_11ABCDEFG0123456789_abcdefghijklmnop", []string{"github_pat_11ABCDEFG0123456789_abcdefghijklmnop"}, "github_pat_"},
		{`curl -H "Authorization: Bearer xoxb-1234567890-abcdefABCDEF" x`, []string{"xoxb-1234567890-abcdefABCDEF"}, "xox"},
		{"aws configure set aws_access_key_id AKIAIOSFODNN7EXAMPLE", []string{"AKIAIOSFODNN7EXAMPLE"}, "AKIA"},
		{"echo '" + pem + "' > key", []string{pem}, "-----BEGIN"},
		{"api --secret 9fK2mQx7Lp4Zr8Tn3Vb6Wc1Yd5", []string{"9fK2mQx7Lp4Zr8Tn3Vb6Wc1Yd5"}, "entropy"},
		{"X=3f9a7c2e4b1d8f6a0c5e9b3d7f2a4c6e", []string{"3f9a7c2e4b1d8f6a0c5e9b3d7f2a4c6e"}, "entropy"},
		{"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U", []string{"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U"}, "entropy"},
	} {
		spans := SecretLiterals(tc.in)
		var got []string
		for _, sp := range spans {
			got = append(got, tc.in[sp.Start:sp.End])
		}
		if strings.Join(got, "|") != strings.Join(tc.want, "|") || spans[0].Reason != tc.why {
			t.Errorf("%q: %q %+v", tc.in, got, spans)
		}
	}
	// 비밀이 아닌 것: 경로, 파일 이름, 상수 이름, UUID, 낱말, 짧은 접두사, ${{ }} 표현식
	for _, in := range []string{
		"node dist/server-bundle-v2.3.1.min.js --port 3000",
		"/usr/local/bin/my-application-server-2024 --config /etc/app/config.production.yaml",
		"SOME_VERY_LONG_CONSTANT_NAME_V2=1",
		"id 550e8400-e29b-41d4-a716-446655440000",
		"python manage.py collectstatic --noinput && gunicorn app.wsgi",
		"com.example.MyApplicationServer2",
		"sk_test",
		"echo ${{ secret(32) }} ${{ db.DATABASE_PRIVATE_URL_WITH_LONG_NAME_123 }}",
		"https://example.com/assets/images/background-large.png",
	} {
		if spans := SecretLiterals(in); len(spans) != 0 {
			t.Errorf("%q: false positive %q", in, in[spans[0].Start:spans[0].End])
		}
	}
}

func TestReplaceSecretLiterals(t *testing.T) {
	in := "run --a sk_live_51HxYzAbCdEf --b ghp_aB3dE5gH7jK9mN1pQ3sT5vW7yZ end"
	n := 0
	out, spans := ReplaceSecretLiterals(in, func(SecretSpan) string { n++; return "<S>" })
	if out != "run --a <S> --b <S> end" || len(spans) != 2 || n != 2 {
		t.Errorf("%q %d", out, len(spans))
	}
	if out, spans := ReplaceSecretLiterals("plain text", nil); out != "plain text" || spans != nil {
		t.Errorf("%q", out)
	}
}
