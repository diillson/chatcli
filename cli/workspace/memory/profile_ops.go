package memory

import (
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// This file owns the semantics of profile LIST fields (certifications, skills,
// goals, interests, directives) and the self-healing normalization applied on
// load. Historically these fields could only append, which turned goals into
// an ever-growing changelog: progress restatements duplicated instead of
// superseding, completed goals never left, and naive comma-splitting sheared
// values that legitimately contain commas ("Quiz X (Anthropic, 60 questões)")
// into orphan fragments. Every operation here exists to close one of those
// holes deterministically.

// listOp is what to do with incoming items for a list field.
type listOp int

const (
	opUpsert  listOp = iota // append new items; same-stem items supersede in place
	opReplace               // replace the whole list (empty value clears it)
	opRemove                // remove entries matching any incoming item (substring or stem)
)

// listFieldAliases folds every accepted key alias into its canonical list
// field name. Scalar keys (name, role, …) are intentionally absent.
var listFieldAliases = map[string]string{
	"certification": "certifications", "certifications": "certifications",
	"cert": "certifications", "certs": "certifications",
	"skill": "skills", "skills": "skills",
	"goal": "goals", "goals": "goals", "objective": "goals", "objectives": "goals",
	"interest": "interests", "interests": "interests", "hobby": "interests", "hobbies": "interests",
	"directive": "directives", "directives": "directives",
}

// opAffixes maps recognized key suffixes/prefixes to operations. "_done" reads
// naturally for goals ("goals_done=tirar CKA") but is accepted on any list.
var opAffixes = []struct {
	affix string
	op    listOp
}{
	{"replace", opReplace}, {"set", opReplace},
	{"remove", opRemove}, {"done", opRemove}, {"delete", opRemove}, {"del", opRemove},
	{"add", opUpsert},
}

// resolveListKey maps an update key — optionally carrying an operation affix
// ("goals_replace", "remove_goals") — to its canonical list field and op.
func resolveListKey(key string) (field string, op listOp, ok bool) {
	k := strings.ToLower(strings.TrimSpace(key))
	op = opUpsert
	for _, a := range opAffixes {
		if strings.HasSuffix(k, "_"+a.affix) {
			k, op = strings.TrimSuffix(k, "_"+a.affix), a.op
			break
		}
		if strings.HasPrefix(k, a.affix+"_") {
			k, op = strings.TrimPrefix(k, a.affix+"_"), a.op
			break
		}
	}
	field, ok = listFieldAliases[k]
	return field, op, ok
}

// splitListItems splits a comma/semicolon-separated value into items WITHOUT
// splitting inside parentheses/brackets, so "Quiz X (Anthropic, 60 questões)"
// stays whole. Leading bullet markers are stripped from each item.
func splitListItems(value string) []string {
	return splitOnSeparators(value, func(r rune) bool { return r == ',' || r == ';' || r == '\n' })
}

// splitSentenceItems is the splitter for sentence-like values (milestones):
// only semicolons and newlines separate, since prose legitimately has commas.
func splitSentenceItems(value string) []string {
	return splitOnSeparators(value, func(r rune) bool { return r == ';' || r == '\n' })
}

func splitOnSeparators(value string, isSep func(rune) bool) []string {
	var items []string
	var b strings.Builder
	depth := 0
	flush := func() {
		if it := cleanListItem(b.String()); it != "" {
			items = append(items, it)
		}
		b.Reset()
	}
	for _, r := range value {
		switch r {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			if depth > 0 {
				depth--
			}
		}
		if depth == 0 && isSep(r) {
			flush()
			continue
		}
		b.WriteRune(r)
	}
	flush()
	return items
}

func cleanListItem(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimLeft(s, "-•*\t ")
	return strings.TrimSpace(s)
}

// stemOf reduces a list entry to its canonical identity: lowercase, markdown
// emphasis stripped, status suffix (text after an em/en dash separator) cut,
// parenthetical qualifiers removed, whitespace collapsed. Entries sharing a
// stem describe the same underlying item at different moments — "goal — EM
// PROGRESSO (8/60)" and "goal — EM PROGRESSO (17/60)" — so the newest one
// supersedes instead of accumulating.
func stemOf(item string) string {
	s := strings.ToLower(item)
	s = strings.NewReplacer("*", "", "`", "", "~", "").Replace(s)
	s = cutStatusSuffix(s)
	s = stripParentheticals(s)
	s = foldDiacritics(s)
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimRight(s, " .,;:!?")
}

// cutStatusSuffix removes a trailing dash-separated STATUS annotation ("goal
// — EM PROGRESSO (17/60)") but leaves dash-separated identity intact
// ("Anthropic — MCP quiz" names a different credential than "Anthropic —
// Claude 101" — cutting blindly would collapse them into one). A tail only
// counts as status when it carries a progress/completion marker. The last
// qualifying dash wins, so "a — b — em progresso" keeps "a — b".
func cutStatusSuffix(s string) string {
	for _, sep := range []string{" — ", " – ", " -- "} {
		if i := strings.LastIndex(s, sep); i >= 0 && isStatusTail(s[i+len(sep):]) {
			s = s[:i]
		}
	}
	return s
}

// statusTailMarkers flags a dash tail as a status annotation rather than part
// of the item's identity.
var statusTailMarkers = []string{
	"progresso", "progress", "andamento", "pendente", "pending",
	"concluí", "concluid", "completed", "done", "finalizad",
	"revisão", "revisao", "review", "respondida", "answered", "✅",
}

func isStatusTail(tail string) bool {
	return containsAny(strings.ToLower(tail), statusTailMarkers...)
}

// foldDiacritics strips combining marks so pt-BR accent variants of the same
// entry ("violão"/"violao") share a stem. Falls back to the input on error.
func foldDiacritics(s string) string {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	out, _, err := transform.String(t, s)
	if err != nil {
		return s
	}
	return out
}

// stripParentheticals removes ( … ) segments, tolerating nesting and unmatched
// closers (fragments from historic bad splits).
func stripParentheticals(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

// isFragmentEntry detects orphan fragments produced by the old comma-splitting
// of parenthesized values: the right half's running paren balance goes
// negative ("60 questões) — EM PROGRESSO (17/60)") and the left half ends
// with an unclosed paren ("completar Quiz X (Anthropic").
func isFragmentEntry(s string) bool {
	depth := 0
	for _, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return true
			}
		}
	}
	return depth > 0
}

