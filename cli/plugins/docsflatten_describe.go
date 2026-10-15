/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * images: "describe" — the referenced images become knowledge.
 *
 * Layer 1 (docsflatten_visual.go) keeps what the text says about an image:
 * its alt text, caption and path. The picture itself — the boxes and arrows
 * of an architecture PNG, the labels of a screenshot — needs a vision
 * model. With images: "describe" every raster image the documents
 * reference is described once (content-addressed cache: a re-run, or the
 * same image on thirty pages, costs nothing), the description becomes its
 * own chunk (kind "image") and each referencing chunk's marker points to
 * it. images: "estimate" counts the images and the tokens without calling
 * any model.
 *
 * The model is the session's vision pipeline (DocsImageDescriber, wired by
 * the cli package): the active model when it has vision, else
 * CHATCLI_VISION_PROVIDER/MODEL, else any configured vision model.
 */
package plugins

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"  // register decoders for DecodeConfig
	_ "image/jpeg" // (dimension filter)
	_ "image/png"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	_ "golang.org/x/image/webp" // webp dimensions

	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
)

// DocsImageDescriber describes one image for @docs-flatten. model names
// the provider/model that wrote the description.
type DocsImageDescriber interface {
	DescribeImage(ctx context.Context, prompt string, img models.ImageContent) (description, model string, err error)
}

type docsDescriberHolder struct{ d DocsImageDescriber }

var docsDescriberAtom atomic.Value // docsDescriberHolder

// SetDocsImageDescriber wires the session's vision pipeline; nil detaches.
func SetDocsImageDescriber(d DocsImageDescriber) {
	docsDescriberAtom.Store(docsDescriberHolder{d: d})
}

func currentDocsImageDescriber() DocsImageDescriber {
	v := docsDescriberAtom.Load()
	if v == nil {
		return nil
	}
	h, _ := v.(docsDescriberHolder)
	return h.d
}

// Image handling modes.
const (
	docsImagesMarkers  = "markers" // default: alt/caption/path markers only
	docsImagesDescribe = "describe"
	docsImagesEstimate = "estimate"
)

// docsImagePromptVersion is part of the cache key: changing the prompt
// re-describes instead of reusing descriptions written for an older one.
const docsImagePromptVersion = "docs-image-v1"

// Defaults and filters.
const (
	defaultDocsMaxImages = 50
	minDescribeEdge      = 64       // smaller is an icon or a badge
	minDescribeBytes     = 1024     // tiny files are spacers and bullets
	maxDescribeBytes     = 20 << 20 // the vision pipeline's own attachment cap
	describeWorkers      = 3
	markerSummaryRunes   = 240
)

// describableExts are the raster formats vision models read.
var describableExts = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true}

// docsImageJob is one image to describe.
type docsImageJob struct {
	asset   string   // the asset path the chunks reference
	file    string   // where it was found on disk (or "" for remote data)
	data    []byte   // loaded bytes
	mime    string   //
	alt     string   // first alt text seen
	docs    []string // documents that reference it
	heading string   // a heading near its first reference
}

// docsImageReport summarizes an images run for the tool result.
type docsImageReport struct {
	Mode      string
	Found     int // distinct referenced images
	Eligible  int // local raster images that pass the filters
	Described int
	Cached    int
	Skipped   map[string]int // reason → count
	Tokens    int            // estimated input tokens (estimate mode)
	Models    map[string]int
	Err       string
}

func (r *docsImageReport) skip(reason string) {
	if r.Skipped == nil {
		r.Skipped = map[string]int{}
	}
	r.Skipped[reason]++
}

// String renders the report as one summary line.
func (r *docsImageReport) String() string {
	if r == nil || r.Found == 0 {
		return ""
	}
	var b strings.Builder
	switch r.Mode {
	case docsImagesEstimate:
		fmt.Fprintf(&b, "images: %d referenced, %d would be described (~%d input tokens, plus ~%d output); re-run with images=describe",
			r.Found, r.Eligible, r.Tokens, r.Eligible*400)
	default:
		fmt.Fprintf(&b, "images: %d referenced, %d described (%d from cache)", r.Found, r.Described, r.Cached)
		if len(r.Models) > 0 {
			var ms []string
			for m := range r.Models {
				ms = append(ms, m)
			}
			sort.Strings(ms)
			b.WriteString(" by " + strings.Join(ms, ", "))
		}
	}
	if len(r.Skipped) > 0 {
		var parts []string
		for reason, n := range r.Skipped {
			parts = append(parts, fmt.Sprintf("%d %s", n, reason))
		}
		sort.Strings(parts)
		b.WriteString("; skipped: " + strings.Join(parts, ", "))
	}
	if r.Err != "" {
		b.WriteString("; " + r.Err)
	}
	return b.String()
}

