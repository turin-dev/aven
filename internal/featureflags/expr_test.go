package featureflags

import (
	"encoding/json"
	"math"
	"strconv"
	"testing"
)

func TestEvaluateTargetingExpressions(t *testing.T) {
	ctx := map[string]any{"plan": "enterprise", "region": "eu-west", "age": float64(24), "enabled": true, "tags": []any{"early", "beta"}}
	tests := []struct {
		name string
		expr string
		want bool
	}{
		{"attribute equality", `plan == "enterprise"`, true},
		{"boolean logic", `plan == "enterprise" && (region == "eu-west" || age < 18)`, true},
		{"negation", `!(plan == "free")`, true},
		{"membership", `plan in ["free", "enterprise"]`, true},
		{"string contains", `region contains "west"`, true},
		{"regex", `region matches "^eu-.*"`, true},
		{"missing attribute", `missing == "yes"`, false},
		{"negated missing attribute", `!(missing == "yes")`, false},
		{"bad comparison", `age > "old"`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Evaluate(tt.expr, "checkout", ctx)
			if err != nil || got != tt.want {
				t.Fatalf("Evaluate(%q) = %v, %v; want %v", tt.expr, got, err, tt.want)
			}
		})
	}
}

func TestStableBucketAndPercentageExpression(t *testing.T) {
	const flag, key = "checkout-v2", "user-42"
	one := StableBucket(flag, key)
	if one < 0 || one >= 1 || math.IsNaN(one) || one != StableBucket(flag, key) {
		t.Fatalf("unstable bucket: %v", one)
	}
	ctx := map[string]any{"key": key}
	for _, pct := range []float64{0, 0.25, 0.5, 1} {
		expr := "bucket(key) < " + formatFloat(pct)
		got, err := Evaluate(expr, flag, ctx)
		if err != nil || got != (one < pct) {
			t.Fatalf("Evaluate(%q) = %v, %v; bucket=%v", expr, got, err, one)
		}
	}
	if got, err := Evaluate("bucket(key) < 1", flag, nil); err != nil || got {
		t.Fatalf("missing key should not match: %v %v", got, err)
	}
}

func TestStableBucketVectors(t *testing.T) {
	// The same vectors are asserted in sdk/index.test.ts so server and SDK bucket identically.
	for key, want := range map[string]float64{"user-42": 0.84299246949562123, "user_8f3a": 0.92247836883631251, "한글": 0.88976060845657412} {
		if got := StableBucket("checkout-v2", key); got != want {
			t.Errorf("StableBucket(checkout-v2, %q) = %.17g, want %.17g", key, got, want)
		}
	}
}

func TestStableBucketDistribution(t *testing.T) {
	// Sequential keys must spread evenly: a 25 % rollout should hit about 25 % of user-0..user-9999.
	for _, pct := range []float64{0.01, 0.1, 0.25, 0.5, 0.9} {
		in := 0
		for i := 0; i < 10000; i++ {
			if StableBucket("checkout-v2", "user-"+strconv.Itoa(i)) < pct {
				in++
			}
		}
		if got := float64(in) / 10000; math.Abs(got-pct) > 0.02 {
			t.Errorf("rollout %.2f hit %.4f of sequential keys", pct, got)
		}
	}
}

func TestValidateExpressions(t *testing.T) {
	for _, expression := range []string{
		`plan == "enterprise" && bucket(key) < 0.25`,
		`version >= 2 || region in ["eu", "us"]`,
		`!false`,
	} {
		if err := Validate(expression); err != nil {
			t.Errorf("Validate(%q): %v", expression, err)
		}
	}
	for _, expression := range []string{"", `plan = "enterprise"`, `bucket() < 0.5`, `plan in ["x"`, stringsRepeat("x", MaxExpressionLength+1)} {
		if err := Validate(expression); err == nil {
			t.Errorf("Validate(%q) unexpectedly succeeded", expression)
		}
	}
}

func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func stringsRepeat(s string, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = s[0]
	}
	return string(b)
}

func TestJSNumberMatchesJavaScriptString(t *testing.T) {
	// 오른쪽은 node -e 'console.log(String(x))'의 결과다.
	for n, want := range map[float64]string{
		0: "0", math.Copysign(0, -1): "0", 1: "1", -42: "-42", 1.5: "1.5", 0.1: "0.1",
		1234567: "1234567", 123456789012: "123456789012", 1e20: "100000000000000000000", 1e21: "1e+21",
		1.5e300: "1.5e+300", 0.000001: "0.000001", 1e-7: "1e-7", 1.5e-7: "1.5e-7", -2.5e-10: "-2.5e-10",
		9007199254740993: "9007199254740992",
	} {
		if got := jsNumber(n); got != want {
			t.Errorf("jsNumber(%v) = %q, want %q", n, got, want)
		}
	}
}

