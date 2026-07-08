package detectors

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/Korbicorp/klovys99/internal/anonymizer"
	"github.com/dlclark/regexp2"
	"github.com/rs/zerolog/log"
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
	// captureGroupFirstNonEmpty mirrors Gitleaks' fallback behavior for rules
	// without secretGroup: use the first non-empty capture before the full match.
	captureGroup int
	// spanPolicy trims or rejects a captured span after regex matching.
	spanPolicy spanPolicy
	// normalizerPolicy converts the matched value into a stable pseudonymization key.
	normalizerPolicy normalizerPolicy
}

type spanPolicy int

const (
	spanPolicyNone spanPolicy = iota
	spanPolicyTrimConservative
	spanPolicyRequireLettersAndDigits
)

type normalizerPolicy int

const (
	normalizerPolicyFold normalizerPolicy = iota
	normalizerPolicyDigitsAndLetters
	normalizerPolicyPhone
)

const (
	captureGroupFirstNonEmpty = -1

	priorityCritical = 1000
	priorityHigh     = 900
	priorityDefault  = 700
	priorityMedium   = 600
	priorityName     = 500
	priorityGeneric  = 100
	priorityWeakID   = 60
	priorityFallback = 50
)

type Config struct {
	EnableExtra     bool
	EnableGitleaks  bool
	EnablePresidio  bool
	GitleaksURL     string
	GitleaksTimeout time.Duration
	PresidioURL     string
	PresidioBaseURL string
	PresidioTimeout time.Duration
}

type LoadResult struct {
	Detectors         []anonymizer.Detector
	BuiltinDetectors  int
	ExternalDetectors int
	Gitleaks          ExternalLoadMetrics
	Presidio          ExternalLoadMetrics
}

type Service struct {
	config Config
}

func DefaultConfig() Config {
	return Config{
		EnableExtra:    true,
		EnableGitleaks: true,
		EnablePresidio: true,
	}
}

func NewService(config Config) *Service {
	if config == (Config{}) {
		config = DefaultConfig()
	}
	return &Service{config: config}
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
		ibanDetector(),
		creditCardDetector(),
		macAddressDetector(),
		uriSecretDetector(),
		labeledSecretDetector(),
		genericIDDetector(),
		numericIDDetector(),
		referenceIDDetector(),
	)
}

func (s *Service) Load(ctx context.Context) (LoadResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	detectors, err := loadBuiltinDetectors(s.config.EnableExtra)
	if err != nil {
		return LoadResult{}, err
	}
	result := LoadResult{
		Detectors:        detectors,
		BuiltinDetectors: len(detectors),
	}

	if s.config.EnableGitleaks {
		sourceURL := strings.TrimSpace(s.config.GitleaksURL)
		if sourceURL == "" {
			sourceURL = DefaultGitleaksURL
		}
		timeout := s.config.GitleaksTimeout
		if timeout <= 0 {
			timeout = DefaultGitleaksTimeout
		}

		loadCtx, cancel := context.WithTimeout(ctx, timeout)
		loadResult, err := LoadGitleaksRulesWithStats(loadCtx, sourceURL, timeout)
		cancel()
		if err != nil {
			return LoadResult{}, fmt.Errorf("load gitleaks detectors: %w", err)
		}
		result.Detectors = append(result.Detectors, loadResult.Detectors...)
		result.ExternalDetectors += len(loadResult.Detectors)
		result.Gitleaks = loadResult.Metrics
	}

	if s.config.EnablePresidio {
		sourceURL := strings.TrimSpace(s.config.PresidioURL)
		if sourceURL == "" {
			sourceURL = DefaultPresidioURL
		}
		timeout := s.config.PresidioTimeout
		if timeout <= 0 {
			timeout = DefaultPresidioTimeout
		}
		sourceBaseURL := strings.TrimSpace(s.config.PresidioBaseURL)
		if sourceBaseURL == "" {
			sourceBaseURL = defaultPresidioSourceBase
		}

		loadCtx, cancel := context.WithTimeout(ctx, timeout)
		loadResult, err := loadPresidioRulesWithStats(loadCtx, sourceURL, sourceBaseURL, timeout)
		cancel()
		if err != nil {
			return LoadResult{}, fmt.Errorf("load presidio detectors: %w", err)
		}
		result.Detectors = append(result.Detectors, loadResult.Detectors...)
		result.ExternalDetectors += len(loadResult.Detectors)
		result.Presidio = loadResult.Metrics
	}

	return result, nil
}

