package plugins

import (
	"context"
	"strings"
	"testing"
)

func runDate(t *testing.T, now, args string) string {
	t.Helper()
	t.Setenv("CHATCLI_NOW", now)
	out, err := NewBuiltinDatePlugin().Execute(context.Background(), []string{args})
	if err != nil {
		t.Fatalf("%s: %v", args, err)
	}
	return out
}

func wantAll(t *testing.T, out string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(out, s) {
			t.Errorf("missing %q in:\n%s", s, out)
		}
	}
}

func TestDate_DiffCountsCalendarDays(t *testing.T) {
	// 55 elapsed hours, but three calendar dates apart.
	out := runDate(t, "2023-04-18T12:00:00", `{"cmd":"diff","args":{"from":"2023-04-15 18:11","to":"2023-04-18 01:48"}}`)
	wantAll(t, out, "calendar days: 3 (4 counting both end dates)", "elapsed: 2d 7h 37m")

	out = runDate(t, "2023-04-18T12:00:00", `{"cmd":"diff","args":{"from":"2023-03-14","to":"2023-04-17"}}`)
	wantAll(t, out, "calendar days: 34", "weeks: 4 weeks and 6 days", "calendar months: 1 month and 3 days")

	out = runDate(t, "2023-04-18T12:00:00", `{"cmd":"diff","args":{"from":"2023-04-17","to":"2023-03-14"}}`)
	wantAll(t, out, "negative", "calendar days: 34")
}

func TestDate_DiffReadsTheFormSessionResultsPrint(t *testing.T) {
	out := runDate(t, "2023-04-18T12:00:00", `{"cmd":"diff","args":{"from":"2023-04-15 (Sat) 18:11","to":"today"}}`)
	wantAll(t, out, "calendar days: 3")
}

func TestDate_ResolveRelativeExpressions(t *testing.T) {
	// 2023-04-22 is a Saturday: "last Saturday" is the one a week earlier.
	out := runDate(t, "2023-04-22T09:00:00", `{"cmd":"resolve","args":{"expr":"last saturday"}}`)
	wantAll(t, out, "= 2023-04-15 (Sat)", "7 days earlier")
	out = runDate(t, "2023-04-22T09:00:00", `{"cmd":"resolve","args":{"expr":"next friday"}}`)
	wantAll(t, out, "= 2023-04-28 (Fri)")
	out = runDate(t, "2023-05-30T09:00:00", `{"cmd":"resolve","args":{"expr":"3 weeks ago"}}`)
	wantAll(t, out, "= 2023-05-09 (Tue)", "approximate")
	out = runDate(t, "2023-05-30T09:00:00", `{"cmd":"resolve","args":{"expr":"yesterday","as_of":"2023-01-01"}}`)
	wantAll(t, out, "= 2022-12-31 (Sat)")
}

func TestDate_AddClampsMonthEnds(t *testing.T) {
	out := runDate(t, "2023-05-30T09:00:00", `{"cmd":"add","args":{"date":"2023-01-31","months":1}}`)
	wantAll(t, out, "= 2023-02-28 (Tue)")
	out = runDate(t, "2023-05-30T09:00:00", `{"cmd":"add","args":{"date":"2023-04-05","weeks":-3}}`)
	wantAll(t, out, "= 2023-03-15 (Wed)", "− 3 weeks")
	out = runDate(t, "2023-05-30T09:00:00", `{"cmd":"add","args":{"date":"2024-02-29","years":1}}`)
	wantAll(t, out, "= 2025-02-28 (Fri)")
	out = runDate(t, "2023-05-30T09:00:00", `{"cmd":"add","args":{"date":"2023-01-15","months":-2}}`)
	wantAll(t, out, "= 2022-11-15 (Tue)")
}

func TestDate_WeekdayNowAndErrors(t *testing.T) {
	wantAll(t, runDate(t, "2023-04-22T09:00:00", `{"cmd":"weekday","args":{"date":"2023-04-22"}}`), "2023-04-22 (Sat)")
	wantAll(t, runDate(t, "2023-04-22T09:00:00", `{"cmd":"now"}`), "now: 2023-04-22 (Sat) 09:00")
	t.Setenv("CHATCLI_NOW", "2023-04-22T09:00:00")
	p := NewBuiltinDatePlugin()
	for _, bad := range []string{
		`{"cmd":"diff","args":{"from":"not a date"}}`,
		`{"cmd":"add","args":{"date":"2023-01-01"}}`,
		`{"cmd":"add","args":{"date":"2023-01-01","days":99999999}}`,
		`{"cmd":"resolve","args":{"expr":"whenever"}}`,
		`{"cmd":"resolve","args":{"expr":"last funday"}}`,
		`{"cmd":"explode"}`,
	} {
		if _, err := p.Execute(context.Background(), []string{bad}); err == nil {
			t.Errorf("%s must fail", bad)
		}
	}
}

func TestDate_FlatArgsInferTheVerb(t *testing.T) {
	wantAll(t, runDate(t, "2023-04-22T09:00:00", `{"from":"2023-04-01","to":"2023-04-22"}`), "calendar days: 21")
	wantAll(t, runDate(t, "2023-04-22T09:00:00", `{"expr":"tomorrow"}`), "= 2023-04-23 (Sun)")
}
