package ics

import (
	"bytes"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/neotoxicfr/agendarr/internal/calendar"
)

func TestWrite(t *testing.T) {
	air := time.Date(2026, 10, 4, 20, 0, 0, 0, time.UTC)
	ev := []calendar.Event{
		{ID: "movie:tmdb:1:digital", Kind: "movie", Release: "digital", Title: "Dune, part two; the return", Date: "2026-10-02", Monitored: true},
		{ID: "series:tvdb:7:s01e02", Kind: "episode", Title: "Andor", Season: 1, Episode: 2, EpTitle: "Épisode très long " + strings.Repeat("é", 60),
			Air: &air, Runtime: 45, HasFile: true, RequestedBy: []string{"neo"}, Overview: "Line one\nline two"},
	}
	var buf bytes.Buffer
	if err := Write(&buf, ev, Labels{CalName: "agendarr", Release: map[string]string{"digital": "Numérique"},
		Available: "Disponible", Expected: "Attendu", RequestedBy: "Demandé par"}, time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"BEGIN:VCALENDAR\r\n", "END:VCALENDAR\r\n",
		"DTSTART;VALUE=DATE:20261002\r\n", "DTEND;VALUE=DATE:20261003\r\n",
		`SUMMARY:Dune\, part two\; the return (Numérique)`,
		"DTSTART:20261004T200000Z\r\n", "DTEND:20261004T204500Z\r\n",
		"SUMMARY:Andor S01E02\r\n", "UID:series:tvdb:7:s01e02@agendarr\r\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, l := range strings.Split(strings.TrimSuffix(out, "\r\n"), "\r\n") {
		if len(l) > 75 {
			t.Errorf("line longer than 75 octets: %q", l)
		}
		if !utf8.ValidString(l) {
			t.Errorf("fold split a UTF-8 sequence: %q", l)
		}
	}
	// Unfolding gives back the escaped description.
	unfolded := strings.ReplaceAll(out, "\r\n ", "")
	if !strings.Contains(unfolded, `Disponible\nDemandé par neo\n\nLine one\nline two`) {
		t.Errorf("description: %s", unfolded)
	}
}