func loadBuiltinDetectors(includeExtra bool) (detectors []anonymizer.Detector, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = builtinDetectorLoadError(recovered)
		}
	}()

	return Default(includeExtra), nil
}

func builtinDetectorLoadError(recovered any) error {
	return fmt.Errorf("load builtin detectors: %v", recovered)
}

func (d regexDetector) FindAll(text string) []anonymizer.Match {
	runeToByte := runeToByteOffsets(text)

	match, err := d.pattern.FindStringMatch(text)
	if err != nil {
		d.logMatchError(err)
		return nil
	}

	var matches []anonymizer.Match
	for match != nil {
		start, end, ok := captureByteRange(match, d.captureGroup, runeToByte)
		if !ok {
			next, nextErr := d.pattern.FindNextMatch(match)
			if nextErr != nil {
				d.logMatchError(nextErr)
				return matches
			}
			match = next
			continue
		}
		if d.spanPolicy != spanPolicyNone {
			start, end = d.adjustSpan(text, start, end)
			if start >= end {
				next, nextErr := d.pattern.FindNextMatch(match)
				if nextErr != nil {
					d.logMatchError(nextErr)
					return matches
				}
				match = next
				continue
			}
		}

		matches = append(matches, anonymizer.Match{
			Start:      start,
			End:        end,
			Type:       d.entityType,
			Priority:   d.priority,
			Normalized: d.normalize(text[start:end]),
		})

		match, err = d.pattern.FindNextMatch(match)
		if err != nil {
			d.logMatchError(err)
			return matches
		}
	}

	return matches
}

func (d regexDetector) logMatchError(err error) {
	log.Error().
		Err(err).
		Str("entity_type", string(d.entityType)).
		Msg("regex detector failed")
}

func (d regexDetector) adjustSpan(text string, start, end int) (int, int) {
	switch d.spanPolicy {
	case spanPolicyTrimConservative:
		return trimConservativeValueSpan(text, start, end)
	case spanPolicyRequireLettersAndDigits:
		return requireLettersAndDigitsSpan(text, start, end)
	default:
		return start, end
	}
}

func (d regexDetector) normalize(value string) string {
	switch d.normalizerPolicy {
	case normalizerPolicyDigitsAndLetters:
		return keepDigitsAndLetters(value)
	case normalizerPolicyPhone:
		return normalizePhone(value)
	default:
		return normalizeFold(value)
	}
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
	if captureGroup == captureGroupFirstNonEmpty {
		groups := match.Groups()
		for index := 1; index < len(groups); index++ {
			if groups[index].Length > 0 {
				return groupByteRange(&groups[index], runeToByte)
			}
		}
		return groupByteRange(match.GroupByNumber(0), runeToByte)
	}

	group := match.GroupByNumber(captureGroup)
	if group == nil || group.Length == 0 && captureGroup > 0 {
		return 0, 0, false
	}

	return groupByteRange(group, runeToByte)
}

func groupByteRange(group *regexp2.Group, runeToByte []int) (int, int, bool) {
	if group == nil {
		return 0, 0, false
	}

	start := group.Index
	end := group.Index + group.Length
	if start < 0 || end < start || end >= len(runeToByte) {
		return 0, 0, false
	}

	return runeToByte[start], runeToByte[end], true
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
		normalizerPolicy: normalizerPolicyFold,
	}
}