// collectImageJobs gathers the distinct images the chunks reference, with
// the documents that reference them.
func collectImageJobs(chunks []docsFlattenChunk) []*docsImageJob {
	byPath := map[string]*docsImageJob{}
	var order []string
	for _, c := range chunks {
		for _, a := range c.Assets {
			if a.Kind != chunkKindImage {
				continue
			}
			j, ok := byPath[a.Path]
			if !ok {
				j = &docsImageJob{asset: a.Path, alt: a.Alt, heading: headingBefore(c.Content, a.Path)}
				byPath[a.Path] = j
				order = append(order, a.Path)
			}
			if j.alt == "" {
				j.alt = a.Alt
			}
			if len(j.docs) == 0 || j.docs[len(j.docs)-1] != c.Source {
				j.docs = append(j.docs, c.Source)
			}
		}
	}
	jobs := make([]*docsImageJob, 0, len(order))
	for _, p := range order {
		jobs = append(jobs, byPath[p])
	}
	return jobs
}

// headingBefore returns the last Markdown heading above the first mention
// of ref in content.
func headingBefore(content, ref string) string {
	i := strings.Index(content, ref)
	if i < 0 {
		i = len(content)
	}
	heading := ""
	for _, l := range strings.Split(content[:i], "\n") {
		if docsFlattenHeading.MatchString(strings.TrimSpace(l)) {
			heading = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l), "#"))
		}
	}
	return heading
}

