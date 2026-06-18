package detectors

import (
	"strings"
	"unicode"

	"github.com/Korbicorp/klovis/internal/anonymizer"
	"github.com/dlclark/regexp2"
)

const (
	priorityCritical = 1000
	priorityHigh     = 900
	priorityDefault  = 700
	priorityMedium   = 600
	priorityName     = 500
	priorityGeneric  = 100
)

type regexDetector struct {
	// entityType determines the anonymized token family emitted by this detector.
	entityType anonymizer.EntityType
	// priority is copied to matches so the anonymizer can resolve overlaps.
	priority int
	// pattern is compiled once and reused for every scan.
	pattern *regexp2.Regexp
	// captureGroup selects a submatch as the sensitive value when labels are kept.
	// Example: with "Prénom: Armand", group 0 is the full match and group 2 is
	// only "Armand", which lets the anonymizer preserve the "Prénom:" label.
	captureGroup int
	// spanAdjuster trims or rejects a captured span after regex matching.
	spanAdjuster func([]byte, int, int) (int, int)
	// normalizer converts the matched value into a stable pseudonymization key.
	normalizer func([]byte) string
	// matchNormalizer can normalize from the full regex match context.
	matchNormalizer func([]byte, []int) string
}

func (d regexDetector) FindAll(text []byte) []anonymizer.Match {
	input := string(text)
	runeToByte := runeToByteOffsets(input)

	match, err := d.pattern.FindStringMatch(input)
	if err != nil {
		panic(err)
	}

	var matches []anonymizer.Match
	for match != nil {
		start, end, ok := captureByteRange(match, d.captureGroup, runeToByte)
		if !ok {
			next, nextErr := d.pattern.FindNextMatch(match)
			if nextErr != nil {
				panic(nextErr)
			}
			match = next
			continue
		}
		if d.spanAdjuster != nil {
			start, end = d.spanAdjuster(text, start, end)
			if start >= end {
				next, nextErr := d.pattern.FindNextMatch(match)
				if nextErr != nil {
					panic(nextErr)
				}
				match = next
				continue
			}
		}

		normalized := ""
		switch {
		case d.matchNormalizer != nil:
			normalized = d.matchNormalizer(text, buildMatchIndex(match, runeToByte))
		case d.normalizer != nil:
			normalized = d.normalizer(text[start:end])
		default:
			normalized = normalizeFold(text[start:end])
		}

		matches = append(matches, anonymizer.Match{
			Start:      start,
			End:        end,
			Type:       d.entityType,
			Priority:   d.priority,
			Normalized: normalized,
		})

		match, err = d.pattern.FindNextMatch(match)
		if err != nil {
			panic(err)
		}
	}

	return matches
}

func runeToByteOffsets(input string) []int {
	offsets := make([]int, 0, len([]rune(input))+1)
	for index := range input {
		offsets = append(offsets, index)
	}
	offsets = append(offsets, len(input))
	return offsets
}

func captureByteRange(match *regexp2.Match, captureGroup int, runeToByte []int) (int, int, bool) {
	group := match.GroupByNumber(captureGroup)
	if group == nil || group.Length == 0 && captureGroup > 0 {
		return 0, 0, false
	}

	start := group.Index
	end := group.Index + group.Length
	if start < 0 || end < start || end >= len(runeToByte) {
		return 0, 0, false
	}

	return runeToByte[start], runeToByte[end], true
}

func buildMatchIndex(match *regexp2.Match, runeToByte []int) []int {
	groups := match.Groups()
	indices := make([]int, len(groups)*2)
	for i, group := range groups {
		groupStart, groupEnd := -1, -1
		if group.Length > 0 || i == 0 {
			start := group.Index
			end := group.Index + group.Length
			if start >= 0 && end >= start && end < len(runeToByte) {
				groupStart = runeToByte[start]
				groupEnd = runeToByte[end]
			}
		}
		indices[i*2] = groupStart
		indices[i*2+1] = groupEnd
	}

	return indices
}