// completedStatusWords marks a goal as finished when any appears as a
// standalone word (after stripping emphasis/punctuation). Word-level matching
// keeps "Certificado de Conclusão" (an active goal) safe.
var completedStatusWords = map[string]struct{}{
	"concluído": {}, "concluida": {}, "concluída": {}, "concluido": {},
	"completed": {}, "done": {}, "finalizado": {}, "finalizada": {}, "✅": {},
}

// isCompletedStatus reports whether a goal entry carries a completed marker.
func isCompletedStatus(s string) bool {
	for _, f := range strings.Fields(strings.ToLower(s)) {
		f = strings.Trim(f, "*_`()[]{}.,;:!—–-")
		if _, ok := completedStatusWords[f]; ok {
			return true
		}
	}
	return false
}

// isInstructionEcho detects the user's memory-management REQUEST recorded as
// if it were a goal ("remove Anthropic certifications from active goals").
// Instructions are to be applied, never stored.
func isInstructionEcho(s string) bool {
	l := strings.ToLower(strings.TrimSpace(s))
	verb := false
	for _, v := range []string{"remove ", "remover ", "remova ", "delete ", "apagar ", "apague ", "limpar ", "limpe ", "update ", "atualizar ", "atualize "} {
		if strings.HasPrefix(l, v) {
			verb = true
			break
		}
	}
	if !verb {
		return false
	}
	return containsAny(l, "goal", "objetivo", "profile", "perfil", "certifica", "memória", "memoria", "memory")
}

// upsertItems merges incoming items into list: an item whose stem matches an
// existing entry replaces it in place (newest status wins); new stems append.
// Fragments are refused so historic pollution cannot re-enter.
func upsertItems(list *[]string, items []string) bool {
	changed := false
	for _, item := range items {
		if item == "" || isFragmentEntry(item) {
			continue
		}
		st := stemOf(item)
		found := false
		for i, existing := range *list {
			if stemOf(existing) == st {
				found = true
				if existing != item {
					(*list)[i] = item
					changed = true
				}
				break
			}
		}
		if !found {
			*list = append(*list, item)
			changed = true
		}
	}
	return changed
}

// removeItems drops entries matching ANY incoming item, by case-insensitive
// substring or by stem equality. Returns whether the list shrank.
func removeItems(list *[]string, matchers []string) bool {
	if len(matchers) == 0 {
		return false
	}
	kept := make([]string, 0, len(*list))
	for _, existing := range *list {
		if entryMatchesAny(existing, matchers) {
			continue
		}
		kept = append(kept, existing)
	}
	if len(kept) == len(*list) {
		return false
	}
	*list = kept
	return true
}

func entryMatchesAny(entry string, matchers []string) bool {
	le := strings.ToLower(entry)
	se := stemOf(entry)
	for _, m := range matchers {
		if m == "" {
			continue
		}
		if strings.Contains(le, strings.ToLower(m)) || stemOf(m) == se {
			return true
		}
	}
	return false
}

// replaceItems overwrites the list with the (deduped, fragment-filtered)
// incoming items. An empty incoming set clears the list.
func replaceItems(list *[]string, items []string) bool {
	next := make([]string, 0, len(items))
	for _, item := range items {
		if item == "" || isFragmentEntry(item) {
			continue
		}
		upsertItems(&next, []string{item})
	}
	if equalStringSlices(*list, next) {
		return false
	}
	*list = next
	return true
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// cleanListInPlace self-heals one list field: fragments are dropped, an
// optional extra predicate prunes entries (completed goals, instruction
// echoes), and same-stem duplicates collapse to the LAST occurrence (newest
// information wins) while keeping first-seen order.
func cleanListInPlace(list *[]string, drop func(string) bool) bool {
	byStem := make(map[string]int)
	next := make([]string, 0, len(*list))
	for _, entry := range *list {
		if entry == "" || isFragmentEntry(entry) || (drop != nil && drop(entry)) {
			continue
		}
		st := stemOf(entry)
		if i, ok := byStem[st]; ok {
			next[i] = entry // later variant supersedes, position preserved
			continue
		}
		byStem[st] = len(next)
		next = append(next, entry)
	}
	if equalStringSlices(*list, next) {
		return false
	}
	*list = next
	return true
}

// normalizeLoadedProfile self-heals list fields written by earlier versions
// (append-only updates + naive comma splitting). It runs on every load and is
// idempotent; the caller persists when it reports a change, so a polluted
// legacy profile is fixed once and stays fixed.
func normalizeLoadedProfile(p *UserProfile) bool {
	changed := false
	for _, f := range []*[]string{&p.Certifications, &p.Skills, &p.Interests, &p.Directives} {
		if cleanListInPlace(f, nil) {
			changed = true
		}
	}
	if cleanListInPlace(&p.Goals, func(s string) bool {
		return isCompletedStatus(s) || isInstructionEcho(s)
	}) {
		changed = true
	}
	return changed
}
