/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * BuiltinDatePlugin — calendar arithmetic as an @date ReAct tool.
 *
 * Language models read dates well and count them badly: "how many days
 * between the 14th of March and the 17th of April", "what day was last
 * Saturday", "three weeks before the 5th". Every verb here is pure,
 * offline and deterministic; it touches no file, network or credential, and
 * counts from CHATCLI_NOW when it is pinned (otherwise the wall clock), the
 * same reference every other date the model sees is measured from.
 */
package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/utils"
)

// BuiltinDatePlugin is the @date tool.
type BuiltinDatePlugin struct{}

// NewBuiltinDatePlugin returns a ready-to-register plugin.
func NewBuiltinDatePlugin() *BuiltinDatePlugin { return &BuiltinDatePlugin{} }

// Name returns "@date".
func (*BuiltinDatePlugin) Name() string { return "@date" }

// Description surfaces the tool in the catalog (i18n).
func (*BuiltinDatePlugin) Description() string { return i18n.T("plugins.date.description") }

// Usage explains the canonical invocation.
func (*BuiltinDatePlugin) Usage() string {
	return `<tool_call name="@date" args='{"cmd":"diff","args":{"from":"2023-03-14","to":"2023-04-17"}}' />

Dates: 2023-03-14, 2023/03/14, 2023-03-14 18:11, RFC 3339, or the form the
@session results print (2023-03-14 (Tue) 18:11); "today" and "now" mean the
current date. Counting starts from the current date (CHATCLI_NOW when pinned).

Subcommands (cmd + args):
  now      {}                                 the current date, weekday and time
  diff     {from, to}                         calendar days, weeks, months and the
                                              elapsed time between two dates
  add      {date, days?, weeks?, months?, years?}
                                              the date that many units later
                                              (negative = earlier)
  weekday  {date}                             the weekday of a date
  resolve  {expr, as_of?}                     a relative expression as a date:
                                              "last saturday", "next friday",
                                              "3 weeks ago", "yesterday"`
}

// Version is semver.
func (*BuiltinDatePlugin) Version() string { return "1.0.0" }

// Path is empty for builtin plugins.
func (*BuiltinDatePlugin) Path() string { return "" }

// Schema describes the subcommands.
func (*BuiltinDatePlugin) Schema() string {
	dateFlag := func(name, desc string, req bool) map[string]interface{} {
		return map[string]interface{}{"name": name, "type": "string", "required": req, "description": desc}
	}
	numFlag := func(name, desc string) map[string]interface{} {
		return map[string]interface{}{"name": name, "type": "integer", "required": false, "description": desc}
	}
	schema := map[string]interface{}{
		"argsFormat": "JSON envelope {cmd, args}; flat {from,to} defaults to diff, {date,days} to add, {expr} to resolve",
		"subcommands": []map[string]interface{}{
			{"name": "now", "description": "The current date, weekday and time (CHATCLI_NOW when pinned).",
				"flags":    []map[string]interface{}{},
				"examples": []string{`{"cmd":"now","args":{}}`}},
			{"name": "diff", "description": "Calendar days, weeks, months and elapsed time between two dates.",
				"flags": []map[string]interface{}{
					dateFlag("from", "Start date (2023-03-14, 2023-03-14 18:11, \"today\")", true),
					dateFlag("to", "End date, same forms (default: today)", false)},
				"examples": []string{`{"cmd":"diff","args":{"from":"2023-03-14","to":"2023-04-17"}}`}},
			{"name": "add", "description": "A date moved by days, weeks, months or years (negative = earlier).",
				"flags": []map[string]interface{}{
					dateFlag("date", "Base date (default: today)", false),
					numFlag("days", "Days to add"), numFlag("weeks", "Weeks to add"),
					numFlag("months", "Months to add"), numFlag("years", "Years to add")},
				"examples": []string{`{"cmd":"add","args":{"date":"2023-04-05","weeks":-3}}`}},
			{"name": "weekday", "description": "The weekday of a date.",
				"flags":    []map[string]interface{}{dateFlag("date", "Date (default: today)", false)},
				"examples": []string{`{"cmd":"weekday","args":{"date":"2023-04-22"}}`}},
			{"name": "resolve", "description": "A relative expression as a concrete date, counted from as_of (default: today).",
				"flags": []map[string]interface{}{
					dateFlag("expr", "yesterday, tomorrow, last|next <weekday>, N days|weeks|months|years ago, in N days|weeks|months", true),
					dateFlag("as_of", "Date the expression is counted from (default: today)", false)},
				"examples": []string{`{"cmd":"resolve","args":{"expr":"last saturday","as_of":"2023-04-22"}}`, `{"cmd":"resolve","args":{"expr":"3 weeks ago"}}`}},
		},
	}
	data, _ := json.Marshal(schema)
	return string(data)
}

