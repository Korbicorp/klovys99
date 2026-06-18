package detectors

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Korbicorp/klovis/internal/anonymizer"
)

func anonymize(t *testing.T, input string, includeExtra bool) (string, anonymizer.Result) {
	t.Helper()

	engine := anonymizer.New(Default(includeExtra))
	output, result := engine.Anonymize([]byte(input))
	return string(output), result
}

func TestDefaultDetectorsAnonymizeCoreEntities(t *testing.T) {
	input := strings.Join([]string{
		"Email: Alice.Example+test@Example.COM",
		"IP: 192.168.1.42 and 2001:db8::1",
		"Phone: 06 12 34 56 78 or +33 6 12 34 56 78",
		"NIR: 1 84 12 75 123 456 78",
		"Prénom: Jean",
		"Nom: Dupont",
		"Je m'appelle Alice",
		"Date de naissance: 14 mars 1988",
		"Groupe sanguin O+",
		"Adresse: 10 rue de la Paix, 75002 Paris",
	}, "\n")

	output, result := anonymize(t, input, false)

	for _, token := range []string{
		"[EMAIL_1]",
		"[IP_1]",
		"[IP_2]",
		"[PHONE_1]",
		"[PHONE_2]",
		"[NIR_1]",
		"[FIRST_NAME_1]",
		"[NAME_1]",
		"[NAME_2]",
		"[BIRTH_DATE_1]",
		"[BLOOD_TYPE_1]",
		"[ADDRESS_1]",
	} {
		if !strings.Contains(output, token) {
			t.Fatalf("output does not contain %s:\n%s", token, output)
		}
	}
	if got, want := result.Stats[anonymizer.EntityPhone].Count, 2; got != want {
		t.Fatalf("phone count = %d, want %d", got, want)
	}
}

func TestNameDetectionRequiresExplicitLabel(t *testing.T) {
	output, result := anonymize(t, "Jean Dupont a envoye un message.", false)

	if got, want := output, "Jean Dupont a envoye un message."; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if len(result.Stats) != 0 {
		t.Fatalf("stats = %#v, want empty", result.Stats)
	}
}

func TestContextualNameDetectorSupportsFrenchAndEnglishContexts(t *testing.T) {
	output, result := anonymize(t, "Je m'appelle Jean-Pierre.\nHello this is Alice\n**Nom :** JULIEN MOREAU", false)

	if !strings.Contains(output, "Je m'appelle [NAME_1].") {
		t.Fatalf("french contextual name was not anonymized: %s", output)
	}
	if !strings.Contains(output, "Hello this is [NAME_2]") {
		t.Fatalf("english contextual name was not anonymized: %s", output)
	}
	if !strings.Contains(output, "**Nom :** [NAME_3]") {
		t.Fatalf("markdown label name was not anonymized: %s", output)
	}
	if got, want := result.Stats[anonymizer.EntityName].Count, 3; got != want {
		t.Fatalf("name count = %d, want %d", got, want)
	}
}

func TestContextualNameDetectorRequiresExplicitContext(t *testing.T) {
	output, result := anonymize(t, "Alice a envoyé le document à Bob.", false)

	if got, want := output, "Alice a envoyé le document à Bob."; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if _, ok := result.Stats[anonymizer.EntityName]; ok {
		t.Fatalf("name should not be counted without contextual trigger: %#v", result.Stats)
	}
}

