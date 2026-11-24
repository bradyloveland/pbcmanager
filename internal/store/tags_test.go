package store

import (
	"reflect"
	"testing"
)

func TestClientTags(t *testing.T) {
	s := open(t, t.TempDir())
	c := &Client{ID: "c1", Name: "NAS", Address: "192.0.2.5", Port: 22, Status: ClientReady}
	if err := s.CreateClient(c); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetClient("c1"); got.Tags == nil || len(got.Tags) != 0 {
		t.Fatalf("a new client has no tags: %#v", got.Tags)
	}
	if err := s.SetClientTags("c1", []string{"home", "Raspberry Pi"}); err != nil {
		t.Fatal(err)
	}
	// A check-in saving the client it read earlier doesn't undo the tags.
	c.LastContact = 123
	if err := s.SaveClient(c); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetClient("c1")
	if !reflect.DeepEqual(got.Tags, []string{"home", "Raspberry Pi"}) || got.LastContact != 123 {
		t.Fatalf("after SaveClient: %+v", got)
	}
	if err := s.SetClientTags("nope", nil); err != ErrNotFound {
		t.Fatalf("unknown client: %v", err)
	}
}

func TestCleanTags(t *testing.T) {
	got, err := CleanTags([]string{"  home  lab ", "nas", "NAS", "", "Pi"}, []string{"NAS", "offsite"})
	if err != nil || !reflect.DeepEqual(got, []string{"home lab", "NAS", "Pi"}) {
		t.Fatalf("got %q, %v", got, err)
	}
	if got, err := CleanTags(nil, nil); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("no tags: %#v %v", got, err)
	}
	for _, bad := range [][]string{
		{"a,b"},
		{"this tag is much longer than thirty-two characters"},
		{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11"},
	} {
		if _, err := CleanTags(bad, nil); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
}
