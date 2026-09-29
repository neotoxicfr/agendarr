// Package ics writes an iCalendar (RFC 5545) feed of release events, for
// subscription from iOS, Google Calendar or Outlook.
package ics

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/neotoxicfr/agendarr/internal/calendar"
)

// Labels are the few words the feed needs, in the user's language.
type Labels struct {
	CalName     string // X-WR-CALNAME
	Release     map[string]string
	Available   string
	Expected    string
	Unplanned   string
	RequestedBy string
}

// Write renders events as a VCALENDAR. now stamps every event (DTSTAMP).
func Write(w io.Writer, events []calendar.Event, l Labels, now time.Time) error {
	b := bufio.NewWriter(w)
	line := func(s string) { writeFolded(b, s) }
	line("BEGIN:VCALENDAR")
	line("VERSION:2.0")
	line("PRODID:-//agendarr//agendarr//EN")
	line("CALSCALE:GREGORIAN")
	line("METHOD:PUBLISH")
	line("X-WR-CALNAME:" + escape(l.CalName))
	// Hints for how often clients should poll; Google ignores them.
	line("REFRESH-INTERVAL;VALUE=DURATION:PT1H")
	line("X-PUBLISHED-TTL:PT1H")
	stamp := now.UTC().Format("20060102T150405Z")
	for _, e := range events {
		line("BEGIN:VEVENT")
		line("UID:" + escape(e.ID) + "@agendarr")
		line("DTSTAMP:" + stamp)
		if e.Air != nil {
			start := e.Air.UTC()
			d := time.Duration(e.Runtime) * time.Minute
			if d <= 0 {
				d = 30 * time.Minute
			}
			line("DTSTART:" + start.Format("20060102T150405Z"))
			line("DTEND:" + start.Add(d).Format("20060102T150405Z"))
		} else {
			day, err := time.Parse(time.DateOnly, e.Date)
			if err != nil {
				line("END:VEVENT")
				continue
			}
			line("DTSTART;VALUE=DATE:" + day.Format("20060102"))
			line("DTEND;VALUE=DATE:" + day.AddDate(0, 0, 1).Format("20060102"))
			line("TRANSP:TRANSPARENT") // an all-day release does not block the day
		}
		line("SUMMARY:" + escape(summary(e, l)))
		line("DESCRIPTION:" + escape(description(e, l)))
		cat := "Movie"
		if e.Kind == "episode" {
			cat = "Episode"
		}
		line("CATEGORIES:" + cat)
		line("END:VEVENT")
	}
	line("END:VCALENDAR")
	return b.Flush()
}

func summary(e calendar.Event, l Labels) string {
	if e.Kind == "episode" {
		return fmt.Sprintf("%s S%02dE%02d", e.Title, e.Season, e.Episode)
	}
	if r := l.Release[e.Release]; r != "" {
		return e.Title + " (" + r + ")"
	}
	return e.Title
}

func description(e calendar.Event, l Labels) string {
	var parts []string
	if e.EpTitle != "" {
		parts = append(parts, e.EpTitle)
	}
	switch {
	case e.HasFile:
		parts = append(parts, l.Available)
	case e.Monitored:
		parts = append(parts, l.Expected)
	default:
		parts = append(parts, l.Unplanned)
	}
	if len(e.RequestedBy) > 0 && l.RequestedBy != "" {
		parts = append(parts, l.RequestedBy+" "+strings.Join(e.RequestedBy, ", "))
	}
	if e.Overview != "" {
		parts = append(parts, "", e.Overview)
	}
	return strings.Join(parts, "\n")
}

// escape applies RFC 5545 TEXT escaping.
func escape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, ";", `\;`, ",", `\,`, "\r\n", `\n`, "\n", `\n`, "\r", `\n`)
	return r.Replace(s)
}

// writeFolded writes a content line, folded at 75 octets without splitting
// a UTF-8 sequence, terminated by CRLF.
func writeFolded(b *bufio.Writer, s string) {
	const max = 75
	first := true
	for len(s) > 0 {
		limit := max
		if !first {
			limit = max - 1 // the leading space counts
			b.WriteByte(' ')
		}
		if len(s) <= limit {
			b.WriteString(s)
			break
		}
		cut := limit
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		b.WriteString(s[:cut])
		b.WriteString("\r\n")
		s = s[cut:]
		first = false
	}
	b.WriteString("\r\n")
}