func TestBucketNumericKeyMatchesStringKey(t *testing.T) {
	// SDK는 숫자 속성을 String(value)로 해시한다. 서버도 1234567을 "1234567"로 해시해야 같은 bucket이다.
	for _, n := range []float64{7, 1234567, 98765432101} {
		s := jsNumber(n)
		want := StableBucket("f", s)
		for _, expr := range []string{"bucket(id) < " + formatFloat(want+1e-12), "bucket(id) >= " + formatFloat(want)} {
			got, err := Evaluate(expr, "f", map[string]any{"id": n})
			if err != nil || !got {
				t.Errorf("%v: %s = %v %v", n, expr, got, err)
			}
		}
	}
}

func TestEvaluateEdgeCases(t *testing.T) {
	ctx := map[string]any{
		"plan": "pro", "age": float64(30), "tags": []any{"a", "b"}, "one": []any{"a b"}, "beta": true,
		"nested": map[string]any{"x": float64(1)}, "nul": nil, "pattern": "([", "n": json.Number("5"),
	}
	for _, tc := range []struct {
		expr string
		want bool
	}{
		{`tags == ["a", "b"]`, true},
		{`tags == ["a b"]`, false}, // 예전 fmt 비교는 [a b]로 같게 보았다
		{`one == ["a", "b"]`, false},
		{`tags != ["a"]`, true},
		{`age == 30`, true},
		{`age == "30"`, false},
		{`n == 5`, true},
		{`n > 4.5`, true},
		{`beta == true`, true},
		{`beta == "true"`, false},
		{`nul == null`, false}, // null 속성은 없는 것과 같다
		{`!(nul == null)`, false},
		{`plan in ["free", "pro"]`, true},
		{`plan in []`, false},
		{`plan in [missing, "pro"]`, false},
		{`tags contains "b"`, true},
		{`age contains "3"`, false},
		{`age contains "3" || plan == "pro"`, true}, // 타입이 안 맞는 contains는 false일 뿐 규칙 전체를 깨지 않는다
		{`age matches "3"`, false},
		{`plan matches pattern || beta`, true}, // 속성에서 온 잘못된 정규식
		{`plan < "zzz" && plan >= "pro"`, true},
		{`age < "x"`, false},
		{`missing == 1 || plan == "pro"`, true},
		{`missing == 1 && plan == "pro"`, false},
		{`!missing`, false},
		{`!!beta`, true},
		{`beta && (age >= 18 && age <= 65)`, true},
		{`-1 < age && .5 < 1 && 1e2 == 100`, true},
		{`"a\"b" == "a\"b"`, true},
		{`targetingKey == "x"`, false},
	} {
		got, err := Evaluate(tc.expr, "flag", ctx)
		if err != nil || got != tc.want {
			t.Errorf("Evaluate(%q) = %v, %v; want %v", tc.expr, got, err, tc.want)
		}
	}
	// 잘못된 식은 오류다
	for _, expr := range []string{`"open`, `"bad\x"`, `a == `, `(a == 1`, `a == 1 == 2`, `bucket(1) < 1`, `a $ b`, `1-2 == 1`, `[1, 2`} {
		if _, err := Evaluate(expr, "flag", ctx); err == nil {
			t.Errorf("Evaluate(%q) succeeded", expr)
		}
	}
}

func TestValidateMore(t *testing.T) {
	for _, expr := range []string{`a in "x"`, `a matches "("`, `a matches 1`, `plan`, `1`, `"x"`, `a && 1`} {
		if err := Validate(expr); err == nil {
			t.Errorf("Validate(%q) succeeded", expr)
		}
	}
	for _, expr := range []string{`a matches b`, `a in []`, `true`, `(a == 1) && !(b != 2)`} {
		if err := Validate(expr); err != nil {
			t.Errorf("Validate(%q): %v", expr, err)
		}
	}
}

// FuzzEvaluate는 아무 식에도 패닉하지 않는지 본다.
func FuzzEvaluate(f *testing.F) {
	for _, s := range []string{`a == 1`, `bucket(k) < 0.5`, `!(a in [1, "x"])`, `a matches "^x"`, `((((`, `"\u00e9"`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		_ = Validate(s)
		_, _ = Evaluate(s, "f", map[string]any{"a": float64(1), "k": "u", "l": []any{"x"}})
	})
}
