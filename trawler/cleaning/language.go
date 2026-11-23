package cleaning

import "strings"

// iso6392to1 maps ISO 639-2/B and MARC three-letter language codes to ISO
// 639-1 two-letter codes. This is the union of codes observed across all
// upstream sources (DataCite, DOAJ, PubMed). MARC-specific deprecated codes
// are included for PubMed compatibility.
var iso6392to1 = map[string]string{
	"eng": "en", "fre": "fr", "ger": "de", "spa": "es", "ita": "it",
	"por": "pt", "rus": "ru", "jpn": "ja", "chi": "zh", "kor": "ko",
	"dut": "nl", "pol": "pl", "swe": "sv", "dan": "da", "nor": "no",
	"fin": "fi", "ara": "ar", "heb": "he", "tur": "tr", "cze": "cs",
	"hun": "hu", "rum": "ro", "gre": "el", "ukr": "uk", "hrv": "hr",
	"slv": "sl", "bul": "bg", "cat": "ca", "vie": "vi", "ind": "id",
	"per": "fa", "hin": "hi", "lat": "la", "wel": "cy", "geo": "ka",
	"afr": "af", "slo": "sk", "lit": "lt", "lav": "lv", "est": "et",
	"alb": "sq", "ice": "is", "mac": "mk", "ser": "sr", "bos": "bs",
	"mon": "mn", "ben": "bn", "tam": "ta", "glg": "gl",
	"scr": "hr", // deprecated MARC code for Croatian
	"scc": "sr", // deprecated MARC code for Serbian
}

// NormalizeLanguage converts a language tag to a two-letter ISO 639-1 code.
// It accepts:
//   - ISO 639-1 two-letter codes (passed through as-is after lowercasing)
//   - ISO 639-2/B and MARC three-letter codes (e.g. "eng" → "en")
//   - BCP 47 language tags (e.g. "en-US" → "en")
//
// Returns an empty string if the input cannot be mapped.
func NormalizeLanguage(lang string) string {
	lang = strings.ToLower(strings.TrimSpace(lang))
	if lang == "" {
		return ""
	}
	if len(lang) == 2 {
		return lang
	}
	if len(lang) == 3 {
		return iso6392to1[lang]
	}
	// BCP 47 tags like "en-us" or "zh-tw"
	if len(lang) > 2 && lang[2] == '-' {
		return lang[:2]
	}
	return ""
}

// iso6391 is every ISO 639-1 code, for telling a real two-letter code from a
// two-letter word.
var iso6391 = func() map[string]bool {
	m := map[string]bool{}
	for _, c := range strings.Fields(`aa ab ae af ak am an ar as av ay az ba be bg bh bi bm bn bo br bs
		ca ce ch co cr cs cu cv cy da de dv dz ee el en eo es et eu fa ff fi fj fo fr fy ga gd gl gn gu gv
		ha he hi ho hr ht hu hy hz ia id ie ig ii ik io is it iu ja jv ka kg ki kj kk kl km kn ko kr ks ku
		kv kw ky la lb lg li ln lo lt lu lv mg mh mi mk ml mn mr ms mt my na nb nd ne ng nl nn no nr nv ny
		oc oj om or os pa pi pl ps pt qu rm rn ro ru rw sa sc sd se sg si sk sl sm sn so sq sr ss st su sv
		sw ta te tg th ti tk tl tn to tr ts tt tw ty ug uk ur uz ve vi vo wa wo xh yi yo za zh zu`) {
		m[c] = true
	}
	return m
}()

// iso6392T holds the ISO 639-2/T ("terminology") codes that differ from the
// /B codes in iso6392to1. Repositories use both.
var iso6392T = map[string]string{
	"deu": "de", "fra": "fr", "ces": "cs", "nld": "nl", "ell": "el", "slk": "sk",
	"ron": "ro", "zho": "zh", "fas": "fa", "hye": "hy", "eus": "eu", "isl": "is",
	"mkd": "mk", "msa": "ms", "sqi": "sq", "bod": "bo", "cym": "cy", "mya": "my",
	"kat": "ka", "srp": "sr",
}