func TestConservativeLabelsDoNotConsumeNextField(t *testing.T) {
	output, _ := anonymize(t, "Nom: Dupont Email: alice@example.com", false)

	if got, want := output, "Nom: [NAME_1] Email: [EMAIL_1]"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestBirthDateDetectionRequiresBirthContext(t *testing.T) {
	output, result := anonymize(t, "Il a rendez-vous le 14 mars 1988.", false)

	if got, want := output, "Il a rendez-vous le 14 mars 1988."; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if _, ok := result.Stats[anonymizer.EntityBirthDate]; ok {
		t.Fatalf("birth date should not be counted without explicit context: %#v", result.Stats)
	}
}

func TestBirthDateDetectorSupportsNumericAndTextualFormats(t *testing.T) {
	output, result := anonymize(t, "Date de naissance: 12/01/1988\nNée le 3 février 1991", false)

	if !strings.Contains(output, "Date de naissance: [BIRTH_DATE_1]") {
		t.Fatalf("numeric birth date was not anonymized: %s", output)
	}
	if !strings.Contains(output, "Née le [BIRTH_DATE_2]") {
		t.Fatalf("textual birth date was not anonymized: %s", output)
	}
	if got, want := result.Stats[anonymizer.EntityBirthDate].Count, 2; got != want {
		t.Fatalf("birth date count = %d, want %d", got, want)
	}
}

func TestBloodTypeDetectorSupportsShortAndLongFormats(t *testing.T) {
	output, result := anonymize(t, "Groupe sanguin O+\nBlood type is AB negative", false)

	if !strings.Contains(output, "Groupe sanguin [BLOOD_TYPE_1]") {
		t.Fatalf("short blood type was not anonymized: %s", output)
	}
	if !strings.Contains(output, "Blood type is [BLOOD_TYPE_2]") {
		t.Fatalf("long blood type was not anonymized: %s", output)
	}
	if got, want := result.Stats[anonymizer.EntityBloodType].Count, 2; got != want {
		t.Fatalf("blood type count = %d, want %d", got, want)
	}
}

func TestBloodTypeDetectorRequiresMedicalContext(t *testing.T) {
	output, result := anonymize(t, "Mon ancienne note était A+ au lycée.", false)

	if got, want := output, "Mon ancienne note était A+ au lycée."; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if _, ok := result.Stats[anonymizer.EntityBloodType]; ok {
		t.Fatalf("blood type should not be counted without context: %#v", result.Stats)
	}
}

func TestAddressDetectorSupportsResidenceComplements(t *testing.T) {
	output, result := anonymize(t, "Adresse: 15 résidence des Lilas, bâtiment B, 13008 Marseille", false)

	if got, want := output, "Adresse: [ADDRESS_1]"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if got, want := result.Stats[anonymizer.EntityAddress].Count, 1; got != want {
		t.Fatalf("address count = %d, want %d", got, want)
	}
}

func TestFrenchAddressDetectorSupportsUnlabelledAddresses(t *testing.T) {
	output, result := anonymize(t, "Rendez-vous au 14 Rue de la République, 69002 Lyon demain.", false)

	if got, want := output, "Rendez-vous au [ADDRESS_1] demain."; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if got, want := result.Stats[anonymizer.EntityAddress].Count, 1; got != want {
		t.Fatalf("address count = %d, want %d", got, want)
	}
}

func TestEmailWinsOverExtraURLOverlap(t *testing.T) {
	output, result := anonymize(t, "Contact: https://example.com/a?email=alice@example.com", true)

	if strings.Contains(output, "alice@example.com") {
		t.Fatalf("email was not anonymized: %s", output)
	}
	if got, want := result.Stats[anonymizer.EntityEmail].Count, 1; got != want {
		t.Fatalf("email count = %d, want %d", got, want)
	}
	if _, ok := result.Stats[anonymizer.EntityURL]; ok {
		t.Fatalf("overlapping lower priority URL should not be counted: %#v", result.Stats)
	}
}

func TestExtraDetectorsCanBeDisabled(t *testing.T) {
	input := "URL https://example.com IBAN FR76 3000 6000 0112 3456 7890 189 MAC aa:bb:cc:dd:ee:ff ID 1234567 Ref: ABC12345"

	withExtra, _ := anonymize(t, input, true)
	withoutExtra, _ := anonymize(t, input, false)

	if strings.Contains(withExtra, "https://example.com") ||
		strings.Contains(withExtra, "FR76 3000") ||
		strings.Contains(withExtra, "aa:bb:cc") ||
		strings.Contains(withExtra, "1234567") ||
		strings.Contains(withExtra, "ABC12345") {
		t.Fatalf("extra entities were not anonymized: %s", withExtra)
	}
	if withoutExtra != input {
		t.Fatalf("without extra = %q, want original %q", withoutExtra, input)
	}
}

func TestGenericIDDetectorsUseLowPriority(t *testing.T) {
	input := "NIR: 1 84 12 75 123 456 78 Other ID 1234567 Ref: ABC12345"

	output, result := anonymize(t, input, true)

	for _, token := range []string{"[NIR_1]", "[NUMERIC_ID_1]", "[REFERENCE_ID_1]"} {
		if !strings.Contains(output, token) {
			t.Fatalf("output does not contain %s:\n%s", token, output)
		}
	}
	if got, want := result.Stats[anonymizer.EntityNIR].Count, 1; got != want {
		t.Fatalf("nir count = %d, want %d", got, want)
	}
	if got, want := result.Stats[anonymizer.EntityNumericID].Count, 1; got != want {
		t.Fatalf("numeric id count = %d, want %d", got, want)
	}
}

func TestReferenceIDRequiresLettersAndDigits(t *testing.T) {
	output, result := anonymize(t, "Ticket: ABCDEFGH Ref: 12345678 Account: AB123456", true)

	if strings.Contains(output, "AB123456") {
		t.Fatalf("mixed reference id was not anonymized: %s", output)
	}
	if !strings.Contains(output, "Ticket: ABCDEFGH") {
		t.Fatalf("letters-only reference id should stay visible: %s", output)
	}
	if got, want := result.Stats[anonymizer.EntityReferenceID].Count, 1; got != want {
		t.Fatalf("reference id count = %d, want %d", got, want)
	}
}

func TestDetectorsFromGitleaksRulesUseSecretGroup(t *testing.T) {
	loaded, err := detectorsFromGitleaksRules([]gitleaksRule{
		{
			ID:          "demo-secret",
			Regex:       `token=([A-Z0-9]{6})`,
			SecretGroup: 1,
		},
	})
	if err != nil {
		t.Fatalf("detectorsFromGitleaksRules returned error: %v", err)
	}

	engine := anonymizer.New(loaded)
	output, result := engine.Anonymize([]byte("token=ABC123\n"))

	if got, want := string(output), "token=[SECRET_1]\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if got, want := result.Stats[anonymizer.EntitySecret].Count, 1; got != want {
		t.Fatalf("secret count = %d, want %d", got, want)
	}
}