func Default(includeExtra bool) []anonymizer.Detector {
	// Core detectors cover the initially requested PII. Extra detectors are useful
	// defaults, but remain easy to disable from the CLI for stricter scope control.
	base := []anonymizer.Detector{
		emailDetector(),
		ipDetector(),
		nirDetector(),
		phoneDetector(),
		firstNameDetector(),
		lastNameDetector(),
		ContextualNameDetector(),
		FrenchAddressDetector(),
		birthDateDetector(),
		BloodTypeDetector(),
		addressDetector(),
	}

	if !includeExtra {
		return base
	}

	return append(base,
		urlDetector(),
		ibanDetector(),
		creditCardDetector(),
		macAddressDetector(),
		numericIDDetector(),
		referenceIDDetector(),
	)
}

func emailDetector() anonymizer.Detector {
	return regexDetector{
		entityType:   anonymizer.EntityEmail,
		priority:     priorityCritical,
		captureGroup: 0,
		pattern: regexp2.MustCompile(
			`(?i)\b[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}\b`,
			regexp2.RE2,
		),
		normalizer: normalizeFold,
	}
}

func urlDetector() anonymizer.Detector {
	return regexDetector{
		entityType:   anonymizer.EntityURL,
		priority:     priorityHigh,
		captureGroup: 0,
		pattern: regexp2.MustCompile(
			`(?i)\b(?:https?://|www\.)[^\s<>"']+[^\s<>"'.,;:!?)]`,
			regexp2.RE2,
		),
		normalizer: normalizeFold,
	}
}

func ipDetector() anonymizer.Detector {
	return regexDetector{
		entityType:   anonymizer.EntityIP,
		priority:     priorityHigh,
		captureGroup: 0,
		pattern: regexp2.MustCompile(
			`\b(?:(?:25[0-5]|2[0-4]\d|1?\d?\d)\.){3}(?:25[0-5]|2[0-4]\d|1?\d?\d)\b|`+
				`\b(?:`+
				`(?:[0-9a-fA-F]{1,4}:){7}[0-9a-fA-F]{1,4}|`+
				`(?:[0-9a-fA-F]{1,4}:){1,7}:|`+
				`(?:[0-9a-fA-F]{1,4}:){1,6}:[0-9a-fA-F]{1,4}|`+
				`(?:[0-9a-fA-F]{1,4}:){1,5}(?::[0-9a-fA-F]{1,4}){1,2}|`+
				`(?:[0-9a-fA-F]{1,4}:){1,4}(?::[0-9a-fA-F]{1,4}){1,3}|`+
				`(?:[0-9a-fA-F]{1,4}:){1,3}(?::[0-9a-fA-F]{1,4}){1,4}|`+
				`(?:[0-9a-fA-F]{1,4}:){1,2}(?::[0-9a-fA-F]{1,4}){1,5}|`+
				`[0-9a-fA-F]{1,4}:(?:(?::[0-9a-fA-F]{1,4}){1,6})|`+
				`:(?:(?::[0-9a-fA-F]{1,4}){1,7}|:)`+
				`)\b`,
			regexp2.RE2,
		),
		normalizer: normalizeFold,
	}
}

func nirDetector() anonymizer.Detector {
	return regexDetector{
		entityType:   anonymizer.EntityNIR,
		priority:     priorityCritical,
		captureGroup: 0,
		pattern: regexp2.MustCompile(
			`\b[12]\s?\d{2}\s?(?:0[1-9]|1[0-2]|2[ABab])\s?\d{2}\s?\d{3}\s?\d{3}\s?\d{2}\b`,
			regexp2.RE2,
		),
		normalizer: keepDigitsAndLetters,
	}
}

