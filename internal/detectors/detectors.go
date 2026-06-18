package detectors

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/Korbicorp/klovis/internal/anonymizer"
)

const (
	priorityCritical = 1000
	priorityHigh     = 900
	priorityDefault  = 700
	priorityName     = 500
	priorityGeneric  = 100
)

type regexDetector struct {
	// entityType determines the anonymized token family emitted by this detector.
	entityType anonymizer.EntityType
	// priority is copied to matches so the anonymizer can resolve overlaps.
	priority int
	// pattern is compiled once and reused for every scan.
	pattern *regexp.Regexp
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
	indices := d.pattern.FindAllSubmatchIndex(text, -1)
	matches := make([]anonymizer.Match, 0, len(indices))

	for _, index := range indices {
		start, end := index[0], index[1]
		if d.captureGroup > 0 {
			groupStart := d.captureGroup * 2
			groupEnd := groupStart + 1
			if groupEnd >= len(index) || index[groupStart] < 0 || index[groupEnd] < 0 {
				continue
			}
			start, end = index[groupStart], index[groupEnd]
		}
		if d.spanAdjuster != nil {
			start, end = d.spanAdjuster(text, start, end)
			if start >= end {
				continue
			}
		}

		normalized := ""
		switch {
		case d.matchNormalizer != nil:
			normalized = d.matchNormalizer(text, index)
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
	}

	return matches
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
		pattern: regexp.MustCompile(
			`(?i)\b[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}\b`,
		),
		normalizer: normalizeFold,
	}
}

func urlDetector() anonymizer.Detector {
	return regexDetector{
		entityType:   anonymizer.EntityURL,
		priority:     priorityHigh,
		captureGroup: 0,
		pattern: regexp.MustCompile(
			`(?i)\b(?:https?://|www\.)[^\s<>"']+[^\s<>"'.,;:!?)]`,
		),
		normalizer: normalizeFold,
	}
}

func ipDetector() anonymizer.Detector {
	return regexDetector{
		entityType:   anonymizer.EntityIP,
		priority:     priorityHigh,
		captureGroup: 0,
		pattern: regexp.MustCompile(
			`\b(?:(?:25[0-5]|2[0-4]\d|1?\d?\d)\.){3}(?:25[0-5]|2[0-4]\d|1?\d?\d)\b|` +
				`\b(?:` +
				`(?:[0-9a-fA-F]{1,4}:){7}[0-9a-fA-F]{1,4}|` +
				`(?:[0-9a-fA-F]{1,4}:){1,7}:|` +
				`(?:[0-9a-fA-F]{1,4}:){1,6}:[0-9a-fA-F]{1,4}|` +
				`(?:[0-9a-fA-F]{1,4}:){1,5}(?::[0-9a-fA-F]{1,4}){1,2}|` +
				`(?:[0-9a-fA-F]{1,4}:){1,4}(?::[0-9a-fA-F]{1,4}){1,3}|` +
				`(?:[0-9a-fA-F]{1,4}:){1,3}(?::[0-9a-fA-F]{1,4}){1,4}|` +
				`(?:[0-9a-fA-F]{1,4}:){1,2}(?::[0-9a-fA-F]{1,4}){1,5}|` +
				`[0-9a-fA-F]{1,4}:(?:(?::[0-9a-fA-F]{1,4}){1,6})|` +
				`:(?:(?::[0-9a-fA-F]{1,4}){1,7}|:)` +
				`)\b`,
		),
		normalizer: normalizeFold,
	}
}

func nirDetector() anonymizer.Detector {
	return regexDetector{
		entityType:   anonymizer.EntityNIR,
		priority:     priorityCritical,
		captureGroup: 0,
		pattern: regexp.MustCompile(
			`\b[12]\s?\d{2}\s?(?:0[1-9]|1[0-2]|2[ABab])\s?\d{2}\s?\d{3}\s?\d{3}\s?\d{2}\b`,
		),
		normalizer: keepDigitsAndLetters,
	}
}

func phoneDetector() anonymizer.Detector {
	return regexDetector{
		entityType:   anonymizer.EntityPhone,
		priority:     priorityDefault,
		captureGroup: 0,
		pattern: regexp.MustCompile(
			`(?i)(?:\b0[1-9](?:[\s.\-]?\d{2}){4}\b|\+33[\s.\-]?\(?0?\)?[\s.\-]?[1-9](?:[\s.\-]?\d{2}){4}\b|` +
				`\b00[1-9]\d{0,2}(?:[\s.\-]?\d{2}){4,6}\b)`,
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
		pattern: regexp.MustCompile(
			`(?i)\b(pr[ée]nom|first[ -]?name)\s*[:=]\s*([A-ZÀ-ÖØ-Ý][A-Za-zÀ-ÖØ-öø-ÿ' -]{1,60})`,
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
		pattern: regexp.MustCompile(
			`(?i)\b(nom|last[ -]?name|surname)\s*[:=]\s*([A-ZÀ-ÖØ-Ý][A-Za-zÀ-ÖØ-öø-ÿ' -]{1,80})`,
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
		pattern: regexp.MustCompile(
			`(?i)\b(?:adresse|address)\s*[:=]\s*(` +
				`\d{1,4}\s*(?:bis|ter|quater)?\s+` +
				`(?:rue|avenue|av\.?|boulevard|bd\.?|chemin|impasse|place|all[ée]e|route|quai|cours|square)\s+` +
				`[A-Za-zÀ-ÖØ-öø-ÿ0-9' -]{2,80}` +
				`(?:,\s*)?(?:\b\d{5}\b\s+[A-Za-zÀ-ÖØ-öø-ÿ' -]{2,60})?` +
				`)`,
		),
		normalizer: normalizeFold,
	}
}

func ibanDetector() anonymizer.Detector {
	return regexDetector{
		entityType:   anonymizer.EntityIBAN,
		priority:     priorityCritical,
		captureGroup: 0,
		pattern: regexp.MustCompile(
			`\b[A-Z]{2}\d{2}(?:[ ]?[A-Z0-9]){11,30}\b`,
		),
		normalizer: keepDigitsAndLetters,
	}
}

func creditCardDetector() anonymizer.Detector {
	return regexDetector{
		entityType:   anonymizer.EntityCreditCard,
		priority:     priorityHigh,
		captureGroup: 0,
		pattern: regexp.MustCompile(
			`\b(?:\d[ -]?){13,19}\b`,
		),
		normalizer: keepDigitsAndLetters,
	}
}

func macAddressDetector() anonymizer.Detector {
	return regexDetector{
		entityType:   anonymizer.EntityMACAddress,
		priority:     priorityHigh,
		captureGroup: 0,
		pattern: regexp.MustCompile(
			`\b[0-9a-fA-F]{2}(?::[0-9a-fA-F]{2}){5}\b|\b[0-9a-fA-F]{2}(?:-[0-9a-fA-F]{2}){5}\b`,
		),
		normalizer: keepDigitsAndLetters,
	}
}

func numericIDDetector() anonymizer.Detector {
	return regexDetector{
		entityType:   anonymizer.EntityNumericID,
		priority:     priorityGeneric,
		captureGroup: 0,
		pattern: regexp.MustCompile(
			`\b\d(?:[\s_-]?\d){6,}\b`,
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
		pattern: regexp.MustCompile(
			`(?i)\b(id|user id|client id|customer id|reference|ref|account|ticket)\s*[:=]\s*([A-Z0-9][A-Z0-9_-]{6,})\b`,
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