// resolveLocalImage finds an asset on disk under root. Sites serve
// "/img/x.png" from a static or public directory, so those are tried too.
// The result never escapes root.
func resolveLocalImage(root, asset string) string {
	if root == "" || strings.Contains(asset, "://") || strings.HasPrefix(asset, "//") {
		return ""
	}
	clean := path.Clean("/" + asset)
	for _, base := range []string{"", "static", "public", "assets", "docs/static", "website/static"} {
		p := filepath.Join(root, filepath.FromSlash(path.Join(base, clean)))
		if rel, err := filepath.Rel(root, p); err != nil || strings.HasPrefix(rel, "..") {
			continue
		}
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}

// decorativeName flags files that are almost never content: logos, icons,
// favicons, badges, avatars.
func decorativeName(asset string) bool {
	base := strings.ToLower(path.Base(asset))
	for _, w := range []string{"logo", "icon", "favicon", "badge", "avatar", "sprite", "emoji", "spinner"} {
		if strings.Contains(base, w) {
			return true
		}
	}
	return false
}

// loadImageJob reads and filters one image. reason is non-empty when it is
// skipped.
func loadImageJob(j *docsImageJob, root string, fetch func(string) ([]byte, error)) (reason string) {
	ext := strings.ToLower(path.Ext(strings.SplitN(j.asset, "?", 2)[0]))
	switch {
	case ext == ".svg":
		return "svg (its text is already in the corpus)"
	case !describableExts[ext]:
		return "unsupported format"
	case decorativeName(j.asset):
		return "logos/icons"
	}
	var data []byte
	if strings.Contains(j.asset, "://") || strings.HasPrefix(j.asset, "//") {
		if fetch == nil {
			return "remote (not downloaded)"
		}
		b, err := fetch(j.asset)
		if err != nil {
			return "download failed"
		}
		data = b
	} else {
		j.file = resolveLocalImage(root, j.asset)
		if j.file == "" {
			return "not found"
		}
		info, err := os.Stat(j.file)
		if err != nil || info.Size() > maxDescribeBytes {
			return "too large"
		}
		b, err := os.ReadFile(j.file) // #nosec G304 -- resolved under the corpus root by resolveLocalImage
		if err != nil {
			return "unreadable"
		}
		data = b
	}
	if len(data) < minDescribeBytes {
		return "tiny"
	}
	mime, ok := models.DetectImageMediaType(data)
	if !ok {
		return "not an image"
	}
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil && (cfg.Width < minDescribeEdge || cfg.Height < minDescribeEdge) {
		return "tiny"
	}
	j.data, j.mime = data, mime
	return ""
}

// docsImagePrompt asks for a description that retrieval and a reader can
// use: what the image is, every element, every flow, all text verbatim.
func docsImagePrompt(j *docsImageJob) string {
	var ctx []string
	if len(j.docs) > 0 {
		ctx = append(ctx, "document: "+j.docs[0])
	}
	if j.heading != "" {
		ctx = append(ctx, "section: "+j.heading)
	}
	if j.alt != "" {
		ctx = append(ctx, "alt text: "+j.alt)
	}
	return "You are indexing technical documentation. Describe this image so that someone who cannot see it can answer questions about it.\n" +
		"1. Say what it is (architecture diagram, sequence diagram, flowchart, ER diagram, chart, UI screenshot, photo…).\n" +
		"2. List every component, actor, box or screen element and what it represents.\n" +
		"3. List every connection or flow, with its direction and label, in order (\"A → B: label\").\n" +
		"4. Transcribe ALL visible text verbatim.\n" +
		"5. In one sentence, say what the image shows in the context of the documentation.\n" +
		"Write plain text with short lines, no preamble, in the language of the surrounding documentation (" + strings.Join(ctx, "; ") + ")."
}

// docsVisionCache stores descriptions by image content and prompt version.
type docsVisionCache struct{ dir string }

type cachedDescription struct {
	Model       string `json:"model"`
	Description string `json:"description"`
}

func openDocsVisionCache() docsVisionCache {
	dir, err := utils.PrivateStateDir("docs-flatten-vision")
	if err != nil {
		return docsVisionCache{}
	}
	return docsVisionCache{dir: dir}
}

func (c docsVisionCache) key(data []byte) string {
	h := sha256.New()
	h.Write([]byte(docsImagePromptVersion))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

func (c docsVisionCache) get(data []byte) (cachedDescription, bool) {
	if c.dir == "" {
		return cachedDescription{}, false
	}
	b, err := os.ReadFile(filepath.Join(c.dir, c.key(data)+".json")) // #nosec G304 -- hex digest name under the private state dir
	if err != nil {
		return cachedDescription{}, false
	}
	var d cachedDescription
	if json.Unmarshal(b, &d) != nil || strings.TrimSpace(d.Description) == "" {
		return cachedDescription{}, false
	}
	return d, true
}

func (c docsVisionCache) put(data []byte, d cachedDescription) {
	if c.dir == "" {
		return
	}
	b, _ := json.Marshal(d)
	_ = os.WriteFile(filepath.Join(c.dir, c.key(data)+".json"), b, 0o600)
}

// describeDocsImages runs the images mode over the flattened chunks: it
// returns the chunks with descriptions attached (and image chunks
// appended) plus a report. In estimate mode nothing is called.
func describeDocsImages(ctx context.Context, chunks []docsFlattenChunk, cfg docsFlattenArgs, root string,
	fetch func(string) ([]byte, error), describer DocsImageDescriber, emit func(string)) ([]docsFlattenChunk, *docsImageReport) {

	rep := &docsImageReport{Mode: cfg.Images, Models: map[string]int{}}
	jobs := collectImageJobs(chunks)
	rep.Found = len(jobs)
	if len(jobs) == 0 {
		return chunks, rep
	}
	maxImages := cfg.MaxImages
	if maxImages <= 0 {
		maxImages = defaultDocsMaxImages
	}
	var eligible []*docsImageJob
	for _, j := range jobs {
		if reason := loadImageJob(j, root, fetch); reason != "" {
			rep.skip(reason)
			continue
		}
		if len(eligible) >= maxImages {
			rep.skip(fmt.Sprintf("over maxImages=%d", maxImages))
			continue
		}
		eligible = append(eligible, j)
	}
	rep.Eligible = len(eligible)
	if cfg.Images == docsImagesEstimate {
		for _, j := range eligible {
			rep.Tokens += models.EstimateImageTokens(models.ImageContent{MediaType: j.mime, Data: j.data}) + len(docsImagePrompt(j))/4
		}
		return chunks, rep
	}
	if describer == nil {
		rep.Err = "no vision pipeline in this session: images kept as markers"
		return chunks, rep
	}

	cache := openDocsVisionCache()
	results := make(map[string]cachedDescription, len(eligible))
	var mu sync.Mutex
	var firstErr error
	sem := make(chan struct{}, describeWorkers)
	var wg sync.WaitGroup
	var done int32
	for _, j := range eligible {
		if ctx.Err() != nil {
			break
		}
		if d, ok := cache.get(j.data); ok {
			mu.Lock()
			results[j.asset] = d
			rep.Cached++
			mu.Unlock()
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(j *docsImageJob) {
			defer wg.Done()
			defer func() { <-sem }()
			desc, model, err := describer.DescribeImage(ctx, docsImagePrompt(j),
				models.ImageContent{MediaType: j.mime, Data: j.data, FileName: path.Base(j.asset)})
			n := atomic.AddInt32(&done, 1)
			mu.Lock()
			defer mu.Unlock()
			if err != nil || strings.TrimSpace(desc) == "" {
				if firstErr == nil && err != nil {
					firstErr = err
				}
				rep.skip("description failed")
				return
			}
			d := cachedDescription{Model: model, Description: strings.TrimSpace(desc)}
			cache.put(j.data, d)
			results[j.asset] = d
			emit(fmt.Sprintf("described %s (%d/%d)", j.asset, n, len(eligible)))
		}(j)
	}
	wg.Wait()
	if firstErr != nil && len(results) == 0 {
		rep.Err = "description failed: " + firstErr.Error()
	}
	if len(results) == 0 {
		return chunks, rep
	}

	next := len(chunks) + 1
	for _, j := range eligible {
		d, ok := results[j.asset]
		if !ok {
			continue
		}
		rep.Described++
		rep.Models[d.Model]++
		chunks = attachDescription(chunks, j, d)
		content := imageChunkContent(j, d)
		title := "Image: " + firstNonEmptyStr(j.alt, path.Base(j.asset))
		chunks = append(chunks, docsFlattenChunk{
			ID:        fmt.Sprintf("%s#%04d", j.asset, next),
			Source:    j.asset,
			Title:     title,
			Content:   content,
			ChunkSize: len(content),
			RepoURL:   chunks[0].RepoURL,
			Commit:    chunks[0].Commit,
			Kind:      chunkKindImage,
			Assets:    []docsFlattenAsset{{Path: j.asset, Kind: chunkKindImage, Alt: j.alt, Description: d.Description}},
		})
		next++
	}
	return chunks, rep
}

// imageChunkContent is the text of an image's own chunk.
func imageChunkContent(j *docsImageJob, d cachedDescription) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Image: %s\n\n", j.asset)
	if len(j.docs) > 0 {
		fmt.Fprintf(&b, "Referenced by: %s\n", strings.Join(j.docs, ", "))
	}
	if j.heading != "" {
		fmt.Fprintf(&b, "Section: %s\n", j.heading)
	}
	if j.alt != "" {
		fmt.Fprintf(&b, "Alt text: %s\n", j.alt)
	}
	fmt.Fprintf(&b, "Described by: %s\n\n%s\n", d.Model, d.Description)
	return b.String()
}

// attachDescription records the description on every chunk that references
// the image and extends its marker with the description's opening, so the
// document's own chunk is retrievable by what the picture shows.
func attachDescription(chunks []docsFlattenChunk, j *docsImageJob, d cachedDescription) []docsFlattenChunk {
	summary := []rune(strings.Join(strings.Fields(d.Description), " "))
	short := string(summary)
	if len(summary) > markerSummaryRunes {
		short = string(summary[:markerSummaryRunes]) + "…"
	}
	for i := range chunks {
		touched := false
		for k := range chunks[i].Assets {
			if chunks[i].Assets[k].Kind == chunkKindImage && chunks[i].Assets[k].Path == j.asset {
				chunks[i].Assets[k].Description = d.Description
				touched = true
			}
		}
		if !touched {
			continue
		}
		chunks[i].Content = extendImageMarkers(chunks[i].Content, j.asset, short)
		chunks[i].ChunkSize = len(chunks[i].Content)
	}
	return chunks
}

// extendImageMarkers turns "[image: alt — path]" into
// "[image: alt — path | shows: …]".
func extendImageMarkers(content, asset, short string) string {
	for _, tail := range []string{" — " + asset + "]", "[image: " + asset + "]"} {
		if !strings.Contains(content, tail) {
			continue
		}
		repl := strings.TrimSuffix(tail, "]") + " | shows: " + short + "]"
		return strings.ReplaceAll(content, tail, repl)
	}
	return content
}

// errNoImagesMode guards the argument values.
var errNoImagesMode = errors.New(`invalid images mode (valid: markers|describe|estimate)`)