func phoneDetector() anonymizer.Detector {
	return regexDetector{
		entityType:   anonymizer.EntityPhone,
		priority:     priorityDefault,
		captureGroup: 0,
		pattern: regexp2.MustCompile(
			`(?i)(?:\b0[1-9](?:[\s.\-]?\d{2}){4}\b|\+33[\s.\-]?\(?0?\)?[\s.\-]?[1-9](?:[\s.\-]?\d{2}){4}\b|`+
				`\b00[1-9]\d{0,2}(?:[\s.\-]?\d{2}){4,6}\b)`,
			regexp2.RE2,
		),
		normalizer: normalizePhone,
	}
}

func firstNameDetector() anonymizer.Detector {
	return regexDetector{
		entityType:   anonymizer.EntityFirstName,
		priority:     priorityName,
		captureGroup: 2,
		spanAdjuster: trimConservativeValueSpan,
		pattern: regexp2.MustCompile(
			`(?i)\b(pr[ée]nom|first[ -]?name)\s*[:=]\s*([A-ZÀ-ÖØ-Ý][A-Za-zÀ-ÖØ-öø-ÿ' -]{1,60})`,
			regexp2.RE2,
		),
		normalizer: normalizeFold,
	}
}

func lastNameDetector() anonymizer.Detector {
	return regexDetector{
		entityType:   anonymizer.EntityLastName,
		priority:     priorityName,
		captureGroup: 2,
		spanAdjuster: trimConservativeValueSpan,
		pattern: regexp2.MustCompile(
			`(?i)\b(nom|last[ -]?name|surname)\s*[:=]\s*([A-ZÀ-ÖØ-Ý][A-Za-zÀ-ÖØ-öø-ÿ' -]{1,80})`,
			regexp2.RE2,
		),
		normalizer: normalizeFold,
	}
}

func ContextualNameDetector() anonymizer.Detector {
	// Keep the detector compatible with regexp2 by capturing the contextual prefix
	// plus the name span, instead of relying on a variable-length lookbehind.
	contextPattern := `(?i:(?:` +
		`je\s+m'appelle\s+|mon\s+nom\s+est\s+|cordialement,?\s+|de\s+la\s+part\s+de\s+|salut\s+c'est\s+|` +
		`my\s+name\s+is\s+|i\s+am\s+|sincerely,?\s+|regards,?\s+|hi\s+i'm\s+|hello\s+this\s+is\s+|` +
		`(?:\*\*|__|\*|_)?\bnom\b(?:\*\*|__|\*|_)?\s*:\s*(?:\*\*|__|\*|_)?\s*|` +
		`(?:\*\*|__|\*|_)?\bname\b(?:\*\*|__|\*|_)?\s*:\s*(?:\*\*|__|\*|_)?\s*` +
		`))`

	namePattern := `((?-i:(?:\p{Lu}\p{Ll}+|\p{Lu}+)(?!\s*:)(?:[- ](?:\p{Lu}\p{Ll}+|\p{Lu}+)(?!\s*:)){0,3}))\b`

	return regexDetector{
		entityType:   anonymizer.EntityName,
		priority:     priorityHigh,
		captureGroup: 1,
		pattern: regexp2.MustCompile(
			contextPattern+namePattern,
			regexp2.None,
		),
		normalizer: normalizeFold,
	}
}

func addressDetector() anonymizer.Detector {
	return regexDetector{
		entityType:   anonymizer.EntityAddress,
		priority:     priorityDefault,
		captureGroup: 1,
		spanAdjuster: trimConservativeValueSpan,
		pattern: regexp2.MustCompile(
			`(?i)\b(?:adresse|address)\s*[:=]\s*(`+
				`\d{1,4}\s*(?:bis|ter|quater)?\s+`+
				`(?:rue|avenue|av\.?|boulevard|bd\.?|chemin|impasse|place|all[ée]e|route|quai|cours|square|passage|voie|villa|résidence|residence|lotissement)\s+`+
				`[A-Za-zÀ-ÖØ-öø-ÿ0-9'’. -]{2,100}`+
				`(?:(?:,\s*|(?:\s+-\s+|\s+))(?:bât(?:iment)?|bat(?:iment)?|appartement|appt|escalier|étage|etage|porte)\s+[A-Za-zÀ-ÖØ-öø-ÿ0-9'’.-]{1,30}(?:,\s*|\s+)?)?`+
				`(?:,\s*)?(?:\b\d{5}\b\s+[A-Za-zÀ-ÖØ-öø-ÿ' -]{2,60})?`+
				`)`,
			regexp2.RE2,
		),
		normalizer: normalizeFold,
	}
}

