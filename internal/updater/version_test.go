package updater

import "testing"

func TestParseVersion(t *testing.T) {
	cases := []struct {
		in    string
		ok    bool
		major int
		minor int
		patch int
		pre   string
		dev   bool
		base  string
	}{
		{in: "v0.6.1", ok: true, minor: 6, patch: 1, base: "v0.6.1"},
		{in: "0.6.1", ok: true, minor: 6, patch: 1, base: "v0.6.1"},
		{in: "v1.10.0", ok: true, major: 1, minor: 10, base: "v1.10.0"},
		{in: "v0.1.3-rc1", ok: true, minor: 1, patch: 3, pre: "rc1", base: "v0.1.3-rc1"},
		{in: "v0.6.1-3-g29a95a4", ok: true, minor: 6, patch: 1, dev: true, base: "v0.6.1"},
		{in: "dev", ok: false},
		{in: "", ok: false},
		{in: "v0.6", ok: false},
		{in: "v0.6.x", ok: false},
	}
	for _, c := range cases {
		got, ok := ParseVersion(c.in)
		if ok != c.ok {
			t.Fatalf("ParseVersion(%q) ok=%v，期望 %v", c.in, ok, c.ok)
		}
		if !ok {
			continue
		}
		if got.Major != c.major || got.Minor != c.minor || got.Patch != c.patch ||
			got.Pre != c.pre || got.Dev != c.dev {
			t.Fatalf("ParseVersion(%q) = %+v", c.in, got)
		}
		if got.Base() != c.base {
			t.Fatalf("ParseVersion(%q).Base() = %q，期望 %q", c.in, got.Base(), c.base)
		}
	}
}

func TestNewer(t *testing.T) {
	cases := []struct {
		cur, tag string
		want     bool
		why      string
	}{
		{cur: "v0.6.1", tag: "v0.6.2", want: true, why: "补丁号更大"},
		{cur: "v0.6.1", tag: "v0.7.0", want: true},
		{cur: "v0.9.9", tag: "v1.0.0", want: true},
		{cur: "v0.6.1", tag: "v0.6.1", want: false},
		{cur: "v0.6.2", tag: "v0.6.1", want: false, why: "不往回退"},
		{cur: "v0.1.3-rc1", tag: "v0.1.3", want: true, why: "预发布之后是正式版"},
		{cur: "v0.1.3", tag: "v0.1.3-rc1", want: false},
		// 开发版排在同号正式版之后：拿 v0.6.1 覆盖基于它的开发版是降级
		{cur: "v0.6.1-3-g29a95a4", tag: "v0.6.1", want: false, why: "开发版比它的基准正式版新"},
		{cur: "v0.6.1-3-g29a95a4", tag: "v0.6.2", want: true, why: "下一个正式版仍算升级"},
		// 认不出来的一律不提示更新
		{cur: "dev", tag: "v0.6.2", want: false},
		{cur: "v0.6.1", tag: "dev", want: false},
	}
	for _, c := range cases {
		if got := Newer(c.cur, c.tag); got != c.want {
			t.Errorf("Newer(%q, %q) = %v，期望 %v %s", c.cur, c.tag, got, c.want, c.why)
		}
	}
}
