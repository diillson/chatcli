package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"
)

// RollupStore consolidates daily notes into weekly digests and weekly digests
// into monthly ones — the long-range narrative daily notes cannot provide
// (they expire after ~30 days). Weeklies keep the recent trajectory sharp;
// monthlies preserve it indefinitely at low cost.
//
// Layout under the memory dir:
//
//	weekly/2026-W27.md    (ISO week, written once the week has ended)
//	monthly/2026-06.md    (written once the month has ended)
type RollupStore struct {
	memoryDir string
	logger    *zap.Logger
}

// NewRollupStore creates a rollup store rooted at the memory directory.
func NewRollupStore(memoryDir string, logger *zap.Logger) *RollupStore {
	return &RollupStore{memoryDir: memoryDir, logger: logger}
}

const (
	// weeklyRetention bounds how many weekly digests are kept; monthlies
	// absorb the history beyond it and are kept forever.
	weeklyRetention = 26
	// rollupInputCap bounds how much source text feeds one digest, so the
	// summarization prompt (or deterministic fallback) stays cheap.
	rollupInputCap = 12 * 1024
	// rollupSummarizeTimeout bounds one LLM summarization call.
	rollupSummarizeTimeout = 60 * time.Second
)

// SummarizeFunc produces a digest for the given prompt; nil or a returned
// error falls back to deterministic condensation, so rollups never depend on
// LLM availability.
type SummarizeFunc func(ctx context.Context, prompt string) (string, error)

// Run performs all pending rollups: every fully-elapsed ISO week with daily
// notes and no weekly file, then every fully-elapsed month with source
// material and no monthly file, then weekly retention. Returns how many
// digest files were written. Idempotent — safe to call on any cadence.
func (rs *RollupStore) Run(ctx context.Context, summarize SummarizeFunc) (int, error) {
	written := 0
	dailies, err := rs.listDailyNotes()
	if err != nil {
		return 0, err
	}

	now := time.Now()
	today := now.Format("2006-01-02")
	for _, wk := range groupByISOWeek(dailies) {
		// Calendar-date comparison: a week is elapsed only when its Sunday is
		// strictly before today, regardless of time zone of the parsed dates.
		if wk.end.Format("2006-01-02") >= today {
			continue // week still running
		}
		path := filepath.Join(rs.memoryDir, "weekly", wk.label+".md")
		if fileExists(path) {
			continue
		}
		digest := rs.digest(ctx, summarize, weeklyPromptHeader(wk), wk.content())
		header := fmt.Sprintf("# Week %s (%s – %s)\n\n", wk.label, wk.start.Format("2006-01-02"), wk.end.Format("2006-01-02"))
		if err := rs.writeDigest(path, header+digest); err != nil {
			rs.logger.Warn("weekly rollup write failed", zap.String("week", wk.label), zap.Error(err))
			continue
		}
		written++
	}

	n, err := rs.rollMonths(ctx, summarize, now)
	written += n
	if err != nil {
		return written, err
	}

	rs.enforceWeeklyRetention()
	return written, nil
}

// rollMonths consolidates every fully-elapsed month that has source material
// (weeklies preferred — they survive daily-note cleanup — else dailies).
func (rs *RollupStore) rollMonths(ctx context.Context, summarize SummarizeFunc, now time.Time) (int, error) {
	months, err := rs.listSourceMonths()
	if err != nil {
		return 0, err
	}
	currentMonth := now.Format("2006-01")
	written := 0
	for _, month := range months {
		if month >= currentMonth {
			continue // month still running
		}
		path := filepath.Join(rs.memoryDir, "monthly", month+".md")
		if fileExists(path) {
			continue
		}
		source := rs.monthSource(month)
		if strings.TrimSpace(source) == "" {
			continue
		}
		prompt := "Condense these work notes from " + month + " into a monthly digest: at most 10 bullets covering achievements, decisions, blockers and themes. Write in the same language as the notes. Output only the bullets.\n\n"
		digest := rs.digest(ctx, summarize, prompt, source)
		if err := rs.writeDigest(path, "# Month "+month+"\n\n"+digest); err != nil {
			rs.logger.Warn("monthly rollup write failed", zap.String("month", month), zap.Error(err))
			continue
		}
		written++
	}
	return written, nil
}