func FrenchAddressDetector() anonymizer.Detector {
	return regexDetector{
		entityType:   anonymizer.EntityAddress,
		priority:     priorityHigh,
		captureGroup: 0,
		pattern: regexp2.MustCompile(
			`(?i)\b\d+(?:[\s,-]+(?:rue|avenue|boulevard|allée|place|route|chemin|faubourg|impasse|quai|square|cours)\b[\p{L}\s\d,'’.-]+)[\s,-]+\b\d{5}\b[\s,-]+(?-i:[A-ZÀ-ÖØ-Ý][\p{L}-]+(?:[\s-]+[A-ZÀ-ÖØ-Ý][\p{L}-]+){0,3})\b`,
			regexp2.None,
		),
		normalizer: normalizeFold,
	}
}

func birthDateDetector() anonymizer.Detector {
	return regexDetector{
		entityType:   anonymizer.EntityBirthDate,
		priority:     priorityMedium,
		captureGroup: 1,
		spanAdjuster: trimConservativeValueSpan,
		pattern: regexp2.MustCompile(
			`(?i)\b(?:date\s+de\s+naissance|date\s+of\s+birth|birth\s*date|dob|n(?:é|ée|e)\s+le|born\s+on)\s*[:=]?\s*(`+
				`(?:0?[1-9]|[12]\d|3[01])[\/.\-](?:0?[1-9]|1[0-2])[\/.\-](?:19|20)\d{2}|`+
				`(?:19|20)\d{2}-(?:0?[1-9]|1[0-2])-(?:0?[1-9]|[12]\d|3[01])|`+
				`(?:0?[1-9]|[12]\d|3[01])\s+(?:janvier|février|fevrier|mars|avril|mai|juin|juillet|août|aout|septembre|octobre|novembre|décembre|decembre)\s+(?:19|20)\d{2}`+
				`)`,
			regexp2.RE2,
		),
		normalizer: normalizeFold,
	}
}

func BloodTypeDetector() anonymizer.Detector {
	// Require explicit medical context so common letters like "A+" are not captured
	// in unrelated text such as grades or product names.
	contextPattern := `(?i)\b(?:` +
		`groupe\s+sanguin|groupe|sang|rhésus|rhesus|` +
		`blood\s+type|blood\s+group|blood` +
		`)\s+(?:est\s+de\s+type\s+|est\s+|is\s+)?`

	bloodPattern := `((?:A|B|AB|O)\s*(?:\+|-|positif|négatif|positive|negative|pos|neg))(?:\b|(?=\s|$|[.,;:!?]))`

	return regexDetector{
		entityType:   anonymizer.EntityBloodType,
		priority:     priorityMedium,
		captureGroup: 1,
		pattern: regexp2.MustCompile(
			contextPattern+bloodPattern,
			regexp2.None,
		),
		normalizer: normalizeFold,
	}
}

func ibanDetector() anonymizer.Detector {
	return regexDetector{
		entityType:   anonymizer.EntityIBAN,
		priority:     priorityCritical,
		captureGroup: 0,
		pattern: regexp2.MustCompile(
			`\b[A-Z]{2}\d{2}(?:[ ]?[A-Z0-9]){11,30}\b`,
			regexp2.RE2,
		),
		normalizer: keepDigitsAndLetters,
	}
}

