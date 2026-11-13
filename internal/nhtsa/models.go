package nhtsa

import (
	"regexp"
	"strings"
	"unicode"
)

// maxCandidates bounds the model names tried per vehicle, so a family with
// many NHTSA model names costs a bounded number of requests.
const maxCandidates = 12

var (
	lettersThenDigits = regexp.MustCompile(`^([A-Za-z]+)([0-9]+)$`)
	lettersOnly       = regexp.MustCompile(`^[A-Za-z]+$`)
	familySuffix      = regexp.MustCompile(`(?i)[\s-]*(class|series)$`)
)

// ModelCandidates lists the model names to try against NHTSA's recall lookup,
// most specific first and without duplicates: the stored model, the trim, their
// spacing variants, then those of products (NHTSA's model names for the make
// and year) in the same family as model.
func ModelCandidates(model, trim string, products []string) []string {
	var names []string
	seen := map[string]bool{}
	add := func(name string) {
		name = strings.Join(strings.Fields(name), " ")
		if key := strings.ToLower(name); name != "" && !seen[key] && len(names) < maxCandidates {
			seen[key] = true
			names = append(names, name)
		}
	}
	for _, name := range []string{model, trim, withoutDrivetrain(trim)} {
		add(name)
		add(spacedVariant(name))
		add(compactVariant(name))
	}
	family := familyKey(model)
	for _, product := range products {
		if inFamily(family, product) {
			add(product)
		}
	}
	return names
}

// drivetrains are trim words that name a drivetrain, which NHTSA often leaves
// off a recall's model: "E450" rather than "E450 4MATIC".
var drivetrains = map[string]bool{
	"4MATIC": true, "4MATIC+": true, "AWD": true, "4WD": true, "2WD": true, "FWD": true, "RWD": true,
	"4X4": true, "4X2": true, "QUATTRO": true, "XDRIVE": true, "SDRIVE": true,
}

// withoutDrivetrain drops trailing drivetrain words: "E450 4MATIC" becomes
// "E450". A name with none is returned as it is.
func withoutDrivetrain(name string) string {
	words := strings.Fields(name)
	for len(words) > 1 && drivetrains[strings.ToUpper(words[len(words)-1])] {
		words = words[:len(words)-1]
	}
	return strings.Join(words, " ")
}

// spacedVariant splits letters from digits in each word: "E450 4MATIC" becomes
// "E 450 4MATIC".
func spacedVariant(name string) string {
	words := strings.Fields(name)
	for i, word := range words {
		if parts := lettersThenDigits.FindStringSubmatch(word); parts != nil {
			words[i] = parts[1] + " " + parts[2]
		}
	}
	return strings.Join(words, " ")
}

// compactVariant joins a letters-only word to a following word that starts
// with a digit: "E 450 4MATIC" becomes "E450 4MATIC".
func compactVariant(name string) string {
	var words []string
	for _, word := range strings.Fields(name) {
		if last := len(words) - 1; last >= 0 && lettersOnly.MatchString(words[last]) && unicode.IsDigit([]rune(word)[0]) {
			words[last] += word
			continue
		}
		words = append(words, word)
	}
	return strings.Join(words, " ")
}

// inFamily reports whether product is a trim of the model family, such as
// "E 450 4MATIC" or "E350" for family "E" but not "EQE 350". A product also
// belongs when its first word is the family, as in "GLC Coupe".
func inFamily(family, product string) bool {
	if family == "" {
		return false
	}
	key := modelKey(product)
	if rest, ok := strings.CutPrefix(key, family); ok && (rest == "" || unicode.IsDigit([]rune(rest)[0])) {
		return true
	}
	words := strings.Fields(product)
	return len(words) > 0 && familyKey(words[0]) == family
}

// familyKey is the key of a model's family name: "E-Class" and "3-Series"
// become "E" and "3".
func familyKey(name string) string {
	return modelKey(familySuffix.ReplaceAllString(strings.TrimSpace(name), ""))
}

// modelKey uppercases name and drops everything but letters and digits.
func modelKey(name string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(name) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
