package featureflags

import (
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