func ipDetector() anonymizer.Detector {
	const (
		ipLeftDelimiter   = `(?:^|[^0-9A-Za-z_.-])`
		ipv4RightBoundary = `(?=$|[^0-9A-Za-z_.-])`
		ipv6RightBoundary = `(?=$|[^0-9A-Za-z_:.-])`
		ipv4Pattern       = `(?:(?:25[0-5]|2[0-4]\d|1?\d?\d)\.){3}(?:25[0-5]|2[0-4]\d|1?\d?\d)`
		// Keep compressed IPv6 forms, but exclude bare "::" to avoid code-path false positives.
		ipv6Pattern = `(?:` +
			`(?:[0-9a-fA-F]{1,4}:){7}[0-9a-fA-F]{1,4}|` +
			`(?:[0-9a-fA-F]{1,4}:){1,7}:|` +
			`(?:[0-9a-fA-F]{1,4}:){1,6}:[0-9a-fA-F]{1,4}|` +
			`(?:[0-9a-fA-F]{1,4}:){1,5}(?::[0-9a-fA-F]{1,4}){1,2}|` +
			`(?:[0-9a-fA-F]{1,4}:){1,4}(?::[0-9a-fA-F]{1,4}){1,3}|` +
			`(?:[0-9a-fA-F]{1,4}:){1,3}(?::[0-9a-fA-F]{1,4}){1,4}|` +
			`(?:[0-9a-fA-F]{1,4}:){1,2}(?::[0-9a-fA-F]{1,4}){1,5}|` +
			`[0-9a-fA-F]{1,4}:(?:(?::[0-9a-fA-F]{1,4}){1,6})|` +
			`:(?::[0-9a-fA-F]{1,4}){1,7}` +
			`)`
	)

	return regexDetector{
		entityType:   anonymizer.EntityIP,
		priority:     priorityHigh,
		captureGroup: 1,
		pattern: regexp2.MustCompile(
			ipLeftDelimiter+`(`+ipv4Pattern+ipv4RightBoundary+`|`+ipv6Pattern+ipv6RightBoundary+`)`,
			regexp2.RE2,
		),
		normalizerPolicy: normalizerPolicyFold,
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
		normalizerPolicy: normalizerPolicyDigitsAndLetters,
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
		normalizerPolicy: normalizerPolicyPhone,
	}
}

func firstNameDetector() anonymizer.Detector {
	return regexDetector{
		entityType:   anonymizer.EntityFirstName,
		priority:     priorityName,
		captureGroup: 2,
		spanPolicy:   spanPolicyTrimConservative,
		pattern: regexp2.MustCompile(
			`(?i)\b(pr[ée]nom|first[ -]?name)\s*[:=]\s*([A-ZÀ-ÖØ-Ý][A-Za-zÀ-ÖØ-öø-ÿ' -]{1,60})`,
			regexp2.RE2,
		),
		normalizerPolicy: normalizerPolicyFold,
	}
}

func lastNameDetector() anonymizer.Detector {
	return regexDetector{
		entityType:   anonymizer.EntityLastName,
		priority:     priorityName,
		captureGroup: 2,
		spanPolicy:   spanPolicyTrimConservative,
		pattern: regexp2.MustCompile(
			`(?i)\b(nom|last[ -]?name|surname)\s*[:=]\s*([A-ZÀ-ÖØ-Ý][A-Za-zÀ-ÖØ-öø-ÿ' -]{1,80})`,
			regexp2.RE2,
		),
		normalizerPolicy: normalizerPolicyFold,
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
		normalizerPolicy: normalizerPolicyFold,
	}
}

func addressDetector() anonymizer.Detector {
	return regexDetector{
		entityType:   anonymizer.EntityAddress,
		priority:     priorityDefault,
		captureGroup: 1,
		spanPolicy:   spanPolicyTrimConservative,
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
		normalizerPolicy: normalizerPolicyFold,
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
		normalizerPolicy: normalizerPolicyFold,
	}
}

func birthDateDetector() anonymizer.Detector {
	return regexDetector{
		entityType:   anonymizer.EntityDate,
		priority:     priorityMedium,
		captureGroup: 1,
		spanPolicy:   spanPolicyTrimConservative,
		pattern: regexp2.MustCompile(
			`(?i)\b(?:date\s+de\s+naissance|date\s+of\s+birth|birth\s*date|dob|n(?:é|ée|e)\s+le|born\s+on)\s*[:=]?\s*(`+
				`(?:0?[1-9]|[12]\d|3[01])[\/.\-](?:0?[1-9]|1[0-2])[\/.\-](?:19|20)\d{2}|`+
				`(?:19|20)\d{2}-(?:0?[1-9]|1[0-2])-(?:0?[1-9]|[12]\d|3[01])|`+
				`(?:0?[1-9]|[12]\d|3[01])\s+(?:janvier|février|fevrier|mars|avril|mai|juin|juillet|août|aout|septembre|octobre|novembre|décembre|decembre)\s+(?:19|20)\d{2}`+
				`)`,
			regexp2.RE2,
		),
		normalizerPolicy: normalizerPolicyFold,
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
		normalizerPolicy: normalizerPolicyFold,
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
		normalizerPolicy: normalizerPolicyDigitsAndLetters,
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
		normalizerPolicy: normalizerPolicyDigitsAndLetters,
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
		normalizerPolicy: normalizerPolicyDigitsAndLetters,
	}
}