// FormatTrajectory renders the recent long-range narrative — latest monthly
// digest plus the most recent weeklies — bounded to maxChars for prompt use.
func (rs *RollupStore) FormatTrajectory(maxChars int) string {
	if maxChars <= 0 {
		maxChars = 600
	}
	var parts []string

	if latest := rs.latestFile("monthly"); latest != "" {
		if txt := readHead(latest, maxChars/2); txt != "" {
			parts = append(parts, txt)
		}
	}
	weeklies := rs.sortedFiles("weekly")
	for i := 0; i < 2 && i < len(weeklies); i++ {
		if txt := readHead(weeklies[len(weeklies)-1-i], maxChars/3); txt != "" {
			parts = append(parts, txt)
		}
	}
	out := strings.Join(parts, "\n\n")
	if len(out) > maxChars {
		out = out[:maxChars]
	}
	return strings.TrimSpace(out)
}

// --- digest production -------------------------------------------------------

// digest asks the LLM for a condensation and falls back to deterministic
// bullet extraction when no summarizer is available or it fails.
func (rs *RollupStore) digest(ctx context.Context, summarize SummarizeFunc, prompt, source string) string {
	if len(source) > rollupInputCap {
		source = source[:rollupInputCap]
	}
	if summarize != nil {
		sctx, cancel := context.WithTimeout(ctx, rollupSummarizeTimeout)
		out, err := summarize(sctx, prompt+source)
		cancel()
		if err == nil && strings.TrimSpace(out) != "" {
			return strings.TrimSpace(out)
		}
		if err != nil {
			rs.logger.Debug("rollup summarize failed; using deterministic fallback", zap.Error(err))
		}
	}
	return condenseBullets(source, 12)
}

func weeklyPromptHeader(wk isoWeek) string {
	return "Condense these daily work notes from week " + wk.label +
		" into a weekly digest: at most 8 bullets covering achievements, decisions, blockers and recurring themes. " +
		"Write in the same language as the notes. Output only the bullets.\n\n"
}

// condenseBullets keeps the first maxBullets bullet lines — a lossy but
// deterministic digest that never blocks on an LLM.
func condenseBullets(source string, maxBullets int) string {
	var bullets []string
	for _, line := range strings.Split(source, "\n") {
		l := strings.TrimSpace(line)
		if strings.HasPrefix(l, "- ") || strings.HasPrefix(l, "* ") || strings.HasPrefix(l, "• ") {
			bullets = append(bullets, "- "+strings.TrimLeft(l, "-*• "))
			if len(bullets) >= maxBullets {
				break
			}
		}
	}
	if len(bullets) == 0 {
		if len(source) > 400 {
			source = source[:400]
		}
		return strings.TrimSpace(source)
	}
	return strings.Join(bullets, "\n")
}

// --- filesystem helpers --------------------------------------------------------

type dailyFile struct {
	date time.Time
	path string
}

// listDailyNotes scans the YYYYMM/YYYYMMDD.md layout the daily store writes.
func (rs *RollupStore) listDailyNotes() ([]dailyFile, error) {
	entries, err := os.ReadDir(rs.memoryDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []dailyFile
	for _, e := range entries {
		if !e.IsDir() || len(e.Name()) != 6 {
			continue
		}
		if _, err := time.Parse("200601", e.Name()); err != nil {
			continue
		}
		monthDir := filepath.Join(rs.memoryDir, e.Name())
		files, err := os.ReadDir(monthDir)
		if err != nil {
			continue
		}
		for _, f := range files {
			name := strings.TrimSuffix(f.Name(), ".md")
			// Local location keeps week boundaries aligned with the user's
			// calendar; plain Parse would yield UTC and shift day edges.
			d, err := time.ParseInLocation("20060102", name, time.Local)
			if err != nil {
				continue
			}
			out = append(out, dailyFile{date: d, path: filepath.Join(monthDir, f.Name())})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].date.Before(out[j].date) })
	return out, nil
}

type isoWeek struct {
	label      string // 2026-W27
	start, end time.Time
	files      []dailyFile
}

func (w isoWeek) content() string {
	var sb strings.Builder
	for _, f := range w.files {
		data, err := os.ReadFile(f.path) //#nosec G304 -- paths enumerated from the memory dir itself
		if err != nil {
			continue
		}
		sb.WriteString("### " + f.date.Format("2006-01-02") + "\n")
		sb.Write(data)
		sb.WriteString("\n")
	}
	return sb.String()
}

func groupByISOWeek(files []dailyFile) []isoWeek {
	byLabel := make(map[string]*isoWeek)
	var order []string
	for _, f := range files {
		year, week := f.date.ISOWeek()
		label := fmt.Sprintf("%04d-W%02d", year, week)
		wk, ok := byLabel[label]
		if !ok {
			start := startOfISOWeek(f.date)
			wk = &isoWeek{label: label, start: start, end: start.AddDate(0, 0, 6)}
			byLabel[label] = wk
			order = append(order, label)
		}
		wk.files = append(wk.files, f)
	}
	out := make([]isoWeek, 0, len(order))
	for _, l := range order {
		out = append(out, *byLabel[l])
	}
	return out
}