// Execute parses args and dispatches.
func (p *BuiltinDatePlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream ignores the stream callback: every verb answers at once.
func (p *BuiltinDatePlugin) ExecuteWithStream(_ context.Context, args []string, _ func(string)) (string, error) {
	cmd, raw, err := parseDateToolInvocation(args)
	if err != nil {
		return "", fmt.Errorf("@date: %w", err)
	}
	out, err := runDateVerb(cmd, raw, utils.ReferenceNow())
	if err != nil {
		return "", fmt.Errorf("@date %s: %w", cmd, err)
	}
	return out, nil
}

func parseDateToolInvocation(args []string) (string, map[string]json.RawMessage, error) {
	return parseVerbInvocation(args, canonicalDateCmd, inferDateCmd, "date",
		nil, map[string]bool{"days": true, "weeks": true, "months": true, "years": true})
}

func canonicalDateCmd(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "now", "today", "current":
		return "now"
	case "diff", "difference", "between", "delta", "days_between", "datediff", "date_diff":
		return "diff"
	case "add", "plus", "shift", "offset":
		return "add"
	case "weekday", "dow", "day_of_week":
		return "weekday"
	case "resolve", "parse", "relative":
		return "resolve"
	}
	return ""
}

// inferDateCmd picks the verb for flat native args.
func inferDateCmd(raw map[string]json.RawMessage) string {
	switch {
	case jsonHas(raw, "expr", "expression", "phrase"):
		return "resolve"
	case jsonHas(raw, "from", "to", "start", "end"):
		return "diff"
	case jsonHas(raw, "days", "weeks", "months", "years"):
		return "add"
	case jsonHas(raw, "date"):
		return "weekday"
	}
	return "now"
}

func runDateVerb(cmd string, raw map[string]json.RawMessage, now time.Time) (string, error) {
	switch cmd {
	case "now":
		return "now: " + fmtDateTime(now), nil
	case "diff":
		from, err := parseDateArg(jsonString(raw, "from", "start", "a"), now)
		if err != nil {
			return "", fmt.Errorf(`"from": %w`, err)
		}
		toRaw := jsonString(raw, "to", "end", "b")
		to := now
		if toRaw != "" {
			if to, err = parseDateArg(toRaw, now); err != nil {
				return "", fmt.Errorf(`"to": %w`, err)
			}
		}
		return dateDiffReport(from, to), nil
	case "add":
		base := now
		if s := jsonString(raw, "date", "from", "base"); s != "" {
			var err error
			if base, err = parseDateArg(s, now); err != nil {
				return "", fmt.Errorf(`"date": %w`, err)
			}
		}
		days, weeks := jsonInt(raw, "days", "day"), jsonInt(raw, "weeks", "week")
		months, years := jsonInt(raw, "months", "month"), jsonInt(raw, "years", "year")
		if days == 0 && weeks == 0 && months == 0 && years == 0 {
			return "", fmt.Errorf(`give at least one of "days", "weeks", "months" or "years"`)
		}
		const bound = 100000
		if abs(days) > bound || abs(weeks) > bound || abs(months) > bound || abs(years) > bound {
			return "", fmt.Errorf("offsets are limited to ±%d", bound)
		}
		out := addCalendar(base, years, months).AddDate(0, 0, days+7*weeks)
		return fmt.Sprintf("%s %s = %s", fmtDay(base), describeOffset(years, months, weeks, days), fmtDay(out)), nil
	case "weekday":
		d := now
		if s := jsonString(raw, "date", "day"); s != "" {
			var err error
			if d, err = parseDateArg(s, now); err != nil {
				return "", fmt.Errorf(`"date": %w`, err)
			}
		}
		return fmtDay(d), nil
	case "resolve":
		expr := jsonString(raw, "expr", "expression", "phrase")
		if strings.TrimSpace(expr) == "" {
			return "", fmt.Errorf(`"expr" is required`)
		}
		asOf := now
		if s := jsonString(raw, "as_of", "asof", "from"); s != "" {
			var err error
			if asOf, err = parseDateArg(s, now); err != nil {
				return "", fmt.Errorf(`"as_of": %w`, err)
			}
		}
		return resolveRelativeDate(expr, asOf)
	}
	return "", fmt.Errorf("unknown cmd %q (valid: now|diff|add|weekday|resolve)", cmd)
}

// --- parsing ------------------------------------------------------------

