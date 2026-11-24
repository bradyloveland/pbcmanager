package store

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Limits on client tags.
const (
	MaxTags   = 10
	MaxTagLen = 32
)

// CleanTags tidies tags typed by the user: spaces trimmed and runs of spaces
// made one, duplicates dropped ignoring case, and a tag that another client
// already has spelled the same way it is there ("nas" becomes "NAS"), so the
// same tag never makes two groups. known are the tags in use.
func CleanTags(in, known []string) ([]string, error) {
	spelling := map[string]string{}
	for _, k := range known {
		if _, ok := spelling[strings.ToLower(k)]; !ok {
			spelling[strings.ToLower(k)] = k
		}
	}
	out, seen := []string{}, map[string]bool{}
	for _, t := range in {
		t = strings.Join(strings.Fields(t), " ")
		if t == "" {
			continue
		}
		if utf8.RuneCountInString(t) > MaxTagLen {
			return nil, fmt.Errorf("“%s” is too long: a tag can be up to %d characters", t, MaxTagLen)
		}
		if strings.ContainsRune(t, ',') || strings.IndexFunc(t, unicode.IsControl) >= 0 {
			return nil, fmt.Errorf("“%s” can't be a tag: tags can't contain commas", t)
		}
		key := strings.ToLower(t)
		if seen[key] {
			continue
		}
		seen[key] = true
		if s, ok := spelling[key]; ok {
			t = s
		}
		out = append(out, t)
	}
	if len(out) > MaxTags {
		return nil, fmt.Errorf("a client can have up to %d tags", MaxTags)
	}
	return out, nil
}

// AllTags lists the tags clients have, each once, in the order first seen.
func AllTags(clients []*Client) []string {
	out, seen := []string{}, map[string]bool{}
	for _, c := range clients {
		for _, t := range c.Tags {
			if !seen[strings.ToLower(t)] {
				seen[strings.ToLower(t)] = true
				out = append(out, t)
			}
		}
	}
	return out
}