// languageNames maps language names, in English and in the language itself,
// to ISO 639-1. Lowercase keys. Built from the names that actually turn up in
// repository metadata, so it is broad rather than exhaustive.
var languageNames = map[string]string{
	"english": "en", "inglés": "en", "ingles": "en", "inglês": "en", "engels": "en", "inglise": "en", "anglais": "en", "englisch": "en",
	"spanish": "es", "español": "es", "espanol": "es", "castellano": "es", "castilian": "es", "esp": "es",
	"portuguese": "pt", "português": "pt", "portugues": "pt",
	"french": "fr", "français": "fr", "francais": "fr",
	"german": "de", "deutsch": "de", "saksa": "de",
	"italian": "it", "italiano": "it",
	"dutch": "nl", "nederlands": "nl",
	"turkish": "tr", "türkçe": "tr", "turkce": "tr",
	"czech": "cs", "čeština": "cs", "cestina": "cs",
	"slovak": "sk", "slovenčina": "sk",
	"slovenian": "sl", "slovene": "sl", "slovenščina": "sl",
	"greek": "el", "ελληνικά": "el", "gr": "el",
	"hungarian": "hu", "magyar": "hu",
	"estonian": "et", "eesti": "et",
	"latvian": "lv", "latviešu": "lv",
	"lithuanian": "lt", "lietuvių": "lt",
	"finnish": "fi", "suomi": "fi",
	"swedish": "sv", "svenska": "sv",
	"norwegian": "no", "norsk": "no",
	"danish": "da", "dansk": "da",
	"icelandic": "is", "íslenska": "is",
	"polish": "pl", "polski": "pl",
	"russian": "ru", "русский": "ru", "vene": "ru",
	"ukrainian": "uk", "українська": "uk", "ua": "uk",
	"croatian": "hr", "hrvatski": "hr",
	"serbian": "sr", "srpski": "sr", "српски": "sr",
	"bosnian": "bs", "macedonian": "mk", "bulgarian": "bg", "albanian": "sq",
	"romanian": "ro", "română": "ro",
	"catalan": "ca", "català": "ca",
	"galician": "gl", "galego": "gl",
	"basque": "eu", "euskara": "eu",
	"latin": "la", "ladina": "la",
	"indonesian": "id", "bahasa indonesia": "id", "malay": "ms",
	"thai": "th", "vietnamese": "vi",
	"chinese": "zh", "japanese": "ja", "korean": "ko",
	"arabic": "ar", "persian": "fa", "farsi": "fa", "hebrew": "he", "hindi": "hi",
	"georgian": "ka", "afrikaans": "af", "welsh": "cy", "irish": "ga",
	"angol": "en", "kurdish": "ku", "filipino": "tl", "flemish": "nl", "deutsche": "de",
	"γαλλικά": "fr", "γερμανικά": "de", "ισπανικά": "es", "中文": "zh",
	"cmn": "zh", "zsm": "ms",
}

// NormalizeLanguageLoose is NormalizeLanguage for free-text language fields:
// it also understands language names, locale tags with underscores
// ("pt_BR"), codes wrapped in decoration ("English [eng]", "en;") and lists
// ("eng deu", "Turkish and English"), returning the first language it
// recognises. Unlike NormalizeLanguage it only ever returns a real ISO 639-1
// code, so a two-letter word is not mistaken for one.
func NormalizeLanguageLoose(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return ""
	}
	if code, ok := languageNames[s]; ok {
		return code
	}
	tokens := strings.FieldsFunc(s, func(r rune) bool {
		return strings.ContainsRune(" \t;,/|:()[]<>", r)
	})
	for _, t := range tokens {
		if code := looseToken(t); code != "" {
			return code
		}
	}
	return ""
}

func looseToken(t string) string {
	if code, ok := languageNames[t]; ok {
		return code
	}
	t = strings.ReplaceAll(t, "_", "-")
	if code, ok := iso6392T[t]; ok {
		return code
	}
	if code := NormalizeLanguage(t); iso6391[code] {
		return code
	}
	return ""
}
