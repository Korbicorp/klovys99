package detectors

import (
	"strings"
	"testing"

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
		"[LAST_NAME_1]",
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

func TestConservativeLabelsDoNotConsumeNextField(t *testing.T) {
	output, _ := anonymize(t, "Nom: Dupont Email: alice@example.com", false)

	if got, want := output, "Nom: [LAST_NAME_1] Email: [EMAIL_1]"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
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