func startOfISOWeek(d time.Time) time.Time {
	d = startOfDay(d)
	for d.Weekday() != time.Monday {
		d = d.AddDate(0, 0, -1)
	}
	return d
}

func startOfDay(d time.Time) time.Time {
	return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, d.Location())
}

// listSourceMonths returns every YYYY-MM that has dailies or weeklies.
func (rs *RollupStore) listSourceMonths() ([]string, error) {
	months := make(map[string]struct{})
	dailies, err := rs.listDailyNotes()
	if err != nil {
		return nil, err
	}
	for _, d := range dailies {
		months[d.date.Format("2006-01")] = struct{}{}
	}
	for _, w := range rs.sortedFiles("weekly") {
		base := strings.TrimSuffix(filepath.Base(w), ".md") // 2026-W27
		if wk, ok := parseWeekLabel(base); ok {
			months[wk.Format("2006-01")] = struct{}{}
		}
	}
	out := make([]string, 0, len(months))
	for m := range months {
		out = append(out, m)
	}
	sort.Strings(out)
	return out, nil
}

// monthSource prefers weekly digests whose week STARTS inside the month
// (they survive daily cleanup); when none exist it falls back to the raw
// daily notes of that month.
func (rs *RollupStore) monthSource(month string) string {
	var sb strings.Builder
	for _, w := range rs.sortedFiles("weekly") {
		base := strings.TrimSuffix(filepath.Base(w), ".md")
		if wk, ok := parseWeekLabel(base); ok && wk.Format("2006-01") == month {
			if data, err := os.ReadFile(w); err == nil { //#nosec G304 -- paths enumerated from the memory dir itself
				sb.Write(data)
				sb.WriteString("\n")
			}
		}
	}
	if sb.Len() > 0 {
		return sb.String()
	}
	dailies, err := rs.listDailyNotes()
	if err != nil {
		return ""
	}
	for _, d := range dailies {
		if d.date.Format("2006-01") != month {
			continue
		}
		if data, err := os.ReadFile(d.path); err == nil { //#nosec G304 -- paths enumerated from the memory dir itself
			sb.WriteString("### " + d.date.Format("2006-01-02") + "\n")
			sb.Write(data)
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// parseWeekLabel converts "2026-W27" to the Monday that starts that week.
func parseWeekLabel(label string) (time.Time, bool) {
	var year, week int
	if _, err := fmt.Sscanf(label, "%04d-W%02d", &year, &week); err != nil {
		return time.Time{}, false
	}
	// Jan 4 is always in ISO week 1.
	jan4 := time.Date(year, 1, 4, 0, 0, 0, 0, time.Local)
	return startOfISOWeek(jan4).AddDate(0, 0, (week-1)*7), true
}

func (rs *RollupStore) writeDigest(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	return atomicWriteFile(path, []byte(content), 0o600)
}

// enforceWeeklyRetention deletes the oldest weeklies beyond the cap; their
// content lives on in the monthly digests.
func (rs *RollupStore) enforceWeeklyRetention() {
	files := rs.sortedFiles("weekly")
	for len(files) > weeklyRetention {
		if err := os.Remove(files[0]); err != nil {
			rs.logger.Warn("weekly retention delete failed", zap.String("file", files[0]), zap.Error(err))
			return
		}
		files = files[1:]
	}
}

// sortedFiles lists the .md files of one rollup subdir in name order (names
// sort chronologically by construction).
func (rs *RollupStore) sortedFiles(sub string) []string {
	entries, err := os.ReadDir(filepath.Join(rs.memoryDir, sub))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			out = append(out, filepath.Join(rs.memoryDir, sub, e.Name()))
		}
	}
	sort.Strings(out)
	return out
}

// latestFile returns the newest file of one rollup subdir, or "".
func (rs *RollupStore) latestFile(sub string) string {
	files := rs.sortedFiles(sub)
	if len(files) == 0 {
		return ""
	}
	return files[len(files)-1]
}

// readHead returns up to maxChars of a file, trimmed.
func readHead(path string, maxChars int) string {
	data, err := os.ReadFile(path) //#nosec G304 -- paths enumerated from the memory dir itself
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(data))
	if len(s) > maxChars {
		s = s[:maxChars]
	}
	return s
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
