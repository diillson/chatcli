package scheduler

import (
	"testing"
	"time"
)

func TestParseCron_FiveFieldAndAliases(t *testing.T) {
	cases := []struct {
		expr      string
		shouldErr bool
	}{
		{"0 2 * * *", false},
		{"@daily", false},
		{"@hourly", false},
		{"*/5 * * * *", false},
		{"0 0,12 * * mon-fri", false},
		{"0 0 30 feb *", false}, // valid shape, vacuous match — no error at parse
		{"bogus", true},
		{"1 2 3 4", true},           // 4 fields
		{"1 2 3 4 5 6", true},       // 6 fields
		{"99 * * * *", true},        // out of range
	}
	for _, c := range cases {
		_, err := parseCron(c.expr)
		if (err != nil) != c.shouldErr {
			t.Errorf("parseCron(%q): shouldErr=%v got %v", c.expr, c.shouldErr, err)
		}
	}
}

func TestCronNext_Daily(t *testing.T) {
	cs, err := parseCron("0 2 * * *") // 02:00 every day
	if err != nil {
		t.Fatal(err)
	}
	after := time.Date(2026, 4, 23, 15, 30, 0, 0, time.UTC)
	next := cs.Next(after)
	// Should be 2026-04-24 02:00 UTC.
	want := time.Date(2026, 4, 24, 2, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("cron 0 2 * * * after %s → %s, want %s", after, next, want)
	}
}

func TestCronNext_DOM_DOW_OR(t *testing.T) {
	// When both dom and dow are restricted, cron ORs them.
	cs, err := parseCron("0 0 1 * sun")
	if err != nil {
		t.Fatal(err)
	}
	// 2026-04-01 was a Wednesday — dom=1 matches, so this should fire.
	after := time.Date(2026, 3, 31, 23, 30, 0, 0, time.UTC)
	next := cs.Next(after)
	want := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("dom/dow OR: after %s → %s, want %s", after, next, want)
	}
}

func TestSchedule_Next_Absolute(t *testing.T) {
	t0 := time.Date(2026, 6, 1, 12, 0, 0, 0, time.Local)
	s := Schedule{Kind: ScheduleAbsolute, ExactTime: t0}
	after := t0.Add(-10 * time.Minute)
	if got := s.Next(after, after); !got.Equal(t0) {
		t.Errorf("future absolute: got %v want %v", got, t0)
	}
	if got := s.Next(t0.Add(1*time.Second), t0.Add(1*time.Second)); !got.IsZero() {
		t.Errorf("past absolute: expected zero, got %v", got)
	}
}

func TestSchedule_Next_Interval(t *testing.T) {
	s := Schedule{Kind: ScheduleInterval, Interval: 5 * time.Minute}
	createdAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// after==createdAt should produce createdAt+5m.
	if got := s.Next(createdAt, createdAt); !got.Equal(createdAt.Add(5 * time.Minute)) {
		t.Errorf("interval initial: got %v", got)
	}
	// after=6 minutes → next is 10m.
	after := createdAt.Add(6 * time.Minute)
	if got := s.Next(after, createdAt); !got.Equal(createdAt.Add(10 * time.Minute)) {
		t.Errorf("interval mid: got %v", got)
	}
}

func TestSchedule_Validate(t *testing.T) {
	cases := []struct {
		name string
		s    Schedule
		bad  bool
	}{
		{"abs ok", Schedule{Kind: ScheduleAbsolute, ExactTime: time.Now().Add(time.Hour)}, false},
		{"abs zero", Schedule{Kind: ScheduleAbsolute}, true},
		{"rel ok", Schedule{Kind: ScheduleRelative, Relative: time.Minute}, false},
		{"rel zero", Schedule{Kind: ScheduleRelative}, true},
		{"cron empty", Schedule{Kind: ScheduleCron}, true},
		{"cron bad", Schedule{Kind: ScheduleCron, Cron: "bogus"}, true},
		{"cron ok", Schedule{Kind: ScheduleCron, Cron: "@hourly"}, false},
		{"bad tz", Schedule{Kind: ScheduleCron, Cron: "@hourly", Timezone: "Not/A/Zone"}, true},
	}
	for _, c := range cases {
		err := c.s.Validate()
		if (err != nil) != c.bad {
			t.Errorf("%s: bad=%v err=%v", c.name, c.bad, err)
		}
	}
}

func TestParseScheduleDSL(t *testing.T) {
	cases := []struct {
		in      string
		wantKind ScheduleKind
		wantErr bool
	}{
		{"in 5m", ScheduleRelative, false},
		{"+30s", ScheduleRelative, false},
		{"after 1h", ScheduleRelative, false},
		{"cron:@daily", ScheduleCron, false},
		{"@hourly", ScheduleCron, false},
		{"0 2 * * *", ScheduleCron, false},
		{"every 10s", ScheduleInterval, false},
		{"when-ready", ScheduleOnCondition, false},
		{"manual", ScheduleManual, false},
		{"garbage string", ScheduleKind(""), true},
	}
	for _, c := range cases {
		got, err := ParseScheduleDSL(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("%q: wantErr=%v got err=%v", c.in, c.wantErr, err)
		}
		if err == nil && got.Kind != c.wantKind {
			t.Errorf("%q: kind=%q want %q", c.in, got.Kind, c.wantKind)
		}
	}
}
