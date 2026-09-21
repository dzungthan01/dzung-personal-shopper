package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// coleta mirrors a real Shopify product: one colour, sizes as separate variants.
var coleta = Snapshot{Variants: []Variant{
	{Name: "Light Pistachio / S", Size: "S", Available: true},
	{Name: "Light Pistachio / M", Size: "M", Available: false},
}}

// frame has two inseams per size, so a bare size matches more than one variant.
var frame = Snapshot{Variants: []Variant{
	{Name: `Ridgeway / 30" / 28`, Size: "28", Available: true},
	{Name: `Ridgeway / 32" / 28`, Size: "28", Available: false},
}}

func TestVariantMatches(t *testing.T) {
	variant := Variant{Name: "Light Pistachio / M", Size: "M"}
	assert.True(t, variant.Matches("Light Pistachio / M"))
	assert.True(t, variant.Matches("light pistachio / m"), "matching ignores case")
	assert.True(t, variant.Matches(" M "), "a bare size matches, whitespace ignored")
	assert.False(t, variant.Matches("S"))
	assert.False(t, variant.Matches(""), "empty never matches")
}

func TestVariantInStock(t *testing.T) {
	assert.True(t, VariantInStock(coleta.Variants, "S"))
	assert.False(t, VariantInStock(coleta.Variants, "M"), "M exists but is sold out")
	assert.True(t, VariantInStock(frame.Variants, "28"), "any matching inseam in stock counts")
}

func TestResolveVariant(t *testing.T) {
	tests := []struct {
		name     string
		snapshot Snapshot
		typed    string
		want     string
	}{
		{"bare size normalised to full name", coleta, "m", "Light Pistachio / M"},
		{"full name kept", coleta, "Light Pistachio / S", "Light Pistachio / S"},
		{"ambiguous size kept as typed", frame, "28", "28"},
		{"nothing asked for", coleta, "", ""},
		{"no variants to check against", Snapshot{}, "IT 38", "IT 38"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := testCase.snapshot.ResolveVariant(testCase.typed)
			require.NoError(t, err)
			assert.Equal(t, testCase.want, got)
		})
	}
}

func TestResolveVariantRejectsUnknown(t *testing.T) {
	_, err := coleta.ResolveVariant("XL")
	require.ErrorIs(t, err, ErrUnknownVariant)
	assert.Contains(t, err.Error(), `"Light Pistachio / S"`, "the error lists the real options so the caller can retry")
}

func TestAvailableVariants(t *testing.T) {
	assert.Equal(t, []string{"Light Pistachio / S"}, coleta.AvailableVariants())
	assert.Equal(t, []string{"28"}, Snapshot{Variants: []Variant{{Size: "28", Available: true}}}.AvailableVariants(),
		"falls back to size when the store gives no name")
}

func TestVariantMatchesIgnoresLeadingLabel(t *testing.T) {
	assert.True(t, Variant{Size: "38"}.Matches("IT 38"))
	assert.True(t, Variant{Size: "IT 38"}.Matches("38"))
	assert.True(t, Variant{Size: "IT 38"}.Matches("it38"), "spacing and case ignored")
	assert.False(t, Variant{Size: "40"}.Matches("IT 38"))
	assert.False(t, Variant{Name: "US Navy"}.Matches("Navy"), "only a word before a number is dropped")
}

func TestFormatMoney(t *testing.T) {
	assert.Equal(t, "$173.00", FormatMoney(17300, "USD"))
	assert.Equal(t, "€9.05", FormatMoney(905, "eur"))
	assert.Equal(t, "1450.00 SEK", FormatMoney(145000, "SEK"), "no symbol falls back to the code")
}

func TestParseMoney(t *testing.T) {
	accepted := map[string]int64{
		"173.00":    17300,
		"173":       17300,
		"495.5":     49550, // one decimal digit is tenths, not hundredths
		"1,495.00":  149500,
		"1.495,00":  149500, // the European reading of the same price
		"1.495":     149500, // a lone three-digit group is thousands
		"1 495,00":  149500,
		"1 495,00":  149500, // stores often group with a non-breaking space
		"€495,00":   49500,
		"$1,495.99": 149599,
		".50":       50,
		"0":         0,
		"  173.00 ": 17300,
	}
	for price, want := range accepted {
		cents, err := ParseMoney(price)
		require.NoError(t, err, price)
		assert.Equal(t, want, cents, price)
	}

	rejected := []string{
		"", "   ", "abc", "495 EUR", "-5.00",
		"495.1234", // neither a decimal nor a thousands group
		"1,49.00",  // a two-digit thousands group
		"1,4950.00",
		"1,,495.00",
		",495",
		"12.34.56",
	}
	for _, price := range rejected {
		_, err := ParseMoney(price)
		assert.Error(t, err, "%q must not be guessed at", price)
	}
}

// ParseMoney is the inverse of FormatMoney wherever the currency has a symbol.
func TestParseMoneyRoundTrip(t *testing.T) {
	for _, cents := range []int64{0, 5, 99, 100, 17300, 24800, 149599} {
		for _, currency := range []string{"USD", "EUR", "GBP"} {
			parsed, err := ParseMoney(FormatMoney(cents, currency))
			require.NoError(t, err, FormatMoney(cents, currency))
			assert.Equal(t, cents, parsed, currency)
		}
	}
}