func TestDetectorsFromGitleaksRulesSkipPathScopedAndRegexlessRules(t *testing.T) {
	loaded, err := detectorsFromGitleaksRules([]gitleaksRule{
		{ID: "path-only", Path: `(?i)\.pem$`, Regex: `foo`},
		{ID: "no-regex"},
	})
	if err != nil {
		t.Fatalf("detectorsFromGitleaksRules returned error: %v", err)
	}
	if len(loaded) != 0 {
		t.Fatalf("loaded %d detectors, want 0", len(loaded))
	}
}

func TestLoadExternalRulesParsesGitleaksToml(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte(`
title = "gitleaks config"

[[rules]]
id = "demo-secret"
regex = 'token=([A-Z0-9]{6})'
secretGroup = 1
`))
	}))
	defer server.Close()

	loaded, err := LoadExternalRules(context.Background(), server.Client(), server.URL)
	if err != nil {
		t.Fatalf("LoadExternalRules returned error: %v", err)
	}

	engine := anonymizer.New(loaded)
	output, _ := engine.Anonymize([]byte("token=ABC123\n"))
	if got, want := string(output), "token=[SECRET_1]\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestLoadExternalRulesUsesDiskCacheBetweenRuns(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		_, _ = writer.Write([]byte(`
title = "gitleaks config"

