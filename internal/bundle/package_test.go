package bundle

import "testing"

func TestPackageInfoUpdates(t *testing.T) {
	cases := []struct {
		p             PackageInfo
		update, major bool
	}{
		{PackageInfo{FromApt: true, Newer: true, Installed: "3.4.6-1", Candidate: "3.4.7-1"}, true, false},
		{PackageInfo{FromApt: true, Newer: true, Installed: "3.4.7-1", Candidate: "4.2.7-1"}, true, true},
		{PackageInfo{FromApt: true, Newer: true, Installed: "1:3.4.7-1", Candidate: "1:10.0.0-1"}, true, true},
		{PackageInfo{FromApt: true, Newer: false, Installed: "3.4.7-1", Candidate: "3.4.7-1"}, false, false},
		{PackageInfo{FromApt: false, Newer: true, Installed: "3.4.7", Candidate: "4.0.0-1"}, false, false},
		{PackageInfo{FromApt: true, Newer: true, Installed: "3.4.7-1", Candidate: ""}, false, false},
	}
	for _, c := range cases {
		if c.p.UpdateAvailable() != c.update || c.p.MajorUpdate() != c.major {
			t.Errorf("%+v: update %v major %v", c.p, c.p.UpdateAvailable(), c.p.MajorUpdate())
		}
	}
	var none *PackageInfo
	if none.UpdateAvailable() {
		t.Error("no info means no update")
	}
}