// weekdayParenRe drops the "(Tue)" the @session results print between the
// date and the time.
var weekdayParenRe = regexp.MustCompile(`(?i)\s*\((?:mon|tue|wed|thu|fri|sat|sun)[a-z]*\)`)

var dateLayouts = []string{
	"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02T15:04:05", "2006-01-02T15:04",
	"2006/01/02 15:04:05", "2006/01/02 15:04", "2006-01-02", "2006/01/02",
}

// parseDateArg reads one date in the forms the tool documents, in now's
// location. "today" and "now" are the reference time itself.
func parseDateArg(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	switch strings.ToLower(s) {
	case "":
		return time.Time{}, fmt.Errorf("a date is required")
	case "today", "now", "hoje", "agora":
		return now, nil
	}
	s = strings.TrimSpace(weekdayParenRe.ReplaceAllString(s, ""))
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.In(now.Location()), nil
	}
	for _, layout := range dateLayouts {
		if t, err := time.ParseInLocation(layout, s, now.Location()); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot read %q as a date (use 2023-03-14, 2023-03-14 18:11 or RFC 3339)", s)
}

// --- output -------------------------------------------------------------

func fmtDay(t time.Time) string { return t.Format("2006-01-02 (Mon)") }

func fmtDateTime(t time.Time) string { return t.Format("2006-01-02 (Mon) 15:04 MST") }

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func plural(n int, unit string) string {
	if abs(n) == 1 {
		return fmt.Sprintf("%d %s", n, unit)
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

func describeOffset(years, months, weeks, days int) string {
	var parts []string
	for _, p := range []struct {
		n    int
		unit string
	}{{years, "year"}, {months, "month"}, {weeks, "week"}, {days, "day"}} {
		if p.n != 0 {
			parts = append(parts, plural(p.n, p.unit))
		}
	}
	sign := "+"
	// Mixed signs read as separate terms; a single negative term reads as "−".
	if len(parts) == 1 && strings.HasPrefix(parts[0], "-") {
		sign, parts[0] = "−", strings.TrimPrefix(parts[0], "-")
	}
	return sign + " " + strings.Join(parts, " ")
}

// addCalendar moves t by whole years and months, keeping the day of the month
// and clamping it to the last day of a shorter month: January 31 plus one
// month is February 28 (29), where time.AddDate would roll over into March.
func addCalendar(t time.Time, years, months int) time.Time {
	if years == 0 && months == 0 {
		return t
	}
	y, m, d := t.Date()
	total := (y+years)*12 + int(m) - 1 + months
	ny, nm := total/12, time.Month(total%12+1)
	if total < 0 && total%12 != 0 {
		ny, nm = total/12-1, time.Month(total%12+13)
	}
	if last := time.Date(ny, nm+1, 0, 0, 0, 0, 0, t.Location()).Day(); d > last {
		d = last
	}
	return time.Date(ny, nm, d, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location())
}

// calendarDate is t's date at noon UTC, so day counts ignore the time of day
// and daylight-saving shifts.
func calendarDate(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 12, 0, 0, 0, time.UTC)
}

// dateDiffReport states the gap in every unit a question might use, naming
// which count is which so the model need not guess whether an end point is
// included.
func dateDiffReport(from, to time.Time) string {
	days := int(calendarDate(to).Sub(calendarDate(from)).Hours() / 24)
	sign := ""
	if days < 0 {
		sign = "negative: `to` is before `from`; "
		from, to = to, from
		days = -days
	}
	weeks, restDays := days/7, days%7

	// Calendar months and the days left over, counted forward from `from`.
	months := (to.Year()-from.Year())*12 + int(to.Month()) - int(from.Month())
	if from.AddDate(0, months, 0).After(to) {
		months--
	}
	restM := int(calendarDate(to).Sub(calendarDate(from.AddDate(0, months, 0))).Hours() / 24)

	el := to.Sub(from)
	elapsedH := int(el.Hours())
	var b strings.Builder
	fmt.Fprintf(&b, "%sfrom %s to %s\n", sign, fmtDateTimeShort(from), fmtDateTimeShort(to))
	fmt.Fprintf(&b, "calendar days: %d (%d counting both end dates)\n", days, days+1)
	fmt.Fprintf(&b, "weeks: %s", plural(weeks, "week"))
	if restDays > 0 {
		fmt.Fprintf(&b, " and %s", plural(restDays, "day"))
	}
	fmt.Fprintf(&b, "\ncalendar months: %s", plural(months, "month"))
	if restM > 0 {
		fmt.Fprintf(&b, " and %s", plural(restM, "day"))
	}
	fmt.Fprintf(&b, "\nelapsed: %dd %dh %dm", elapsedH/24, elapsedH%24, int(el.Minutes())%60)
	return b.String()
}

func fmtDateTimeShort(t time.Time) string {
	if t.Hour() == 0 && t.Minute() == 0 && t.Second() == 0 {
		return t.Format("2006-01-02 (Mon)")
	}
	return t.Format("2006-01-02 (Mon) 15:04")
}

// --- relative expressions -------------------------------------------------

var (
	agoRe = regexp.MustCompile(`^(\d{1,4}|a|an|one|two|three|four|five|six|seven|eight|nine|ten|twelve)\s+(day|week|month|year)s?\s+ago$`)
	inRe  = regexp.MustCompile(`^in\s+(\d{1,4}|a|an|one|two|three|four|five|six|seven|eight|nine|ten|twelve)\s+(day|week|month|year)s?$`)
	dirRe = regexp.MustCompile(`^(last|previous|past|next|this|coming)\s+([a-z]+)$`)
)

var wordNumbers = map[string]int{"a": 1, "an": 1, "one": 1, "two": 2, "three": 3, "four": 4, "five": 5,
	"six": 6, "seven": 7, "eight": 8, "nine": 9, "ten": 10, "twelve": 12}

var weekdayNames = map[string]time.Weekday{
	"sunday": time.Sunday, "monday": time.Monday, "tuesday": time.Tuesday, "wednesday": time.Wednesday,
	"thursday": time.Thursday, "friday": time.Friday, "saturday": time.Saturday,
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday,
}

func relCount(s string) int {
	if n, ok := wordNumbers[s]; ok {
		return n
	}
	n, _ := strconv.Atoi(s)
	return n
}

// resolveRelativeDate turns a relative expression into a date counted from
// asOf. "last <weekday>" is the most recent such weekday strictly before
// asOf's date and "next <weekday>" the first one strictly after, and the
// reply says so; units larger than a week are approximate by nature and are
// reported as such.
func resolveRelativeDate(expr string, asOf time.Time) (string, error) {
	e := strings.ToLower(strings.TrimSpace(expr))
	e = strings.TrimSuffix(e, ".")
	prefix := fmt.Sprintf("%q counted from %s:\n", expr, fmtDay(asOf))
	switch e {
	case "today", "now":
		return prefix + "= " + fmtDay(asOf), nil
	case "yesterday":
		return prefix + "= " + fmtDay(asOf.AddDate(0, 0, -1)), nil
	case "tomorrow":
		return prefix + "= " + fmtDay(asOf.AddDate(0, 0, 1)), nil
	}
	if m := agoRe.FindStringSubmatch(e); m != nil {
		return prefix + shiftReport(asOf, -relCount(m[1]), m[2]), nil
	}
	if m := inRe.FindStringSubmatch(e); m != nil {
		return prefix + shiftReport(asOf, relCount(m[1]), m[2]), nil
	}
	if m := dirRe.FindStringSubmatch(e); m != nil {
		wd, ok := weekdayNames[m[2]]
		if !ok {
			return "", fmt.Errorf("cannot resolve %q: %q is not a weekday", expr, m[2])
		}
		cur := int(asOf.Weekday())
		switch m[1] {
		case "last", "previous", "past":
			back := (cur - int(wd) + 7) % 7
			if back == 0 {
				back = 7
			}
			d := asOf.AddDate(0, 0, -back)
			return prefix + fmt.Sprintf("= %s (the most recent %s before %s, %s)", fmtDay(d), wd, asOf.Format("2006-01-02"), plural(back, "day")+" earlier"), nil
		default:
			fwd := (int(wd) - cur + 7) % 7
			if fwd == 0 {
				fwd = 7
			}
			d := asOf.AddDate(0, 0, fwd)
			return prefix + fmt.Sprintf("= %s (the first %s after %s, %s later)", fmtDay(d), wd, asOf.Format("2006-01-02"), plural(fwd, "day")), nil
		}
	}
	return "", fmt.Errorf("cannot resolve %q (try: yesterday, tomorrow, last|next <weekday>, N days|weeks|months|years ago, in N days|weeks|months)", expr)
}

func shiftReport(asOf time.Time, n int, unit string) string {
	var d time.Time
	switch unit {
	case "day":
		d = asOf.AddDate(0, 0, n)
	case "week":
		d = asOf.AddDate(0, 0, 7*n)
	case "month":
		d = addCalendar(asOf, 0, n)
	default:
		d = addCalendar(asOf, n, 0)
	}
	out := "= " + fmtDay(d)
	if unit == "week" || unit == "month" || unit == "year" {
		out += "\n(a phrase like this is approximate: people mean the days around it)"
	}
	return out
}
