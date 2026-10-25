package deploy

import "testing"

func TestSafeJoin(t *testing.T) {
	root := "/work/dep_x"
	for in, want := range map[string]string{".": root, "Dockerfile": root + "/Dockerfile", "a/../b": root + "/b", "../../etc/passwd": root + "/etc/passwd"} {
		got, err := safeJoin(root, in)
		if err != nil || got != want {
			t.Errorf("safeJoin(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}