func uriSecretDetector() anonymizer.Detector {
	return regexDetector{
		entityType:   anonymizer.EntitySecret,
		priority:     priorityDefault,
		captureGroup: 1,
		pattern: regexp2.MustCompile(
			`(?i)\b[a-z][a-z0-9+.-]*://(?:[^:@\s/?#]+)?:([^@\s/?#]+)@`,
			regexp2.RE2,
		),
		normalizerPolicy: normalizerPolicyFold,
	}
}

func labeledSecretDetector() anonymizer.Detector {
	return regexDetector{
		entityType:   anonymizer.EntitySecret,
		priority:     priorityDefault,
		captureGroup: 1,
		pattern: regexp2.MustCompile(
			`(?i)\b(?:api[_-]?key|secret|token|password|passwd|pwd|access[_-]?token|refresh[_-]?token|private[_-]?key|client[_-]?secret)\s*[:=]\s*["']?([a-z0-9][a-z0-9._~+/=-]{10,})["']?`,
			regexp2.RE2,
		),
		normalizerPolicy: normalizerPolicyFold,
	}
}

func genericIDDetector() anonymizer.Detector {
	return regexDetector{
		entityType:   anonymizer.EntityGenericID,
		priority:     priorityWeakID,
		captureGroup: 1,
		spanPolicy:   spanPolicyRequireLettersAndDigits,
		pattern: regexp2.MustCompile(
			`(?i)(?:^|[\s=@&])([a-z0-9]{6,})(?=$|[\s=@&])`,
			regexp2.RE2,
		),
		normalizerPolicy: normalizerPolicyDigitsAndLetters,
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
		normalizerPolicy: normalizerPolicyDigitsAndLetters,
	}
}

func referenceIDDetector() anonymizer.Detector {
	return regexDetector{
		entityType:   anonymizer.EntityReferenceID,
		priority:     priorityGeneric,
		captureGroup: 2,
		spanPolicy:   spanPolicyRequireLettersAndDigits,
		pattern: regexp2.MustCompile(
			`(?i)\b(id|user id|client id|customer id|reference|ref|account|ticket)\s*[:=]\s*([A-Z0-9][A-Z0-9_-]{6,})\b`,
			regexp2.RE2,
		),
		normalizerPolicy: normalizerPolicyDigitsAndLetters,
	}
}

func requireLettersAndDigitsSpan(text string, start, end int) (int, int) {
	hasLetter := false
	hasDigit := false
	for _, r := range text[start:end] {
		hasLetter = hasLetter || unicode.IsLetter(r)
		hasDigit = hasDigit || unicode.IsDigit(r)
	}
	if !hasLetter || !hasDigit {
		return start, start
	}

	return start, end
}

func normalizeFold(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func trimConservativeValueSpan(text string, start, end int) (int, int) {
	// Conservative name/address patterns intentionally require a label, but their
	// value capture can otherwise consume the next labelled field on the same line.
	for start < end && unicode.IsSpace(rune(text[start])) {
		start++
	}
	for start < end && unicode.IsSpace(rune(text[end-1])) {
		end--
	}

	value := strings.ToLower(text[start:end])
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

func normalizePhone(value string) string {
	var builder strings.Builder
	for _, r := range value {
		if unicode.IsDigit(r) || r == '+' {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func keepDigitsAndLetters(value string) string {
	var builder strings.Builder
	for _, r := range value {
		if unicode.IsDigit(r) || unicode.IsLetter(r) {
			builder.WriteRune(unicode.ToUpper(r))
		}
	}
	return builder.String()
}