[[rules]]
id = "demo-secret"
regex = 'token=([A-Z0-9]{6})'
secretGroup = 1
`))
	}))
	defer server.Close()

	cacheDir := t.TempDir()
	first, err := loadExternalRulesWithStats(context.Background(), server.Client(), server.URL, cacheDir, time.Hour)
	if err != nil {
		t.Fatalf("first loadExternalRulesWithStats returned error: %v", err)
	}
	second, err := loadExternalRulesWithStats(context.Background(), server.Client(), server.URL, cacheDir, time.Hour)
	if err != nil {
		t.Fatalf("second loadExternalRulesWithStats returned error: %v", err)
	}

	if requests != 1 {
		t.Fatalf("server handled %d requests, want 1", requests)
	}
	if got, want := first.Metrics.CacheMisses, 1; got != want {
		t.Fatalf("first cache misses = %d, want %d", got, want)
	}
	if got, want := second.Metrics.CacheHits, 1; got != want {
		t.Fatalf("second cache hits = %d, want %d", got, want)
	}
	if len(first.Detectors) == 0 || len(second.Detectors) == 0 {
		t.Fatalf("expected detectors from both loads, got %d and %d", len(first.Detectors), len(second.Detectors))
	}
}

func TestDetectorsFromPresidioSourceParsesLiteralPatterns(t *testing.T) {
	loaded, err := detectorsFromPresidioSource("EmailRecognizer", `
class EmailRecognizer(PatternRecognizer):
    PATTERNS = [
        Pattern("Email", r"\bfoo@example\.com\b", 0.5),
    ]

    def __init__(
        self,
        supported_entity: str = "EMAIL_ADDRESS",
    ):
        pass
`)
	if err != nil {
		t.Fatalf("detectorsFromPresidioSource returned error: %v", err)
	}

	engine := anonymizer.New(loaded)
	output, result := engine.Anonymize([]byte("foo@example.com\n"))
	if got, want := string(output), "[EMAIL_1]\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if got, want := result.Stats[anonymizer.EntityEmail].Count, 1; got != want {
		t.Fatalf("email count = %d, want %d", got, want)
	}
}

func TestDetectorsFromPresidioSourceResolvesStringConstants(t *testing.T) {
	loaded, err := detectorsFromPresidioSource("UrlRecognizer", `
BASE_URL_REGEX = r"example\.com"
class UrlRecognizer(PatternRecognizer):
    PATTERNS = [
        Pattern("URL", "(?i)" + BASE_URL_REGEX, 0.5),
    ]

    def __init__(
        self,
        supported_entity: str = "URL",
    ):
        pass
`)
	if err != nil {
		t.Fatalf("detectorsFromPresidioSource returned error: %v", err)
	}

	engine := anonymizer.New(loaded)
	output, result := engine.Anonymize([]byte("example.com\n"))
	if got, want := string(output), "[URL_1]\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if got, want := result.Stats[anonymizer.EntityURL].Count, 1; got != want {
		t.Fatalf("url count = %d, want %d", got, want)
	}
}

func TestLoadExternalPresidioRulesUsesDiskCacheBetweenRuns(t *testing.T) {
	requests := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests[request.URL.Path]++
		switch request.URL.Path {
		case "/default_recognizers.yaml":
			_, _ = writer.Write([]byte(`
recognizers:
  - name: EmailRecognizer
    type: predefined
`))
		case "/email_recognizer.py":
			_, _ = writer.Write([]byte(`
class EmailRecognizer(PatternRecognizer):
    PATTERNS = [
        Pattern("Email", r"\bfoo@example\.com\b", 0.5),
    ]

    def __init__(
        self,
        supported_entity: str = "EMAIL_ADDRESS",
    ):
        pass
`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	cacheDir := t.TempDir()
	first, err := loadExternalPresidioRulesWithStats(
		context.Background(),
		server.Client(),
		server.URL+"/default_recognizers.yaml",
		server.URL,
		cacheDir,
		time.Hour,
	)
	if err != nil {
		t.Fatalf("first loadExternalPresidioRulesWithStats returned error: %v", err)
	}
	second, err := loadExternalPresidioRulesWithStats(
		context.Background(),
		server.Client(),
		server.URL+"/default_recognizers.yaml",
		server.URL,
		cacheDir,
		time.Hour,
	)
	if err != nil {
		t.Fatalf("second loadExternalPresidioRulesWithStats returned error: %v", err)
	}

	if got, want := requests["/default_recognizers.yaml"], 1; got != want {
		t.Fatalf("config requests = %d, want %d", got, want)
	}
	if got, want := requests["/email_recognizer.py"], 1; got != want {
		t.Fatalf("source requests = %d, want %d", got, want)
	}
	if got, want := first.Metrics.CacheMisses, 2; got != want {
		t.Fatalf("first cache misses = %d, want %d", got, want)
	}
	if got, want := second.Metrics.CacheHits, 2; got != want {
		t.Fatalf("second cache hits = %d, want %d", got, want)
	}
	if len(first.Detectors) == 0 || len(second.Detectors) == 0 {
		t.Fatalf("expected detectors from both loads, got %d and %d", len(first.Detectors), len(second.Detectors))
	}
}

func TestDetectorsFromPresidioSourceSupportsRawStringsWithEscapedQuotes(t *testing.T) {
	loaded, err := detectorsFromPresidioSource("UrlRecognizer", `
