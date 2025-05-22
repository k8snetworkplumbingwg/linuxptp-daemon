package version

import "testing"

func Test_VersionParing(t *testing.T) {
	cases := []struct {
		inpurt   string
		expected Version
	}{
		{"3.1.1-2.el8_6.3", Version{3, 1, 1, suffix{2, "el", 8, []int{6, 3}}}},
		{"3.1.1-6.el9_2.7", Version{3, 1, 1, suffix{6, "el", 9, []int{2, 7}}}},
		{"4.2-2.el9_4.3", Version{4, 2, 0, suffix{2, "el", 9, []int{4, 3}}}},
		{"4.4-1.el9", Version{4, 4, 0, suffix{1, "el", 9, []int{}}}},
	}

	for _, c := range cases {
		v, err := parse(c.inpurt)
		if err != nil {
			t.Errorf("Error parsing version %s: %s", c.inpurt, err)
		}
		if isVersionSame(v, c.expected) == false {
			t.Errorf("Version %v is not same as expected version %v", v, c.expected)
		}
	}
}

func Test_VersionString(t *testing.T) {
	cases := []string{
		"3.1.1-2.el8_6.3",
		"3.1.1-6.el9_2.7",
		"4.2-2.el9_4.3",
		"4.4-1.el9",
	}

	for _, c := range cases {
		v, err := parse(c)
		if err != nil {
			t.Errorf("Error parsing version %s: %s", c, err)
		}
		if v.String() != c {
			t.Errorf("Version %s is not same as expected version %v", v.String(), c)
		}
	}
}

func isSuffixSame(s1, s2 suffix) bool {
	if s1.leading != s2.leading {
		return false
	}
	if s1.repoPrefix != s2.repoPrefix {
		return false
	}
	if s1.repoVersion != s2.repoVersion {
		return false
	}
	if len(s1.trailing) != len(s2.trailing) {
		return false
	}
	for i, v := range s1.trailing {
		if v != s2.trailing[i] {
			return false
		}
	}
	return true
}

func isVersionSame(v1, v2 Version) bool {
	if v1.major != v2.major {
		return false
	}
	if v1.minor != v2.minor {
		return false
	}
	if v1.patch != v2.patch {
		return false
	}
	return isSuffixSame(v1.release, v2.release)
}

func Test_VersionComparison(t *testing.T) {
	cases := []struct {
		v1       Version
		v2       Version
		expected int
	}{
		{
			Version{1, 0, 0, suffix{0, "", 0, []int{}}},
			Version{1, 0, 0, suffix{0, "", 0, []int{}}},
			0,
		},
		{
			Version{2, 0, 0, suffix{0, "", 0, []int{}}},
			Version{1, 0, 0, suffix{0, "", 0, []int{}}},
			-1,
		},
		{
			Version{1, 0, 0, suffix{0, "", 0, []int{}}},
			Version{2, 0, 0, suffix{0, "", 0, []int{}}},
			1,
		},

		{
			Version{1, 1, 0, suffix{0, "", 0, []int{}}},
			Version{1, 0, 0, suffix{0, "", 0, []int{}}},
			-1,
		},
		{
			Version{1, 0, 0, suffix{0, "", 0, []int{}}},
			Version{1, 1, 0, suffix{0, "", 0, []int{}}},
			1,
		},

		{
			Version{1, 0, 1, suffix{0, "", 0, []int{}}},
			Version{1, 0, 0, suffix{0, "", 0, []int{}}},
			-1,
		},
		{
			Version{1, 0, 0, suffix{0, "", 0, []int{}}},
			Version{1, 0, 1, suffix{0, "", 0, []int{}}},
			1,
		},

		{
			Version{1, 1, 1, suffix{0, "", 0, []int{}}},
			Version{1, 0, 0, suffix{0, "", 0, []int{}}},
			-1,
		},
		{
			Version{1, 0, 0, suffix{0, "", 0, []int{}}},
			Version{1, 1, 1, suffix{0, "", 0, []int{}}},
			1,
		},

		{
			Version{1, 0, 0, suffix{1, "", 0, []int{}}},
			Version{1, 0, 0, suffix{0, "", 0, []int{}}},
			-1,
		},
		{
			Version{1, 0, 0, suffix{0, "", 0, []int{}}},
			Version{1, 0, 0, suffix{1, "", 0, []int{}}},
			1,
		},

		{
			Version{1, 0, 0, suffix{0, "b", 0, []int{}}},
			Version{1, 0, 0, suffix{0, "a", 0, []int{}}},
			-1,
		},
		{
			Version{1, 0, 0, suffix{0, "a", 0, []int{}}},
			Version{1, 0, 0, suffix{0, "b", 0, []int{}}},
			1,
		},

		{
			Version{1, 0, 0, suffix{0, "x", 1, []int{}}},
			Version{1, 0, 0, suffix{0, "x", 0, []int{}}},
			-1,
		},
		{
			Version{1, 0, 0, suffix{0, "x", 0, []int{}}},
			Version{1, 0, 0, suffix{0, "x", 1, []int{}}},
			1,
		},

		{
			Version{1, 0, 0, suffix{0, "x", 0, []int{1}}},
			Version{1, 0, 0, suffix{0, "x", 0, []int{0}}},
			-1,
		},
		{
			Version{1, 0, 0, suffix{0, "x", 0, []int{0}}},
			Version{1, 0, 0, suffix{0, "x", 0, []int{1}}},
			1,
		},

		{
			Version{1, 0, 0, suffix{0, "x", 0, []int{1, 1}}},
			Version{1, 0, 0, suffix{0, "x", 0, []int{1}}},
			-1,
		},
		{
			Version{1, 0, 0, suffix{0, "x", 0, []int{1}}},
			Version{1, 0, 0, suffix{0, "x", 0, []int{1, 1}}},
			1,
		},

		{
			Version{1, 0, 0, suffix{0, "x", 0, []int{1, 0}}},
			Version{1, 0, 0, suffix{0, "x", 0, []int{1}}},
			0,
		},
		{
			Version{1, 0, 0, suffix{0, "x", 0, []int{1}}},
			Version{1, 0, 0, suffix{0, "x", 0, []int{1, 0}}},
			0,
		},
	}

	for _, c := range cases {
		if res := c.v1.Compare(c.v2); res != c.expected {
			t.Errorf("Version comparison failed for %v and %v. Expected %d, got %d", c.v1, c.v2, c.expected, res)
		}
	}
}