func creditCardDetector() anonymizer.Detector {
	return regexDetector{
		entityType:   anonymizer.EntityCreditCard,
		priority:     priorityHigh,
		captureGroup: 0,
		pattern: regexp2.MustCompile(
			`\b(?:\d[ -]?){13,19}\b`,
			regexp2.RE2,
		),
		normalizer: keepDigitsAndLetters,
	}
}

func macAddressDetector() anonymizer.Detector {
	return regexDetector{
		entityType:   anonymizer.EntityMACAddress,
		priority:     priorityHigh,
		captureGroup: 0,
		pattern: regexp2.MustCompile(
			`\b[0-9a-fA-F]{2}(?::[0-9a-fA-F]{2}){5}\b|\b[0-9a-fA-F]{2}(?:-[0-9a-fA-F]{2}){5}\b`,
			regexp2.RE2,
		),
		normalizer: keepDigitsAndLetters,
	}
}

func numericIDDetector() anonymizer.Detector {
	return regexDetector{
		entityType:   anonymizer.EntityNumericID,
		priority:     priorityGeneric,
		captureGroup: 0,
		pattern: regexp2.MustCompile(
			`\b\d(?:[\s_-]?\d){6,}\b`,
			regexp2.RE2,
		),
		normalizer: keepDigitsAndLetters,
	}
}

func referenceIDDetector() anonymizer.Detector {
	return regexDetector{
		entityType:   anonymizer.EntityReferenceID,
		priority:     priorityGeneric,
		captureGroup: 2,
		spanAdjuster: requireLettersAndDigitsSpan,
		pattern: regexp2.MustCompile(
			`(?i)\b(id|user id|client id|customer id|reference|ref|account|ticket)\s*[:=]\s*([A-Z0-9][A-Z0-9_-]{6,})\b`,
			regexp2.RE2,
		),
		normalizer: keepDigitsAndLetters,
	}
}

func requireLettersAndDigitsSpan(text []byte, start, end int) (int, int) {
	hasLetter := false
	hasDigit := false
	for _, r := range string(text[start:end]) {
		hasLetter = hasLetter || unicode.IsLetter(r)
		hasDigit = hasDigit || unicode.IsDigit(r)
	}
	if !hasLetter || !hasDigit {
		return start, start
	}

	return start, end
}

func normalizeFold(value []byte) string {
	return strings.ToLower(strings.Join(strings.Fields(string(value)), " "))
}

func trimConservativeValueSpan(text []byte, start, end int) (int, int) {
	// Conservative name/address patterns intentionally require a label, but their
	// value capture can otherwise consume the next labelled field on the same line.
	for start < end && unicode.IsSpace(rune(text[start])) {
		start++
	}
	for start < end && unicode.IsSpace(rune(text[end-1])) {
		end--
	}

	value := strings.ToLower(string(text[start:end]))
	for _, label := range []string{
		" email",
		" e-mail",
		" mail",
		" tel",
		" tél",
		" telephone",
		" téléphone",
		" phone",
		" ip",
		" nir",
		" n°",
		" numero",
		" numéro",
		" adresse",
		" address",
		" nom",
		" prénom",
		" prenom",
		" first name",
		" last name",
		" surname",
		" date de naissance",
		" date of birth",
		" birth date",
		" dob",
	} {
		if index := strings.Index(value, label); index >= 0 {
			end = start + index
			break
		}
	}
	for start < end && unicode.IsSpace(rune(text[end-1])) {
		end--
	}

	return start, end
}

func normalizePhone(value []byte) string {
	var builder strings.Builder
	for _, r := range string(value) {
		if unicode.IsDigit(r) || r == '+' {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func keepDigitsAndLetters(value []byte) string {
	var builder strings.Builder
	for _, r := range string(value) {
		if unicode.IsDigit(r) || unicode.IsLetter(r) {
			builder.WriteRune(unicode.ToUpper(r))
		}
	}
	return builder.String()
}