BASE_URL_REGEX = r"example\.com"
class UrlRecognizer(PatternRecognizer):
    PATTERNS = [
        Pattern("Quoted URL", r'(?i)["\'](https?://' + BASE_URL_REGEX + r')["\']', 0.6),
    ]

    def __init__(
        self,
        supported_entity: str = "URL",
    ):
        pass
`)
	if err != nil {
		t.Fatalf("detectorsFromPresidioSource returned error: %v", err)
	}

	engine := anonymizer.New(loaded)
	output, result := engine.Anonymize([]byte(`"https://example.com"` + "\n"))
	if got, want := string(output), "[URL_1]\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if got, want := result.Stats[anonymizer.EntityURL].Count, 1; got != want {
		t.Fatalf("url count = %d, want %d", got, want)
	}
}

func TestDetectorsFromPresidioSourceSkipsUnknownExpressions(t *testing.T) {
	loaded, err := detectorsFromPresidioSource("UrlRecognizer", `
class UrlRecognizer(PatternRecognizer):
    PATTERNS = [
        Pattern("URL", "(?i)" + UNKNOWN_REGEX, 0.5),
    ]

    def __init__(
        self,
        supported_entity: str = "URL",
    ):
        pass
`)
	if err != nil {
		t.Fatalf("detectorsFromPresidioSource returned error: %v", err)
	}
	if len(loaded) != 0 {
		t.Fatalf("loaded %d detectors, want 0", len(loaded))
	}
}

func TestLoadExternalPresidioRulesParsesYamlAndSources(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/default_recognizers.yaml":
			_, _ = writer.Write([]byte(`
recognizers:
  - name: EmailRecognizer
    type: predefined
  - name: PhoneRecognizer
    type: predefined
`))
		case "/email_recognizer.py":
			_, _ = writer.Write([]byte(`
class EmailRecognizer(PatternRecognizer):
    PATTERNS = [
        Pattern("Email", r"\bfoo@example\.com\b", 0.5),
    ]

    def __init__(
        self,
        supported_entity: str = "EMAIL_ADDRESS",
    ):
        pass
`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	loaded, err := LoadExternalPresidioRules(
		context.Background(),
		server.Client(),
		server.URL+"/default_recognizers.yaml",
		server.URL+"/",
	)
	if err != nil {
		t.Fatalf("LoadExternalPresidioRules returned error: %v", err)
	}

	engine := anonymizer.New(loaded)
	output, _ := engine.Anonymize([]byte("foo@example.com\n"))
	if got, want := string(output), "[EMAIL_1]\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func BenchmarkAnonymizeSmall(b *testing.B) {
	benchmarkAnonymize(b, "Contact Nom: Dupont Email: alice@example.com Tel: 06 12 34 56 78\n")
}

func BenchmarkAnonymizeMedium(b *testing.B) {
	benchmarkAnonymize(b, strings.Repeat("Adresse: 10 rue de la Paix, 75002 Paris Email: alice@example.com IP 192.168.1.42\n", 100))
}

func BenchmarkAnonymizeLarge(b *testing.B) {
	benchmarkAnonymize(b, strings.Repeat("Prénom: Jean Nom: Dupont NIR: 1 84 12 75 123 456 78 Phone +33 6 12 34 56 78\n", 5000))
}

func benchmarkAnonymize(b *testing.B, input string) {
	b.Helper()

	engine := anonymizer.New(Default(true))
	data := []byte(input)
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))

	for i := 0; i < b.N; i++ {
		_, _ = engine.Anonymize(data)
	}
}
