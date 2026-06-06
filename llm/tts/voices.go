/*
 * ChatCLI - Kokoro voice catalog for the embedded TTS provider.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The kokoro-multi-lang-v1_0 model is multi-speaker: a voice is selected by
 * numeric speaker id (--sid) and the G2P language is passed via --kokoro-lang.
 * Names follow the upstream convention <lang><gender>_<name> — bm_george is a
 * British male, pm_alex a Brazilian Portuguese male. The sid is the voice's
 * position in voices.bin; the table below mirrors the upstream catalog at
 * https://k2-fsa.github.io/sherpa/onnx/tts/all/Chinese-English/kokoro-multi-lang-v1_0.html
 * (53 voices, 0-indexed). A wrong sid speaks with the wrong voice silently, so
 * this table must track the model asset version pinned in provision.go.
 */
package tts

// kokoroVoice describes one speaker of the kokoro-multi-lang-v1_0 model.
type kokoroVoice struct {
	sid  int    // index into voices.bin, passed as --sid
	lang string // espeak-ng G2P language, passed as --kokoro-lang
}

// Kokoro language codes by voice-name prefix (first letter): a=American
// English, b=British English, e=Spanish, f=French, h=Hindi, i=Italian,
// j=Japanese, p=Brazilian Portuguese, z=Mandarin. English accents come from
// the voice embedding itself, so both a* and b* use "en".
const (
	kokoroLangEN = "en"
	kokoroLangES = "es"
	kokoroLangFR = "fr"
	kokoroLangHI = "hi"
	kokoroLangIT = "it"
	kokoroLangJA = "ja"
	kokoroLangPT = "pt-br"
	kokoroLangZH = "zh"
)

// Default Jarvis-style voices: a calm British male for English and a Brazilian
// Portuguese male for Portuguese.
const (
	defaultEmbeddedEnVoice = "bm_george"
	defaultEmbeddedPtVoice = "pm_alex"
)

// kokoroVoices maps voice name → speaker id and G2P language for
// kokoro-multi-lang-v1_0.
var kokoroVoices = map[string]kokoroVoice{
	"af_alloy":      {0, kokoroLangEN},
	"af_aoede":      {1, kokoroLangEN},
	"af_bella":      {2, kokoroLangEN},
	"af_heart":      {3, kokoroLangEN},
	"af_jessica":    {4, kokoroLangEN},
	"af_kore":       {5, kokoroLangEN},
	"af_nicole":     {6, kokoroLangEN},
	"af_nova":       {7, kokoroLangEN},
	"af_river":      {8, kokoroLangEN},
	"af_sarah":      {9, kokoroLangEN},
	"af_sky":        {10, kokoroLangEN},
	"am_adam":       {11, kokoroLangEN},
	"am_echo":       {12, kokoroLangEN},
	"am_eric":       {13, kokoroLangEN},
	"am_fenrir":     {14, kokoroLangEN},
	"am_liam":       {15, kokoroLangEN},
	"am_michael":    {16, kokoroLangEN},
	"am_onyx":       {17, kokoroLangEN},
	"am_puck":       {18, kokoroLangEN},
	"am_santa":      {19, kokoroLangEN},
	"bf_alice":      {20, kokoroLangEN},
	"bf_emma":       {21, kokoroLangEN},
	"bf_isabella":   {22, kokoroLangEN},
	"bf_lily":       {23, kokoroLangEN},
	"bm_daniel":     {24, kokoroLangEN},
	"bm_fable":      {25, kokoroLangEN},
	"bm_george":     {26, kokoroLangEN},
	"bm_lewis":      {27, kokoroLangEN},
	"ef_dora":       {28, kokoroLangES},
	"em_alex":       {29, kokoroLangES},
	"ff_siwis":      {30, kokoroLangFR},
	"hf_alpha":      {31, kokoroLangHI},
	"hf_beta":       {32, kokoroLangHI},
	"hm_omega":      {33, kokoroLangHI},
	"hm_psi":        {34, kokoroLangHI},
	"if_sara":       {35, kokoroLangIT},
	"im_nicola":     {36, kokoroLangIT},
	"jf_alpha":      {37, kokoroLangJA},
	"jf_gongitsune": {38, kokoroLangJA},
	"jf_nezumi":     {39, kokoroLangJA},
	"jf_tebukuro":   {40, kokoroLangJA},
	"jm_kumo":       {41, kokoroLangJA},
	"pf_dora":       {42, kokoroLangPT},
	"pm_alex":       {43, kokoroLangPT},
	"pm_santa":      {44, kokoroLangPT},
	"zf_xiaobei":    {45, kokoroLangZH},
	"zf_xiaoni":     {46, kokoroLangZH},
	"zf_xiaoxiao":   {47, kokoroLangZH},
	"zf_xiaoyi":     {48, kokoroLangZH},
	"zm_yunjian":    {49, kokoroLangZH},
	"zm_yunxi":      {50, kokoroLangZH},
	"zm_yunxia":     {51, kokoroLangZH},
	"zm_yunyang":    {52, kokoroLangZH},
}

// voiceInfo resolves a kokoro voice name to its speaker id and language.
func voiceInfo(name string) (kokoroVoice, bool) {
	v, ok := kokoroVoices[name]
	return v, ok
}
